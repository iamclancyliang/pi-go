/**
 * Emit the non-code spans of a TypeScript file, using TypeScript's own parser.
 *
 * Reason this exists: a hand-written scanner cannot decide `/` reliably. Whether
 * it opens a regular expression or divides depends on grammatical position, not
 * on the preceding character or even the preceding token. Both of these are
 * legal and they differ:
 *
 *     if (ready) /pattern/.test(s);     // `)` closes an if-head  -> regex
 *     const r = compute(x) / 2;        // `)` closes a call       -> division
 *
 * Two successive attempts at a heuristic each produced a false member, so the
 * disambiguation is delegated to the parser that defines the language. The AST
 * gives exact spans for regular expressions, strings and the text parts of
 * templates; comment spans come from the scanner.
 *
 * Protocol: `{"<path>": "<source text>", ...}` on stdin, and on stdout
 *   { "<path>": { "dead": [[start, end], ...], "text": [[start, end], ...] } }
 *
 * Batched because the caller scans several hundred files and one process per
 * file would dominate its runtime.
 *
 *   dead - comments and regular expression literals: not code in either view.
 *   text - string bodies and template TEXT (never a template's `${}` code,
 *          which the parser exposes as ordinary expressions and so is absent
 *          from this list by construction rather than by a rule of ours).
 *
 * Offsets are UTF-16 code-unit indices, which is what both TypeScript and the
 * caller's Python string indexing use for the same text.
 */
import { createRequire } from "node:module";
import { readFileSync } from "node:fs";

const require = createRequire(process.argv[2] + "/");
let ts;
try {
	ts = require("typescript");
} catch (error) {
	process.stderr.write(
		`cannot load typescript from ${process.argv[2]}: ${error.message}\n`,
	);
	process.exit(3);
}

const input = JSON.parse(readFileSync(0, "utf8"));
const result = {};
let source = "";
let file = null;
let dead = [];
let text = [];

/**
 * A template's text parts, excluding its `${}` expressions AND its delimiters.
 *
 * The delimiters must stay visible: the caller locates string literals by their
 * quotes and counts brackets on the same view, so blanking a backtick or a `${`
 * would remove structure rather than content. Every part therefore drops one
 * leading character (`` ` `` or `}`) and one or two trailing characters
 * (`` ` `` or `${`).
 */
function templateTextSpans(node) {
	const spans = [];
	const push = (part, trailing) => {
		const start = part.getStart(file) + 1;
		const end = part.end - trailing;
		if (end > start) spans.push([start, end]);
	};
	if (ts.isNoSubstitutionTemplateLiteral(node)) {
		push(node, 1); // `text`
		return spans;
	}
	push(node.head, 2); // `text${
	const parts = node.templateSpans;
	for (let index = 0; index < parts.length; index += 1) {
		const literal = parts[index].literal;
		// A middle ends with `${`, a tail ends with a backtick.
		push(literal, ts.isTemplateTail(literal) ? 1 : 2);
	}
	return spans;
}

function visit(node) {
	switch (node.kind) {
		case ts.SyntaxKind.RegularExpressionLiteral:
			dead.push([node.getStart(file), node.end]);
			return;
		case ts.SyntaxKind.StringLiteral:
			// Body only: the quotes are structure the caller anchors on.
			if (node.end - 1 > node.getStart(file) + 1) {
				text.push([node.getStart(file) + 1, node.end - 1]);
			}
			return;
		case ts.SyntaxKind.NoSubstitutionTemplateLiteral:
			for (const span of templateTextSpans(node)) text.push(span);
			return;
		case ts.SyntaxKind.TemplateExpression:
			for (const span of templateTextSpans(node)) text.push(span);
			// Keep walking: the `${}` expressions may contain their own literals.
			break;
		default:
			break;
	}
	ts.forEachChild(node, visit);
}

for (const [path, contents] of Object.entries(input)) {
	source = contents;
	// The script kind must follow the extension: parsing a `.ts` file as TSX
	// makes `<T>` look like a JSX tag and the file fails to parse.
	const kind = path.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
	file = ts.createSourceFile(path, source, ts.ScriptTarget.Latest, false, kind);
	dead = [];
	text = [];
	visit(file);

	// Comments are not AST nodes; each one is leading trivia of some token. A plain
	// scan loop over the whole file is NOT a substitute: on reaching `}` it cannot
	// know whether a template resumes without reScanTemplateToken, so it swallowed
	// the remainder of any file containing an interpolated template -- comments after
	// the first template went unreported.
	const comments = [];
	function collectComments(node) {
		const ranges = ts.getLeadingCommentRanges(source, node.pos) ?? [];
		for (const range of ranges) comments.push([range.pos, range.end]);
		ts.forEachChild(node, collectComments);
	}
	collectComments(file);
	// The end-of-file token carries any trailing comments as its leading trivia.
	for (const range of ts.getLeadingCommentRanges(source, file.endOfFileToken.pos) ?? []) {
		comments.push([range.pos, range.end]);
	}
	for (const span of comments) dead.push(span);

	// A syntax error would make the spans unreliable, and silently returning
	// partial spans is exactly the failure this file exists to remove.
	const diagnostics = file.parseDiagnostics ?? [];
	if (diagnostics.length > 0) {
		const first = diagnostics[0];
		process.stderr.write(
			`parse error in ${path} at offset ${first.start}: ${ts.flattenDiagnosticMessageText(first.messageText, " ")}\n`,
		);
		process.exit(4);
	}

	// A comment is leading trivia of every node that starts at the same offset,
	// so the same span arrives several times; blanking is idempotent but the
	// duplicates would make the output needlessly large.
	const unique = (spans) => {
		const seen = new Set();
		return spans.filter(([begin, finish]) => {
			const key = `${begin}:${finish}`;
			if (seen.has(key)) return false;
			seen.add(key);
			return true;
		});
	};
	result[path] = { dead: unique(dead), text: unique(text) };
}

process.stdout.write(JSON.stringify(result));
