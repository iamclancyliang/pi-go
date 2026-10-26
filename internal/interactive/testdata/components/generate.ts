// Regenerates golden.json from Pi's interactive components at the pinned
// revision: user and assistant messages, compaction summaries, and tool
// calls drawn by each tool's renderer, in the states they pass through.
//
//   git -C <pi checkout> archive 086c32e packages | tar -x -C <dir>
//   # <dir>/node_modules: links to the checkout's node_modules entries
//   cd internal/interactive/testdata/components
//   PI_PIN=<dir> FORCE_COLOR=3 HOME=/home/tester node --import ./register.mjs generate.ts
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const pin = process.env.PI_PIN;
if (!pin) throw new Error("set PI_PIN to the extracted pinned Pi tree");
const interactive = join(pin, "packages/coding-agent/src/modes/interactive");
const { setThemeInstance, loadThemeFromPath } = await import(join(interactive, "theme/theme.ts"));
setThemeInstance(loadThemeFromPath(join(interactive, "theme/dark.json"), "truecolor"));
const { setKeybindings, setCapabilities } = await import("@earendil-works/pi-tui");
const { KeybindingsManager } = await import(join(pin, "packages/coding-agent/src/core/keybindings.ts"));
setKeybindings(new KeybindingsManager());
setCapabilities({ images: null, trueColor: true, hyperlinks: false });
const { UserMessageComponent } = await import(join(interactive, "components/user-message.ts"));
const { AssistantMessageComponent } = await import(join(interactive, "components/assistant-message.ts"));
const { CompactionSummaryMessageComponent } = await import(join(interactive, "components/compaction-summary-message.ts"));
const { ToolExecutionComponent } = await import(join(interactive, "components/tool-execution.ts"));

const cases: object[] = [];

for (const text of ["hello", "**bold** and `code`", "1) first\n2) second", "\\*escaped\\*", "line1\nline2", "# heading\n\n- a\n- b"]) {
	for (const width of [40, 80]) {
		cases.push({ kind: "user", text, width, lines: new UserMessageComponent(text).render(width) });
	}
}

const call = { type: "toolCall", id: "call-1", name: "read", arguments: { path: "a.txt" } };
const assistant: Record<string, { content: object[]; stopReason?: string; errorMessage?: string }> = {
	text: { content: [{ type: "text", text: "Hello **world**" }] },
	thinkingText: { content: [{ type: "thinking", thinking: "Let me think\n\nabout it" }, { type: "text", text: "Answer" }] },
	twoThinking: { content: [{ type: "thinking", thinking: "t1" }, { type: "thinking", thinking: "t2" }, { type: "text", text: "x" }] },
	onlyThinking: { content: [{ type: "thinking", thinking: "only thinking" }] },
	textTool: { content: [{ type: "text", text: "Calling a tool" }, call] },
	onlyTool: { content: [call] },
	thinkingTool: { content: [{ type: "thinking", thinking: "plan" }, call] },
	length: { content: [{ type: "text", text: "partial" }], stopReason: "length" },
	aborted: { content: [{ type: "text", text: "partial" }], stopReason: "aborted" },
	abortedDefault: { content: [{ type: "text", text: "partial" }], stopReason: "aborted", errorMessage: "Request was aborted" },
	abortedCustom: { content: [], stopReason: "aborted", errorMessage: "Stopped by user" },
	error: { content: [{ type: "text", text: "partial" }], stopReason: "error", errorMessage: "rate limited" },
	errorBare: { content: [], stopReason: "error" },
	errorWithTool: { content: [call], stopReason: "error", errorMessage: "boom" },
	empty: { content: [] },
	blank: { content: [{ type: "text", text: "  " }] },
	blankThinking: { content: [{ type: "thinking", thinking: "  " }, { type: "text", text: "x" }] },
	textThinkingText: { content: [{ type: "text", text: "a" }, { type: "thinking", thinking: "b" }, { type: "text", text: "c" }] },
	markdown: { content: [{ type: "text", text: "# Plan\n\n1. Read `x`\n2. Fix\n\n```go\nfmt.Println()\n```" }] },
};
for (const [name, m] of Object.entries(assistant)) {
	for (const hide of [false, true]) {
		const message = { role: "assistant", content: m.content, stopReason: m.stopReason ?? "stop", errorMessage: m.errorMessage };
		const component = new AssistantMessageComponent(message, hide);
		cases.push({ kind: "assistant", name, message: m, hideThinking: hide, width: 60, lines: component.render(60) });
	}
}

for (const expanded of [false, true]) {
	const c = new CompactionSummaryMessageComponent({ role: "compactionSummary", summary: "## Goal\nDo things", tokensBefore: 12345, timestamp: 0 });
	c.setExpanded(expanded);
	cases.push({ kind: "compaction", tokensBefore: 12345, summary: "## Goal\nDo things", expanded, width: 60, lines: c.render(60) });
}

const cwd = mkdtempSync(join(tmpdir(), "pi-golden-"));
writeFileSync(join(cwd, "notes.txt"), "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n");
mkdirSync(join(cwd, "skills/demo"), { recursive: true });
const ui = { requestRender() {} };
const tick = () => new Promise((r) => setTimeout(r, 20));
const longText = Array.from({ length: 25 }, (_, i) => `out ${i + 1}`).join("\n");
const truncation = (by: string) => ({ truncated: true, truncatedBy: by, totalLines: 5000, totalBytes: 99999, outputLines: 2000, outputBytes: 51200, lastLinePartial: false, firstLineExceedsLimit: false, maxLines: 2000, maxBytes: 51200 });

type Step = { argsComplete?: boolean; started?: boolean; result?: { text: string; isError?: boolean; details?: object }; partial?: boolean; expanded?: boolean };
const tools: Record<string, { name: string; args: object; steps: Step }> = {
	readCall: { name: "read", args: { path: "src/a.txt" }, steps: {} },
	readRange: { name: "read", args: { path: "src/a.txt", offset: 5, limit: 10 }, steps: {} },
	readOffset: { name: "read", args: { path: "src/a.txt", offset: 5 }, steps: {} },
	readNoPath: { name: "read", args: {}, steps: {} },
	readDone: { name: "read", args: { path: "a.txt" }, steps: { argsComplete: true, result: { text: "line1\nline2" } } },
	readExpanded: { name: "read", args: { path: "a.txt" }, steps: { argsComplete: true, result: { text: longText }, expanded: true } },
	readTruncLines: { name: "read", args: { path: "a.txt" }, steps: { argsComplete: true, result: { text: "a\nb", details: { truncation: truncation("lines") } }, expanded: true } },
	readTruncBytes: { name: "read", args: { path: "a.txt" }, steps: { argsComplete: true, result: { text: "a\nb", details: { truncation: truncation("bytes") } }, expanded: true } },
	readError: { name: "read", args: { path: "missing.txt" }, steps: { argsComplete: true, result: { text: "ENOENT: no such file", isError: true } } },
	readAgents: { name: "read", args: { path: "AGENTS.md" }, steps: {} },
	readSkill: { name: "read", args: { path: "skills/demo/SKILL.md", limit: 20 }, steps: {} },
	readHome: { name: "read", args: { path: "/home/tester/notes.txt" }, steps: {} },
	writeShort: { name: "write", args: { path: "out.txt", content: "hello\nworld\n" }, steps: { argsComplete: true } },
	writeLong: { name: "write", args: { path: "out.txt", content: longText }, steps: { argsComplete: true } },
	writeLongExpanded: { name: "write", args: { path: "out.txt", content: longText }, steps: { argsComplete: true, expanded: true } },
	writeDone: { name: "write", args: { path: "out.txt", content: "x" }, steps: { argsComplete: true, result: { text: "Successfully wrote 1 bytes to out.txt" } } },
	writeError: { name: "write", args: { path: "out.txt", content: "x" }, steps: { argsComplete: true, result: { text: "EACCES: permission denied", isError: true } } },
	writeEmpty: { name: "write", args: { path: "out.txt", content: "" }, steps: {} },
	editPending: { name: "edit", args: { path: "notes.txt", edits: [{ oldText: "five", newText: "5" }] }, steps: {} },
	editPreview: { name: "edit", args: { path: "notes.txt", edits: [{ oldText: "five", newText: "5" }] }, steps: { argsComplete: true } },
	editPreviewError: { name: "edit", args: { path: "notes.txt", edits: [{ oldText: "absent", newText: "x" }] }, steps: { argsComplete: true } },
	editMissingFile: { name: "edit", args: { path: "nope.txt", edits: [{ oldText: "a", newText: "b" }] }, steps: { argsComplete: true } },
	editDone: { name: "edit", args: { path: "notes.txt", edits: [{ oldText: "five", newText: "5" }] }, steps: { argsComplete: true, result: { text: "Successfully replaced 1 block(s) in notes.txt.", details: { diff: " 1 one\n-5 five\n+5 5" } } } },
	editMulti: { name: "edit", args: { path: "notes.txt", edits: [{ oldText: "two\nthree", newText: "2\n3\nthree-and-a-half" }, { oldText: "nine", newText: "9" }] }, steps: { argsComplete: true } },
	editError: { name: "edit", args: { path: "notes.txt", edits: [{ oldText: "five", newText: "5" }] }, steps: { argsComplete: true, result: { text: "Could not find the exact text", isError: true } } },
	bashCall: { name: "bash", args: { command: "ls -la" }, steps: {} },
	bashTimeout: { name: "bash", args: { command: "sleep 1", timeout: 30 }, steps: {} },
	bashEmpty: { name: "bash", args: {}, steps: {} },
	bashDone: { name: "bash", args: { command: "echo hi" }, steps: { argsComplete: true, result: { text: "hi" } } },
	bashLong: { name: "bash", args: { command: "seq 25" }, steps: { argsComplete: true, result: { text: longText } } },
	bashLongExpanded: { name: "bash", args: { command: "seq 25" }, steps: { argsComplete: true, result: { text: longText }, expanded: true } },
	bashTruncated: { name: "bash", args: { command: "big" }, steps: { argsComplete: true, result: { text: "tail\n\n[Showing lines 3001-5000 of 5000. Full output: /tmp/pi-bash-1.log]", details: { truncation: truncation("lines"), fullOutputPath: "/tmp/pi-bash-1.log" } } } },
	bashError: { name: "bash", args: { command: "false" }, steps: { argsComplete: true, result: { text: "oops\n\nCommand exited with code 1", isError: true } } },
	bashPartial: { name: "bash", args: { command: "make" }, steps: { argsComplete: true, result: { text: "building" }, partial: true } },
	grepCall: { name: "grep", args: { pattern: "TODO", path: "src", glob: "*.go", limit: 5 }, steps: {} },
	grepDone: { name: "grep", args: { pattern: "TODO" }, steps: { argsComplete: true, result: { text: "a.go:1: TODO x\nb.go:2: TODO y" } } },
	grepLimits: { name: "grep", args: { pattern: "x" }, steps: { argsComplete: true, result: { text: longText, details: { matchLimitReached: 100, truncation: truncation("bytes"), linesTruncated: true } } } },
	grepExpanded: { name: "grep", args: { pattern: "x" }, steps: { argsComplete: true, result: { text: longText }, expanded: true } },
	findCall: { name: "find", args: { pattern: "*.go", limit: 50 }, steps: {} },
	findDone: { name: "find", args: { pattern: "*.go", path: "/home/tester/src" }, steps: { argsComplete: true, result: { text: longText, details: { resultLimitReached: 1000 } } } },
	lsCall: { name: "ls", args: {}, steps: {} },
	lsLimit: { name: "ls", args: { path: "src", limit: 10 }, steps: { argsComplete: true, result: { text: "a\nb", details: { entryLimitReached: 10 } } } },
	customCall: { name: "custom_tool", args: { query: "x", n: 2 }, steps: {} },
	customDone: { name: "custom_tool", args: { query: "x" }, steps: { argsComplete: true, result: { text: "result text" } } },
	customError: { name: "custom_tool", args: {}, steps: { argsComplete: true, result: { text: "failed", isError: true } } },
};
for (const [name, t] of Object.entries(tools)) {
	const c = new ToolExecutionComponent(t.name, "call-1", t.args, {}, undefined, ui as any, cwd);
	if (t.steps.expanded) c.setExpanded(true);
	if (t.steps.argsComplete) c.setArgsComplete();
	await tick();
	if (t.steps.result) {
		c.updateResult({ content: [{ type: "text", text: t.steps.result.text }], isError: !!t.steps.result.isError, details: t.steps.result.details }, !!t.steps.partial);
		await tick();
	}
	cases.push({ kind: "tool", name, tool: t.name, args: t.args, steps: t.steps, width: 60, lines: c.render(60) });
}

const { BashExecutionComponent } = await import(join(interactive, "components/bash-execution.ts"));
const bashCases: Record<string, { command: string; exclude?: boolean; chunks: string[]; complete?: { exitCode?: number; cancelled?: boolean; truncated?: boolean; fullOutputPath?: string }; expanded?: boolean }> = {
	bashRunning: { command: "make", chunks: ["building\n", "step 2"] },
	bashRunningExcluded: { command: "ls", exclude: true, chunks: ["a\n"] },
	bashDoneShort: { command: "echo hi", chunks: ["hi\n"], complete: { exitCode: 0 } },
	bashDoneLong: { command: "seq 30", chunks: [Array.from({ length: 30 }, (_, i) => `${i + 1}`).join("\n")], complete: { exitCode: 0 } },
	bashDoneLongExpanded: { command: "seq 30", chunks: [Array.from({ length: 30 }, (_, i) => `${i + 1}`).join("\n")], complete: { exitCode: 0 }, expanded: true },
	bashFailed: { command: "false", chunks: ["nope\n"], complete: { exitCode: 2 } },
	bashCancelled: { command: "sleep 9", chunks: [], complete: { cancelled: true } },
	bashTruncatedFile: { command: "big", chunks: ["tail\n"], complete: { exitCode: 0, truncated: true, fullOutputPath: "/tmp/out.log" } },
	bashSplitLines: { command: "x", chunks: ["par", "tial\r\nnext\rlast"], complete: { exitCode: 0 } },
	bashNoOutput: { command: "true", chunks: [], complete: { exitCode: 0 } },
};
for (const [name, b] of Object.entries(bashCases)) {
	const c = new BashExecutionComponent(b.command, ui as any, !!b.exclude);
	if (b.expanded) c.setExpanded(true);
	for (const chunk of b.chunks) c.appendOutput(chunk);
	if (b.complete) {
		c.setComplete(b.complete.exitCode, !!b.complete.cancelled, b.complete.truncated ? { truncated: true } : undefined, b.complete.fullOutputPath);
	}
	cases.push({ kind: "bashExecution", name, bash: b, width: 60, lines: c.render(60) });
}

const out = join(dirname(fileURLToPath(import.meta.url)), "golden.json");
writeFileSync(out, `${JSON.stringify({ cwd, cases }, null, "\t")}\n`);
console.log(`wrote ${cases.length} cases to ${out}`);
process.exit(0);
