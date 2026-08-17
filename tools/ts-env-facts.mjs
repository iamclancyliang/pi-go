/**
 * Emit environment-object facts with SCOPE IDENTITY, using TypeScript's parser.
 *
 * Pairing a delete with a write by the receiver's TEXT is not object identity. Two
 * functions may each declare a local `env`; matching on the name alone lets a
 * delete in one vouch for a write in the other, and reports a legitimate override
 * as unguarded because something elsewhere shares its name. The guarantee being
 * checked -- "this name was cleared from THIS object before being set on it" --
 * is about one object, so the object has to be identified.
 *
 * Identity here is the position of the binding's declaration. Each reference is
 * attributed to the innermost enclosing scope that declares that name, which is
 * what the language does. This is lexical resolution over the AST, not a type
 * checker: a binding reached through a parameter, a property, or a re-export is
 * reported as unresolved rather than guessed, so a caller can tell the difference
 * between "no obligation" and "could not tell".
 *
 * Protocol: `{"<path>": "<source text>", ...}` on stdin, and on stdout
 *
 *   { "<path>": {
 *       "objects": [ { "id", "name", "seeded", "unresolvedSeed" } ],
 *       "writes":  [ { "object", "name", "offset" } ],
 *       "deletes": [ { "object", "name", "offset" } ],
 *       "unresolved": [ { "receiver", "name", "offset", "kind" } ]
 *   } }
 *
 *   objects.id      - declaration offset, unique within the file
 *   objects.seeded  - true when the object literal spreads an inherited
 *                     environment, directly or through a chain of local bindings
 *   writes/deletes  - attributed to an object id
 *   unresolved      - an access whose base could not be resolved to a local
 *                     binding; the caller must not treat these as guarded
 *
 * Offsets are code-POINT indices, matching Python string indexing.
 */
import { createRequire } from "node:module";
import { readFileSync } from "node:fs";

const require = createRequire(process.argv[2] + "/");
let ts;
try {
	ts = require("typescript");
} catch (error) {
	process.stderr.write(`cannot load typescript from ${process.argv[2]}: ${error.message}\n`);
	process.exit(3);
}

/** UTF-16 code units -> code points, so offsets match Python indexing. */
function codePointMapper(source) {
	if (!/[\uD800-\uDBFF]/.test(source)) return (index) => index;
	const adjust = new Int32Array(source.length + 1);
	let pairs = 0;
	for (let index = 0; index < source.length; index += 1) {
		adjust[index] = pairs;
		const code = source.charCodeAt(index);
		if (code >= 0xd800 && code <= 0xdbff && index + 1 < source.length) {
			const next = source.charCodeAt(index + 1);
			if (next >= 0xdc00 && next <= 0xdfff) {
				index += 1;
				adjust[index] = pairs;
				pairs += 1;
			}
		}
	}
	adjust[source.length] = pairs;
	return (index) => index - adjust[Math.min(index, source.length)];
}

/** Whether an expression reads an inherited environment. */
function readsInheritedEnv(node) {
	if (ts.isCallExpression(node)) {
		const callee = node.expression;
		return ts.isIdentifier(callee) && callee.text === "getShellEnv";
	}
	// `process.env`
	return (
		ts.isPropertyAccessExpression(node) &&
		node.name.text === "env" &&
		ts.isIdentifier(node.expression) &&
		node.expression.text === "process"
	);
}

/** `X.env` or a bare `env` identifier: the receiver of an environment access. */
function receiverOf(node) {
	if (ts.isPropertyAccessExpression(node)) {
		// `execution.env.NAME` -> receiver is `execution.env`
		if (ts.isPropertyAccessExpression(node.expression) &&
			node.expression.name.text === "env") {
			return node.expression;
		}
		if (ts.isIdentifier(node.expression)) return node.expression;
	}
	return undefined;
}

function analyse(path, source) {
	const kind = path.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
	const file = ts.createSourceFile(path, source, ts.ScriptTarget.Latest, false, kind);
	const diagnostics = file.parseDiagnostics ?? [];
	if (diagnostics.length > 0) {
		const first = diagnostics[0];
		process.stderr.write(
			`parse error in ${path} at offset ${first.start}: ` +
			`${ts.flattenDiagnosticMessageText(first.messageText, " ")}\n`,
		);
		process.exit(4);
	}

	const toCodePoint = codePointMapper(source);
	const objects = new Map();       // declaration offset -> record
	const writes = [];
	const deletes = [];
	const unresolved = [];

	// A scope is a map from declared name to the declaration node. Scopes nest, and
	// a reference resolves in the innermost one that declares it.
	const scopes = [new Map()];
	const declare = (name, node) => scopes[scopes.length - 1].set(name, node);
	const resolve = (name) => {
		for (let index = scopes.length - 1; index >= 0; index -= 1) {
			const found = scopes[index].get(name);
			if (found) return found;
		}
		return undefined;
	};

	const opensScope = (node) =>
		ts.isSourceFile(node) || ts.isBlock(node) || ts.isFunctionDeclaration(node) ||
		ts.isFunctionExpression(node) || ts.isArrowFunction(node) ||
		ts.isMethodDeclaration(node) || ts.isForStatement(node) ||
		ts.isForOfStatement(node) || ts.isForInStatement(node) ||
		ts.isCaseBlock(node) || ts.isModuleBlock(node) || ts.isCatchClause(node) ||
		ts.isConstructorDeclaration(node) || ts.isGetAccessor(node) ||
		ts.isSetAccessor(node) || ts.isFunctionTypeNode(node);

	/** Does this object literal spread an inherited environment, possibly via a chain? */
	const seededFrom = (initializer) => {
		if (!ts.isObjectLiteralExpression(initializer)) return { seeded: false };
		for (const property of initializer.properties) {
			if (!ts.isSpreadAssignment(property)) continue;
			const spread = property.expression;
			if (readsInheritedEnv(spread)) return { seeded: true };
			// A spread of anything other than a resolvable local binding cannot be
			// classified: `{ ...execution.env }` may or may not carry an inherited
			// environment, and reporting it as not seeded decides a question this
			// resolver cannot answer.
			if (!ts.isIdentifier(spread)) {
				return { seeded: false, unresolvedSeed: true };
			}
			if (ts.isIdentifier(spread)) {
				const binding = resolve(spread.text);
				if (binding && binding.__inherited) return { seeded: true };
				// A binding whose VALUE this resolver did not classify -- a parameter,
				// an import, the result of a call it does not understand -- may hold an
				// inherited environment. Reporting it as not seeded would turn "could
				// not tell" into "no obligation".
				if (!binding || !binding.__known) {
					return { seeded: false, unresolvedSeed: true };
				}
			}
		}
		return { seeded: false };
	};

	/** Declare every identifier a binding pattern introduces. */
	const declarePattern = (name, node) => {
		if (ts.isIdentifier(name)) {
			declare(name.text, node);
			return;
		}
		if (ts.isObjectBindingPattern(name) || ts.isArrayBindingPattern(name)) {
			for (const element of name.elements) {
				if (ts.isBindingElement(element)) declarePattern(element.name, node);
			}
		}
	};

	const visit = (node) => {
		const scoped = opensScope(node);
		if (scoped) scopes.push(new Map());

		// PARAMETERS SHADOW. A parameter named `env` is a different object from an
		// outer `env`, so it must be declared in the scope the function opens before
		// the body is walked; otherwise every access in the body resolves outward and
		// an outer object's delete appears to guard a write to the parameter.
		if (scoped && node.parameters) {
			for (const parameter of node.parameters) {
				declarePattern(parameter.name, parameter);
			}
		}
		// A catch clause binds its own variable in the same way.
		if (ts.isCatchClause(node) && node.variableDeclaration) {
			declarePattern(node.variableDeclaration.name, node.variableDeclaration);
		}

		if (ts.isVariableDeclaration(node) && !ts.isIdentifier(node.name)) {
			// A destructured binding introduces names too; none of them can be an
			// object literal, so they only matter for shadowing.
			declarePattern(node.name, node);
		}
		if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name)) {
			const initializer = node.initializer;
			const offset = toCodePoint(node.name.getStart(file));
			if (initializer) {
				// Mark bindings that hold an inherited environment, so an object
				// spreading them counts as seeded.
				if (readsInheritedEnv(initializer)) {
					node.__inherited = true;
					node.__known = true;
				}
				const seeding = seededFrom(initializer);
				if (seeding.seeded) node.__inherited = true;
				// `__known` marks a binding whose value this resolver classified, so a
				// spread of it is a decidable question. Anything unmarked is unknown.
				if (ts.isObjectLiteralExpression(initializer) && !seeding.unresolvedSeed) {
					node.__known = true;
				}
				if (ts.isObjectLiteralExpression(initializer)) {
					objects.set(offset, {
						id: offset,
						name: node.name.text,
						seeded: Boolean(seeding.seeded),
						unresolvedSeed: Boolean(seeding.unresolvedSeed),
					});
					node.__objectId = offset;
				}
			}
			declare(node.name.text, node);
		}

		// `delete <receiver>.NAME`. A DeleteExpression's operand is `.expression`;
		// `.operand` belongs to PrefixUnaryExpression and is undefined here.
		if (ts.isDeleteExpression(node) && ts.isPropertyAccessExpression(node.expression)) {
			record(node.expression, deletes, "delete", node.getStart(file));
		}

		// `<receiver>.NAME = value`, excluding `==`/`===` which are not assignments
		if (ts.isBinaryExpression(node) &&
			node.operatorToken.kind === ts.SyntaxKind.EqualsToken &&
			ts.isPropertyAccessExpression(node.left)) {
			record(node.left, writes, "write", node.getStart(file));
		}

		ts.forEachChild(node, visit);
		if (scoped) scopes.pop();
	};

	function record(access, into, kind, statementStart) {
		const receiver = receiverOf(access);
		if (!receiver) return;
		const name = access.name.text;
		if (!/^[A-Z][A-Z0-9_]*$/.test(name)) return;

		const offset = toCodePoint(statementStart);
		// `process.env.X` is this process's own environment, a different role.
		if (ts.isPropertyAccessExpression(receiver) &&
			ts.isIdentifier(receiver.expression) &&
			receiver.expression.text === "process") {
			into.push({ object: "process", name, offset });
			return;
		}
		if (ts.isIdentifier(receiver)) {
			const binding = resolve(receiver.text);
			if (binding && binding.__objectId !== undefined) {
				into.push({ object: binding.__objectId, name, offset });
				return;
			}
			unresolved.push({ receiver: receiver.text, name, offset, kind });
			return;
		}
		// `something.env.X`: a property, not a local binding, so identity is unknown.
		unresolved.push({ receiver: receiver.getText(file), name, offset, kind });
	}

	visit(file);
	return {
		objects: [...objects.values()],
		writes,
		deletes,
		unresolved,
	};
}

const input = JSON.parse(readFileSync(0, "utf8"));
const result = {};
for (const [path, contents] of Object.entries(input)) {
	result[path] = analyse(path, contents);
}
process.stdout.write(JSON.stringify(result));
