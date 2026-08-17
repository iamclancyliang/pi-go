/**
 * The parts every TypeScript helper needs, in one place.
 *
 * Loading the compiler, refusing to work on a file that does not parse, and
 * converting UTF-16 offsets to code points were written out three times. Each is a
 * decision that must hold identically across helpers: if one of them starts
 * tolerating a parse error, or reports raw UTF-16 offsets, its facts disagree with
 * the others while looking equally authoritative.
 */
import { createRequire } from "node:module";

/**
 * The checkout's own TypeScript.
 *
 * There is deliberately no bundled fallback: a different compiler version would
 * answer questions about the pinned source differently, and silently.
 */
export function loadTypeScript(repoRoot) {
	const require = createRequire(`${repoRoot.replace(/\/+$/, "")}/`);
	try {
		return require("typescript");
	} catch (error) {
		process.stderr.write(
			`cannot load typescript from ${repoRoot}: ${error.message}\n`,
		);
		process.exit(3);
	}
}

/**
 * Stop on a file that does not parse.
 *
 * Spans and member sets from a file with syntax errors are partial, and a partial
 * answer here is indistinguishable from a complete one downstream.
 */
export function requireParsed(ts, file, path) {
	const diagnostics = file.parseDiagnostics ?? [];
	if (diagnostics.length === 0) return;
	const first = diagnostics[0];
	process.stderr.write(
		`parse error in ${path} at offset ${first.start}: ` +
		`${ts.flattenDiagnosticMessageText(first.messageText, " ")}\n`,
	);
	process.exit(4);
}

/**
 * UTF-16 code units to code points.
 *
 * TypeScript counts code units; a caller indexing Python strings counts code
 * points. They agree only while every character is in the BMP, so one emoji shifts
 * every later offset by one per astral character — and the lengths still match, so
 * nothing looks wrong.
 */
export function codePointMapper(source) {
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
	return (index) => index - adjust[Math.min(Math.max(index, 0), source.length)];
}

/** The script kind an extension implies: parsing `.ts` as TSX breaks `<T>`. */
export function scriptKindFor(ts, path) {
	return path.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
}
