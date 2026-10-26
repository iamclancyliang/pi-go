// Regenerates golden.json from jsdiff 8.0.4, the version Pi pins: line and
// word diffs of pairs chosen for the cases where a diff could go more than
// one way.
//
//   PI_PIN=<dir with node_modules/diff> node internal/textdiff/testdata/generate.ts
import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const pin = process.env.PI_PIN;
if (!pin) throw new Error("set PI_PIN to a tree whose node_modules has diff 8.0.4");
const Diff = await import(join(pin, "node_modules/diff/libesm/index.js"));

const pairs: [string, string][] = [
	["", ""],
	["a", "a"],
	["a\nb\nc\n", "a\nb\nc\n"],
	["a\nb\nc\n", "a\nx\nc\n"],
	["a\nb\nc", "a\nb\nc\n"],
	["a\nb\nc\n", "a\nc\n"],
	["a\nc\n", "a\nb\nc\n"],
	["x\na\nb\na\nb\n", "a\nb\nx\na\nb\n"],
	["one\ntwo\nthree\nfour\nfive\nsix\n", "one\n2\nthree\nfour\n5\nsix\nseven\n"],
	["a\r\nb\r\nc\r\n", "a\r\nB\r\nc\r\n"],
	["mixed\r\nends\nhere", "mixed\nends\r\nhere"],
	["", "new\nfile\n"],
	["old\nfile\n", ""],
	["}\n}\n", "}\n  x\n}\n}\n"],
	["func a() {\n\treturn 1\n}\n\nfunc b() {\n\treturn 2\n}\n", "func a() {\n\treturn 1\n}\n\nfunc c() {\n\treturn 3\n}\n\nfunc b() {\n\treturn 2\n}\n"],
	["foo bar baz", "foo baz"],
	["foo bar baz", "foo qux baz"],
	["foo\nbar baz", "foo baz"],
	["foo baz", "foo\nbar baz"],
	["foo   bar baz", "foo  baz"],
	["foo\tbar\nbaz", "foo baz"],
	["const x = 1;", "const y = 2;"],
	["    return value", "    return otherValue"],
	["  if (a && b) {", "  if (a || b) {"],
	["hello world", "hello brave new world"],
	["the quick brown fox", "the slow brown dog"],
	["café naïve", "cafe naive"],
	["a.b.c", "a.x.c"],
	["", "added words"],
	["removed words", ""],
	["  leading", "leading"],
	["trailing  ", "trailing"],
	["one two three four", "four three two one"],
	["fmt.Println(\"hi\")", "fmt.Printf(\"hi %s\\n\", name)"],
	["中文 文本", "中文 内容"],
	["x  =  1", "x = 1"],
];

const cases = pairs.map(([a, b]) => ({
	old: a,
	new: b,
	lines: Diff.diffLines(a, b).map((c: any) => ({ value: c.value, count: c.count, added: c.added, removed: c.removed })),
	words: Diff.diffWords(a, b).map((c: any) => ({ value: c.value, count: c.count, added: c.added, removed: c.removed })),
}));

const out = join(dirname(fileURLToPath(import.meta.url)), "golden.json");
writeFileSync(out, `${JSON.stringify(cases, null, "\t")}\n`);
console.log(`wrote ${cases.length} cases to ${out}`);
