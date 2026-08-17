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
import { readFileSync } from "node:fs";
import { codePointMapper, loadTypeScript, requireParsed, scriptKindFor } from "./ts-shared.mjs";

const ts = loadTypeScript(process.argv[2]);


/** Whether an expression reads an inherited environment. */
function readsInheritedEnv(candidate) {
	const node = candidate && ts.isParenthesizedExpression(candidate)
		? candidate.expression : candidate;
	if (!node) return false;
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

/** Look through parentheses: they are punctuation, not part of the value. */
function unwrapParens(node) {
	let current = node;
	while (current && ts.isParenthesizedExpression(current)) current = current.expression;
	return current;
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
	const kind = scriptKindFor(ts, path);
	const file = ts.createSourceFile(path, source, ts.ScriptTarget.Latest, false, kind);
	requireParsed(ts, file, path);

	const toCodePoint = codePointMapper(source);
	const objects = new Map();       // declaration offset -> record
	const writes = [];
	const deletes = [];
	const unresolved = [];

	// A scope is a map from declared name to the declaration node. Scopes nest, and
	// a reference resolves in the innermost one that declares it.
	// Each scope records whether it is function-level, because `var` is
	// function-scoped and everything else is block-scoped.
	const scopes = [{ names: new Map(), functionLevel: true }];
	const declare = (name, node) => scopes[scopes.length - 1].names.set(name, node);
	const declareVar = (name, node) => {
		for (let index = scopes.length - 1; index >= 0; index -= 1) {
			if (scopes[index].functionLevel) {
				scopes[index].names.set(name, node);
				return;
			}
		}
		scopes[0].names.set(name, node);
	};
	const resolve = (name) => {
		for (let index = scopes.length - 1; index >= 0; index -= 1) {
			const found = scopes[index].names.get(name);
			if (found) return found;
		}
		return undefined;
	};

	const isFunctionLevel = (node) =>
		ts.isSourceFile(node) || ts.isFunctionDeclaration(node) ||
		ts.isFunctionExpression(node) || ts.isArrowFunction(node) ||
		ts.isMethodDeclaration(node) || ts.isConstructorDeclaration(node) ||
		ts.isGetAccessor(node) || ts.isSetAccessor(node) || ts.isModuleBlock(node);

	const opensScope = (node) =>
		ts.isSourceFile(node) || ts.isBlock(node) || ts.isFunctionDeclaration(node) ||
		ts.isFunctionExpression(node) || ts.isArrowFunction(node) ||
		ts.isMethodDeclaration(node) || ts.isForStatement(node) ||
		ts.isForOfStatement(node) || ts.isForInStatement(node) ||
		ts.isCaseBlock(node) || ts.isModuleBlock(node) || ts.isCatchClause(node) ||
		ts.isConstructorDeclaration(node) || ts.isGetAccessor(node) ||
		ts.isSetAccessor(node) || ts.isFunctionTypeNode(node);

	/** Does this object literal spread an inherited environment, possibly via a chain? */
	const seededFrom = (candidate) => {
		const initializer = unwrapParens(candidate);
		if (!initializer || !ts.isObjectLiteralExpression(initializer)) {
			return { seeded: false };
		}
		for (const property of initializer.properties) {
			if (!ts.isSpreadAssignment(property)) continue;
			const spread = unwrapParens(property.expression);
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

	/**
	 * Register the names a scope introduces BEFORE walking its body.
	 *
	 * A lexical binding shadows its whole scope, so `env.X` written above
	 * `const env = ...` in the same function still refers to that binding, not to an
	 * outer one. Registering names as traversal reaches them attributes those earlier
	 * references to the wrong object. `var` is hoisted to the nearest function-level
	 * scope, so a `var` inside a block is visible after the block closes.
	 */
	const hoist = (scopeNode, functionLevel) => {
		// TWO different rules, so two passes. `let`/`const`/`class`/`function` belong
		// to the scope that lexically contains them, so only DIRECT children count --
		// collecting them recursively would leak a block's `let` into the function and
		// make it visible after the block closes. `var` belongs to the nearest
		// function-level scope, so it IS collected recursively, stopping at nested
		// functions, which own their own `var`s.
		// A declaration list appears bare in a `for` header; only a statement wraps
		// one elsewhere. Matching only the statement form leaves `for (let env = ...)`
		// and `for (const env of ...)` unregistered, so writes inside the loop resolve
		// outward to whatever else is named `env`.
		const declarationLists = (node) => {
			if (ts.isVariableStatement(node)) return [node.declarationList];
			if ((ts.isForStatement(node) || ts.isForOfStatement(node) ||
				ts.isForInStatement(node)) && node.initializer &&
				ts.isVariableDeclarationList(node.initializer)) {
				return [node.initializer];
			}
			return [];
		};

		ts.forEachChild(scopeNode, (child) => {
			for (const list of declarationLists(child)) {
				const blockScoped = Boolean(list.flags &
					(ts.NodeFlags.Let | ts.NodeFlags.Const));
				if (!blockScoped) continue;
				for (const declaration of list.declarations) {
					declarePattern(declaration.name, declaration);
					if (ts.isIdentifier(declaration.name)) noteObject(declaration);
				}
			}
			if (declarationLists(child).length) return;
			if (ts.isFunctionDeclaration(child) && child.name) declare(child.name.text, child);
			if (ts.isClassDeclaration(child) && child.name) declare(child.name.text, child);
		});

		if (!functionLevel) return;
		const collectVars = (node) => {
			for (const list of declarationLists(node)) {
				const blockScoped = Boolean(list.flags &
					(ts.NodeFlags.Let | ts.NodeFlags.Const));
				if (blockScoped) continue;
				for (const declaration of list.declarations) {
					if (ts.isIdentifier(declaration.name)) {
						declareVar(declaration.name.text, declaration);
						noteObject(declaration);
					}
				}
			}
			ts.forEachChild(node, (child) => {
				if (isFunctionLevel(child)) return;   // a nested function owns its vars
				collectVars(child);
			});
		};
		ts.forEachChild(scopeNode, (child) => {
			if (isFunctionLevel(child)) return;
			collectVars(child);
		});
	};

	/** Give an object-literal declaration its identity before the body is walked. */
	const noteObject = (declaration) => {
		if (!declaration.initializer || !ts.isIdentifier(declaration.name)) return;
		if (!ts.isObjectLiteralExpression(unwrapParens(declaration.initializer))) return;
		declaration.__objectId = toCodePoint(declaration.name.getStart(file));
	};

	const visit = (node) => {
		const scoped = opensScope(node);
		if (scoped) {
			scopes.push({ names: new Map(), functionLevel: isFunctionLevel(node) });
		}

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
		if (scoped) hoist(node, isFunctionLevel(node));

		if (ts.isVariableDeclaration(node) && !ts.isIdentifier(node.name)) {
			// A destructured binding introduces names too; none of them can be an
			// object literal, so they only matter for shadowing.
			declarePattern(node.name, node);
		}
		if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name)) {
			// Parentheses are not part of the value. Unwrapping in one place and not
			// the others produced an object with no record: the write still resolved to
			// it, so the clear-before-set obligation disappeared without a trace.
			const initializer = unwrapParens(node.initializer);
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
			// The name is already registered by the hoisting pass; re-declaring it here
			// would put a `var` into the block rather than its function scope.
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
