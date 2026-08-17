/**
 * Emit member sets from the AST, so a family's membership comes from the compiler
 * API rather than from patterns over masked text.
 *
 * Masking spans tells an extractor what is code. It does not make the extraction
 * structural: a regular expression over masked text still infers membership from
 * spelling and layout, so it can miss a declaration form nobody thought of and
 * report a plausible short set. Asking the AST removes the inference.
 *
 * This emitter reports facts, not decisions. Which facts constitute a family stays
 * with the family's extractor, because that is the membership authority and it must
 * remain readable as such.
 *
 * Protocol: `{"<path>": "<source text>", ...}` on stdin, and on stdout
 *
 *   { "<path>": {
 *       "exports": [ { "name", "exportTypeOnly", "meanings", "externalTarget",
 *                      "form", "local" } ],
 *       "typeAliasUnions": { "<alias name>": { "literals", "members" } },
 *       "interfaceKeys": { "<interface name>": [ "<key>", ... ] },
 *       "keyedLiterals": [ { "path", "key", "value" } ],
 *       "objectKeys": { "<const name>": [ "<top-level key>", ... ] },
 *       "comparisons": [ { "left", "operator", "value", "leftBinding" } ],
 *       "callLiterals": [ { "callee", "value", "enclosing" } ],
 *       "bindings": { "<declaration offset>": { "name", "initializer" } }
 *   } }
 *
 *   exports         - three orthogonal facts per export, never compressed into one
 *                     label: what the SOURCE says about the surface
 *                     (`exportTypeOnly`), what the target symbol MEANS
 *                     (`meanings`, possibly several), and whether the target lies
 *                     outside the pinned inputs (`externalTarget`)
 *   exports.form    - the syntax that exported it, so a family can require or
 *                     exclude a form instead of guessing from the name
 *   exports.local   - the local name behind an alias, when they differ
 *   typeAliasUnions - for `type X = "a" | "b"`, its string literals; for a union of
 *                     type references, their names
 *   interfaceKeys   - declared member names, string keys included
 *   keyedLiterals   - `{ key: "value" }` pairs with the property path that reached
 *                     them, so a family can say WHERE a literal has to appear
 *   objectKeys      - top-level keys of an exported object literal. A registry
 *                     whose VALUES are objects has its members in the keys, so a
 *                     literal-only view of it reports nothing
 *   comparisons     - `expr === "literal"` with the left side's text and, when that
 *                     side is a plain identifier, the declaration it resolves to.
 *                     Some sets are defined by what a parser ACCEPTS rather than by
 *                     a type, and that acceptance lives in comparisons. The binding
 *                     matters because one file can hold two variables of the same
 *                     name meaning different things, and a file-wide view of the
 *                     comparisons merges them
 *   bindings        - declared name and initialiser text per declaration offset, so
 *                     a family can say WHICH variable it means
 *   callLiterals    - string arguments of a named call, with the top-level binding
 *                     they sit inside. A schema built from `Type.Literal("x")` calls
 *                     has its members in call arguments, which no key/value or type
 *                     view reaches
 *
 * Offsets are not reported: a family that needs a position needs a span, and spans
 * come from `ts-spans.mjs`.
 */
import { readFileSync } from "node:fs";
import { codePointMapper, loadTypeScript, requireParsed, scriptKindFor } from "./ts-shared.mjs";

const ts = loadTypeScript(process.argv[2]);

/**
 * A Program over the supplied files, so exports come from the CHECKER.
 *
 * Syntax cannot answer what a module exports. `interface Foo {}; export { Foo }`
 * exports a TYPE through a clause that looks identical to a value export, and
 * `namespace N { export const Hidden = 1 }` exports nothing from the module at all.
 * Both were reported wrongly while the classification was syntactic.
 *
 * Files the caller did not supply are not invented: an alias that cannot be
 * resolved is reported with kind `"unknown"`, so a family that needs kinds can
 * refuse to use the result rather than accept a guess.
 */
function buildProgram(files, repoRoot) {
	const options = {
		target: ts.ScriptTarget.Latest,
		module: ts.ModuleKind.ESNext,
		moduleResolution: ts.ModuleResolutionKind.Bundler,
		// This codebase imports with explicit `.ts` extensions, which resolution
		// rejects unless allowed. Without it every re-export's alias resolves to a
		// synthetic symbol and the value/type classification silently collapses to
		// one kind.
		allowImportingTsExtensions: true,
		allowJs: false,
		noEmit: true,
		skipLibCheck: true,
		noResolve: false,
	};
	const host = ts.createCompilerHost(options, true);
	const original = host.getSourceFile.bind(host);
	// The compiler's own lib files are part of the TOOL, not of the pinned source, so
	// they load from the installation. Everything else must come from the caller: a
	// fallback to the file system would classify a re-export by reading the working
	// tree, and `node_modules` is not in the pinned commit at all, so such an answer
	// could never be baseline evidence.
	const libDirectory = ts.getDirectoryPath(ts.sys.getExecutingFilePath());
	const isLib = (fileName) => fileName.startsWith(libDirectory);
	// Keyed by ABSOLUTE path rooted at the checkout: module resolution normalises to
	// absolute paths, so a relatively-keyed map is never consulted and every alias
	// resolves to a synthetic symbol.
	const supplied = new Map(
		Object.entries(files).map(([path, text]) => [`${repoRoot}/${path}`, text]));
	host.getSourceFile = (fileName, languageVersion, onError, shouldCreate) => {
		const text = supplied.get(fileName);
		if (text !== undefined) {
			const kind = scriptKindFor(ts, fileName);
			return ts.createSourceFile(fileName, text, languageVersion, true, kind);
		}
		if (isLib(fileName)) {
			return original(fileName, languageVersion, onError, shouldCreate);
		}
		return undefined;
	};
	host.fileExists = (fileName) =>
		supplied.has(fileName) || (isLib(fileName) && ts.sys.fileExists(fileName));
	host.readFile = (fileName) =>
		supplied.get(fileName) ?? (isLib(fileName) ? ts.sys.readFile(fileName) : undefined);
	const program = ts.createProgram([...supplied.keys()], options, host);
	return { program, checker: program.getTypeChecker() };
}

function analyse(path, program, checker) {
	const file = program.getSourceFile(path);
	if (!file) {
		process.stderr.write(`the program has no source file for ${path}\n`);
		process.exit(4);
	}
	requireParsed(ts, file, path);

	const exports_ = [];
	const typeAliasUnions = {};
	const interfaceKeys = {};
	const keyedLiterals = [];
	const objectKeys = {};
	const comparisons = [];
	const bindings = {};
	const callLiterals = [];

	/** Look through `satisfies T` and `as const`, which wrap the literal. */
	const unwrap = (node) => {
		let current = node;
		while (current && (ts.isSatisfiesExpression(current) ||
			ts.isAsExpression(current) || ts.isParenthesizedExpression(current))) {
			current = current.expression;
		}
		return current;
	};

	const nameOf = (node) =>
		node && (ts.isIdentifier(node) || ts.isStringLiteral(node)) ? node.text : undefined;

	/**
	 * The module's exports, as the CHECKER sees them.
	 *
	 * Syntax cannot answer what a module exports. `interface Foo {}; export { Foo }`
	 * exports a TYPE through a clause identical in shape to a value export, and
	 * `namespace N { export const Hidden = 1 }` exports nothing from the module.
	 * Aliases are followed to classify the target; if the target's module was not
	 * supplied, the kind is `"unknown"` rather than assumed, so a family that needs
	 * kinds can refuse the result instead of accepting a guess.
	 */
	/**
	 * The module's exports as THREE orthogonal facts, because they answer different
	 * questions and compressing them loses one:
	 *
	 *   exportTypeOnly - what the pinned SOURCE states about the export surface.
	 *                    `export { type Token }` is a type-only alias whether or not
	 *                    the target is reachable, so this is baseline evidence on its
	 *                    own.
	 *   meanings       - what the target symbol MEANS, from the checker: value, type,
	 *                    namespace, or several at once. A class is a value and a type;
	 *                    an enum is a value and a type; a class merged with a namespace
	 *                    is all three. One dominant kind cannot say that.
	 *   externalTarget - whether the declaration lives outside the pinned inputs. The
	 *                    barrel re-exports from a bare specifier, and `node_modules` is
	 *                    absent from the pinned commit, so the target's meanings are
	 *                    not determinable from the baseline -- but the export surface
	 *                    still is.
	 */
	function recordExports() {
		const moduleSymbol = checker.getSymbolAtLocation(file);
		if (!moduleSymbol) return;
		for (const symbol of checker.getExportsOfModule(moduleSymbol)) {
			const declarations = symbol.declarations ?? [];

			// The source's own statement about the surface.
			const exportTypeOnly = declarations.some((node) => {
				if (ts.isExportSpecifier(node)) {
					return Boolean(node.isTypeOnly || node.parent?.parent?.isTypeOnly);
				}
				return ts.isTypeAliasDeclaration(node) || ts.isInterfaceDeclaration(node);
			});

			// Whether the declaration is outside the pinned inputs.
			const externalTarget = declarations.some((node) => {
				const specifier = node.parent?.parent?.moduleSpecifier;
				return Boolean(specifier && ts.isStringLiteral(specifier) &&
					!specifier.text.startsWith(".") && !specifier.text.startsWith("/"));
			});

			let target = symbol;
			let resolved = true;
			if (symbol.flags & ts.SymbolFlags.Alias) {
				try {
					target = checker.getAliasedSymbol(symbol);
				} catch {
					resolved = false;
				}
				if (!target || target === symbol || (target.flags & ts.SymbolFlags.Unknown) ||
					!(target.declarations ?? []).length) {
					resolved = false;
				}
			}

			// Every meaning the target carries, not one dominant label.
			const meanings = [];
			if (resolved) {
				const flags = target.flags;
				if (flags & ts.SymbolFlags.Value) meanings.push("value");
				if (flags & (ts.SymbolFlags.Type | ts.SymbolFlags.TypeAlias |
					ts.SymbolFlags.Interface)) meanings.push("type");
				if (flags & ts.SymbolFlags.Namespace) meanings.push("namespace");
			}

			exports_.push({
				name: symbol.getName(),
				exportTypeOnly,
				externalTarget,
				meanings,
				form: declarations[0] ? ts.SyntaxKind[declarations[0].kind] : "unknown",
				...(resolved && target !== symbol ? { local: target.getName() } : {}),
			});
		}
	}

	function recordTypeAliasUnion(node) {
		const name = nameOf(node.name);
		if (!name) return;
		const type = node.type;
		const literals = [];
		const members = [];
		const walk = (candidate) => {
			if (ts.isUnionTypeNode(candidate)) {
				candidate.types.forEach(walk);
				return;
			}
			// An intersection or a parenthesised type can wrap the union:
			// `type AuthPrompt = { signal?: X } & ( | {...} | {...} )`. Descending
			// only through unions reports nothing for that shape.
			if (ts.isIntersectionTypeNode(candidate)) {
				candidate.types.forEach(walk);
				return;
			}
			if (ts.isParenthesizedTypeNode(candidate)) {
				walk(candidate.type);
				return;
			}
			if (ts.isLiteralTypeNode(candidate) && ts.isStringLiteral(candidate.literal)) {
				literals.push(candidate.literal.text);
				return;
			}
			if (ts.isTypeReferenceNode(candidate)) {
				const reference = nameOf(candidate.typeName);
				if (reference) members.push(reference);
				return;
			}
			// A union of object types: report each member's own `type` discriminant.
			if (ts.isTypeLiteralNode(candidate)) {
				for (const member of candidate.members) {
					if (!ts.isPropertySignature(member)) continue;
					if (nameOf(member.name) !== "type") continue;
					if (member.type && ts.isLiteralTypeNode(member.type) &&
						ts.isStringLiteral(member.type.literal)) {
						literals.push(member.type.literal.text);
					}
				}
			}
		};
		walk(type);
		typeAliasUnions[name] = { literals, members };
	}

	function recordInterfaceKeys(node) {
		const name = nameOf(node.name);
		if (!name) return;
		const keys = [];
		for (const member of node.members) {
			const key = nameOf(member.name);
			if (key !== undefined) keys.push(key);
		}
		interfaceKeys[name] = keys;
	}

	/** `{ key: "value" }` with the property path that reached it. */
	function recordKeyedLiterals(node, trail) {
		if (ts.isObjectLiteralExpression(node)) {
			for (const property of node.properties) {
				if (!ts.isPropertyAssignment(property)) continue;
				const key = nameOf(property.name);
				if (key === undefined) continue;
				const initializer = property.initializer;
				if (ts.isStringLiteral(initializer)) {
					keyedLiterals.push({ path: trail.join("."), key, value: initializer.text });
				} else {
					recordKeyedLiterals(initializer, [...trail, key]);
				}
			}
			return;
		}
		if (ts.isArrayLiteralExpression(node)) {
			node.elements.forEach((element) => recordKeyedLiterals(element, trail));
			return;
		}
		if (ts.isCallExpression(node)) {
			const callee = ts.isIdentifier(node.expression)
				? node.expression.text
				: ts.isPropertyAccessExpression(node.expression)
					? node.expression.name.text
					: undefined;
			node.arguments.forEach((argument) =>
				recordKeyedLiterals(argument, callee ? [...trail, `${callee}()`] : trail));
		}
	}

	const scopes = [new Map()];
	const declare = (name, offset) => scopes[scopes.length - 1].set(name, offset);
	const resolve = (name) => {
		for (let index = scopes.length - 1; index >= 0; index -= 1) {
			const found = scopes[index].get(name);
			if (found !== undefined) return found;
		}
		return undefined;
	};
	const opensScope = (node) =>
		ts.isSourceFile(node) || ts.isBlock(node) || ts.isFunctionDeclaration(node) ||
		ts.isFunctionExpression(node) || ts.isArrowFunction(node) ||
		ts.isMethodDeclaration(node) || ts.isForStatement(node) ||
		ts.isForOfStatement(node) || ts.isForInStatement(node) ||
		ts.isCaseBlock(node) || ts.isModuleBlock(node) || ts.isCatchClause(node);

	// A nested declaration must not overwrite the module-level authority: a
	// same-named interface inside a function would replace the exported one, and the
	// family reading it would publish the wrong keys.
	const topLevel = new Set(file.statements);

	// Enclosure is a STACK, not a variable. Setting a name when a top-level binding
	// is seen and never clearing it lets every later call in the file borrow that
	// name, so a call in an unrelated function is reported inside the last binding
	// walked -- and a family scoping by enclosure then absorbs it.
	const enclosure = [];
	// Initializers walked with a binding pushed must not be walked again by the
	// generic recursion, or every fact inside them is reported twice.
	const alreadyDescended = new Set();
	const enclosingBinding = () => (enclosure.length ? enclosure[enclosure.length - 1] : null);

	const visit = (node) => {
		// An initializer already walked with its binding pushed must not be walked
		// again, or every fact inside it is reported twice -- once scoped, once not.
		if (alreadyDescended.has(node)) return;

		const scoped = opensScope(node);
		if (scoped) scopes.push(new Map());
		if (scoped && node.parameters) {
			for (const parameter of node.parameters) {
				if (ts.isIdentifier(parameter.name)) {
					declare(parameter.name.text, parameter.getStart(file));
				}
			}
		}
		if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name)) {
			const offset = node.name.getStart(file);
			declare(node.name.text, offset);
			bindings[offset] = {
				name: node.name.text,
				initializer: node.initializer ? node.initializer.getText(file) : null,
			};
		}

		// `Type.Literal("x")` and friends: members carried in call arguments.
		if (ts.isCallExpression(node)) {
			const callee = ts.isIdentifier(node.expression) ? node.expression.text
				: ts.isPropertyAccessExpression(node.expression)
					? `${node.expression.expression.getText(file)}.${node.expression.name.text}`
					: undefined;
			if (callee) {
				for (const argument of node.arguments) {
					if (ts.isStringLiteral(argument)) {
						callLiterals.push({
							callee,
							value: argument.text,
							enclosing: enclosingBinding(),
						});
					}
				}
			}
		}

		// `expr === "literal"`: some sets are defined by what a parser ACCEPTS
		// rather than by a type, and that acceptance lives in comparisons.
		if (ts.isBinaryExpression(node) &&
			(node.operatorToken.kind === ts.SyntaxKind.EqualsEqualsEqualsToken ||
				node.operatorToken.kind === ts.SyntaxKind.EqualsEqualsToken) &&
			ts.isStringLiteral(node.right)) {
			const leftBinding = ts.isIdentifier(node.left)
				? resolve(node.left.text)
				: undefined;
			comparisons.push({
				left: node.left.getText(file),
				operator: node.operatorToken.kind === ts.SyntaxKind.EqualsEqualsEqualsToken
					? "===" : "==",
				value: node.right.text,
				...(leftBinding !== undefined ? { leftBinding } : {}),
			});
		}

		if (ts.isTypeAliasDeclaration(node) && topLevel.has(node)) recordTypeAliasUnion(node);
		if (ts.isInterfaceDeclaration(node) && topLevel.has(node)) recordInterfaceKeys(node);

		if (ts.isVariableStatement(node) && topLevel.has(node)) {
			for (const declaration of node.declarationList.declarations) {
				const name = nameOf(declaration.name);
				if (declaration.initializer) {
					const initializer = unwrap(declaration.initializer);
					if (name) enclosure.push(name);
					try {
						ts.forEachChild(declaration.initializer, visit);
						alreadyDescended.add(declaration.initializer);
					} finally {
						if (name) enclosure.pop();
					}
					recordKeyedLiterals(initializer, name ? [name] : []);
					if (name && ts.isObjectLiteralExpression(initializer)) {
						objectKeys[name] = initializer.properties
							.filter((property) => ts.isPropertyAssignment(property))
							.map((property) => nameOf(property.name))
							.filter((key) => key !== undefined);
					}
				}
			}
		}
		if (ts.isExpressionStatement(node)) recordKeyedLiterals(node.expression, []);
		if (ts.isReturnStatement(node) && node.expression) {
			recordKeyedLiterals(node.expression, ["return"]);
		}

		ts.forEachChild(node, visit);
		if (scoped) scopes.pop();
	};

	recordExports();
	visit(file);
	return { exports: exports_, typeAliasUnions, interfaceKeys, keyedLiterals,
		objectKeys, comparisons, bindings, callLiterals };
}

const input = JSON.parse(readFileSync(0, "utf8"));
const repoRoot = process.argv[2].replace(/\/+$/, "");
const { program, checker } = buildProgram(input, repoRoot);
const result = {};
for (const path of Object.keys(input)) {
	result[path] = analyse(`${repoRoot}/${path}`, program, checker);
}
process.stdout.write(JSON.stringify(result));
