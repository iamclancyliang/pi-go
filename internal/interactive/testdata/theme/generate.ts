// Regenerates golden.json from Pi's theme code at the pinned revision: the
// escape codes the built-in dark theme gives every colour in both colour
// modes, and what chalk's modifiers make of nested and multi-line text.
//
//   git -C <pi checkout> archive 086c32e packages/tui packages/coding-agent | tar -x -C <dir>
//   ln -s <pi checkout>/node_modules <dir>/node_modules
//   PI_PIN=<dir> node internal/interactive/testdata/theme/generate.ts
import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const pin = process.env.PI_PIN;
if (!pin) throw new Error("set PI_PIN to the extracted pinned Pi tree");
const themeDir = join(pin, "packages/coding-agent/src/modes/interactive/theme");
const { loadThemeFromPath } = await import(join(themeDir, "theme.ts"));
const { default: chalk } = await import(join(pin, "node_modules/chalk/source/index.js"));
chalk.level = 3;

const fgNames = [
	"accent", "border", "borderAccent", "borderMuted", "success", "error", "warning", "muted", "dim", "text",
	"thinkingText", "searchMatchText", "userMessageText", "customMessageText", "customMessageLabel", "toolTitle",
	"toolOutput", "mdHeading", "mdLink", "mdLinkUrl", "mdCode", "mdCodeBlock", "mdCodeBlockBorder", "mdQuote",
	"mdQuoteBorder", "mdHr", "mdListBullet", "toolDiffAdded", "toolDiffRemoved", "toolDiffContext", "syntaxComment",
	"syntaxKeyword", "syntaxFunction", "syntaxVariable", "syntaxString", "syntaxNumber", "syntaxType",
	"syntaxOperator", "syntaxPunctuation", "thinkingOff", "thinkingMinimal", "thinkingLow", "thinkingMedium",
	"thinkingHigh", "thinkingXhigh", "thinkingMax", "bashMode",
];
const bgNames = [
	"selectedBg", "scrollbarThumb", "searchMatchBg", "userMessageBg", "customMessageBg", "toolPendingBg",
	"toolSuccessBg", "toolErrorBg",
];

const modes: Record<string, object> = {};
for (const mode of ["truecolor", "256color"]) {
	const theme = loadThemeFromPath(join(themeDir, "dark.json"), mode);
	modes[mode] = {
		fg: Object.fromEntries(fgNames.map((n) => [n, theme.getFgAnsi(n)])),
		bg: Object.fromEntries(bgNames.map((n) => [n, theme.getBgAnsi(n)])),
	};
}

const inputs = ["plain", "a\x1b[22mb", "x\x1b[23my", "line1\nline2", "a\r\nb", "", "\x1b[1mbold\x1b[22m rest"];
const modifiers: Record<string, Record<string, string>> = {};
for (const name of ["bold", "dim", "italic", "underline", "inverse", "strikethrough"]) {
	modifiers[name] = Object.fromEntries(inputs.map((s) => [s, (chalk as any)[name](s)]));
}

const out = join(dirname(fileURLToPath(import.meta.url)), "golden.json");
writeFileSync(out, `${JSON.stringify({ modes, modifiers }, null, "\t")}\n`);
console.log(`wrote ${out}`);
