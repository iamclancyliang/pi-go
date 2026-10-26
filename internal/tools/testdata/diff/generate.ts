// Regenerates golden.json from Pi's generateDiffString at the pinned
// revision — the diff an edit reports and the interface draws.
//
//   git -C <pi checkout> archive 086c32e packages/coding-agent | tar -x -C <dir>
//   ln -s <pi checkout>/node_modules <dir>/node_modules
//   PI_PIN=<dir> node internal/tools/testdata/diff/generate.ts
import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const pin = process.env.PI_PIN;
if (!pin) throw new Error("set PI_PIN to the extracted pinned Pi tree");
const { generateDiffString } = await import(join(pin, "packages/coding-agent/src/core/tools/edit-diff.ts"));

const lines = (n: number, f = (i: number) => `line ${i}`) => Array.from({ length: n }, (_, i) => f(i + 1)).join("\n");
const pairs: [string, string][] = [
	["a\nb\nc\n", "a\nx\nc\n"],
	["a\nb\nc", "a\nb\nc\nd"],
	[lines(30), lines(30).replace("line 15", "line fifteen")],
	[lines(30), lines(30).replace("line 3", "line three").replace("line 27", "line twenty-seven")],
	[lines(30), lines(30).replace("line 10", "line ten").replace("line 14", "line fourteen")],
	[lines(120), lines(120).replace("line 100", "changed")],
	[lines(12), lines(12).replace("line 1\n", "")],
	[lines(12), `new first\n${lines(12)}`],
	[lines(12), `${lines(12)}\nnew last`],
	["func a() {\n\treturn 1\n}\n", "func a() {\n\treturn 2\n}\n\nfunc b() {}\n"],
	["", "created\n"],
	["only\n", ""],
	["same\n", "same\n"],
	["x\na\nb\na\nb\n", "a\nb\nx\na\nb\n"],
	[lines(9), lines(9).replace("line 2", "two").replace("line 8", "eight")],
	[lines(9), lines(9).replace("line 1", "one").replace("line 9", "nine")],
];

const cases = pairs.map(([a, b]) => {
	const r = generateDiffString(a, b);
	return { old: a, new: b, diff: r.diff, first: r.firstChangedLine ?? 0 };
});

const out = join(dirname(fileURLToPath(import.meta.url)), "golden.json");
writeFileSync(out, `${JSON.stringify(cases, null, "\t")}\n`);
console.log(`wrote ${cases.length} cases to ${out}`);
