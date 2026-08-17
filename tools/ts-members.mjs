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
 *       "exports": [ { "name", "kind", "form", "local", "module" } ],
 *       "typeAliasUnions": { "<alias name>": { "literals", "members" } },
 *       "interfaceKeys": { "<interface name>": [ "<key>", ... ] },
 *       "keyedLiterals": [ { "path", "key", "value" } ],
 *       "objectKeys": { "<const name>": [ "<top-level key>", ... ] }
 *   } }
 *
 *   exports.kind    - "value" or "type": TypeScript's two declaration spaces, in
 *                     which a value and a type may share a name up to case
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
 *
 * Offsets are not reported: a family that needs a position needs a span, and spans
 * come from `ts-spans.mjs`.
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

	const exports_ = [];
	const typeAliasUnions = {};
	const interfaceKeys = {};
	const keyedLiterals = [];
	const objectKeys = {};

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

	/** `export { a, type b, c as d } from "..."` and `export type { e }`. */
	function recordExportClause(statement) {
		const clause = statement.exportClause;
		if (!clause || !ts.isNamedExports(clause)) return;
		const wholeClauseIsType = Boolean(statement.isTypeOnly);
		for (const element of clause.elements) {
			const exported = element.name.text;
			const local = element.propertyName ? element.propertyName.text : undefined;
			const isType = wholeClauseIsType || Boolean(element.isTypeOnly);
			exports_.push({
				name: exported,
				kind: isType ? "type" : "value",
				form: wholeClauseIsType ? "export-type-clause" : "export-clause",
				...(local && local !== exported ? { local } : {}),
				...(statement.moduleSpecifier
					? { module: statement.moduleSpecifier.text }
					: {}),
			});
		}
	}

	/** `export const x`, `export function y`, `export type Z`, `export default`. */
	function recordDeclaredExport(node) {
		const modifiers = ts.canHaveModifiers(node) ? ts.getModifiers(node) ?? [] : [];
		if (!modifiers.some((m) => m.kind === ts.SyntaxKind.ExportKeyword)) return;
		const isDefault = modifiers.some((m) => m.kind === ts.SyntaxKind.DefaultKeyword);
		const typeSpace =
			ts.isTypeAliasDeclaration(node) || ts.isInterfaceDeclaration(node);
		const collect = (name, form) =>
			exports_.push({
				name,
				kind: typeSpace ? "type" : "value",
				form: isDefault ? `${form}-default` : form,
			});

		if (ts.isVariableStatement(node)) {
			for (const declaration of node.declarationList.declarations) {
				const name = nameOf(declaration.name);
				if (name) collect(name, "declaration");
			}
			return;
		}
		const name = nameOf(node.name);
		if (name) collect(name, "declaration");
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

	const visit = (node) => {
		if (ts.isExportDeclaration(node)) recordExportClause(node);
		else recordDeclaredExport(node);

		if (ts.isTypeAliasDeclaration(node)) recordTypeAliasUnion(node);
		if (ts.isInterfaceDeclaration(node)) recordInterfaceKeys(node);

		if (ts.isVariableStatement(node)) {
			for (const declaration of node.declarationList.declarations) {
				const name = nameOf(declaration.name);
				if (declaration.initializer) {
					const initializer = unwrap(declaration.initializer);
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
	};

	visit(file);
	return { exports: exports_, typeAliasUnions, interfaceKeys, keyedLiterals, objectKeys };
}

const input = JSON.parse(readFileSync(0, "utf8"));
const result = {};
for (const [path, contents] of Object.entries(input)) {
	result[path] = analyse(path, contents);
}
process.stdout.write(JSON.stringify(result));
