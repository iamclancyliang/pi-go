// Regenerates golden.json from Pi's selector components at the pinned
// revision: the model, scope, session, tree, fork, trust, login and logout
// selectors, the question dialog, and the settings list and input they stand on,
// each in the states a user takes it through.
//
//   git -C <pi checkout> archive 086c32e packages | tar -x -C <dir>
//   # <dir>/node_modules: links to the checkout's node_modules entries
//   cd internal/interactive/testdata/selectors
//   PI_PIN=<dir> FORCE_COLOR=3 HOME=/home/tester node --import ../components/register.mjs generate.ts
import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const pin = process.env.PI_PIN;
if (!pin) throw new Error("set PI_PIN to the extracted pinned Pi tree");
const interactive = join(pin, "packages/coding-agent/src/modes/interactive");
const { setThemeInstance, loadThemeFromPath, getSettingsListTheme } = await import(join(interactive, "theme/theme.ts"));
setThemeInstance(loadThemeFromPath(join(interactive, "theme/dark.json"), "truecolor"));
const { setKeybindings, setCapabilities, Input, SettingsList } = await import("@earendil-works/pi-tui");
const { KeybindingsManager } = await import(join(pin, "packages/coding-agent/src/core/keybindings.ts"));
const keybindings = new KeybindingsManager();
setKeybindings(keybindings);
setCapabilities({ images: null, trueColor: true, hyperlinks: false });
const { ExtensionSelectorComponent } = await import(join(interactive, "components/extension-selector.ts"));
const { TrustSelectorComponent } = await import(join(interactive, "components/trust-selector.ts"));
const { UserMessageSelectorComponent } = await import(join(interactive, "components/user-message-selector.ts"));
const { OAuthSelectorComponent } = await import(join(interactive, "components/oauth-selector.ts"));
const { ModelSelectorComponent } = await import(join(interactive, "components/model-selector.ts"));
const { ScopedModelsSelectorComponent } = await import(join(interactive, "components/scoped-models-selector.ts"));
const { SessionSelectorComponent } = await import(join(interactive, "components/session-selector.ts"));
const { TreeSelectorComponent } = await import(join(interactive, "components/tree-selector.ts"));
const { LoginDialogComponent } = await import(join(interactive, "components/login-dialog.ts"));

const tui = { requestRender() {} };
const noop = () => {};
const tick = () => new Promise((resolve) => setTimeout(resolve, 20));
const cases: object[] = [];
const WIDTH = 80;

function send(component: { handleInput(data: string): void }, keys: string[]): void {
	for (const key of keys) component.handleInput(key);
}

// --- the question dialog ---
for (const keys of [[], ["\x1b[B"], ["j", "j", "k"]]) {
	const dialog = new ExtensionSelectorComponent("Summarize branch?", ["No summary", "Summarize", "Summarize with custom prompt"], noop, noop);
	send(dialog, keys);
	cases.push({ kind: "choice", title: "Summarize branch?", options: ["No summary", "Summarize", "Summarize with custom prompt"], keys, width: WIDTH, lines: dialog.render(WIDTH) });
}

// --- trust ---
const cwd = "/home/tester/work/project";
for (const [name, saved, trusted, keys] of [
	["fresh", null, false, []],
	["parentTrusted", { path: "/home/tester/work", decision: true }, true, []],
	["refused", { path: cwd, decision: false }, false, ["\x1b[A"]],
] as const) {
	const selector = new TrustSelectorComponent({ cwd, savedDecision: saved, projectTrusted: trusted, onSelect: noop, onCancel: noop });
	send(selector, [...keys]);
	cases.push({ kind: "trust", name, cwd, saved, trusted, keys, width: WIDTH, lines: selector.render(WIDTH) });
}

// --- fork ---
const forkMessages = [
	{ id: "m1", text: "first question" },
	{ id: "m2", text: "a long\nmulti line question that goes on and on past the width of a narrow terminal" },
	{ id: "m3", text: "third" },
];
for (const [name, initial, width, keys] of [["last", undefined, WIDTH, []], ["first", "m1", WIDTH, []], ["narrow", "m2", 40, ["\x1b[B"]]] as const) {
	const selector = new UserMessageSelectorComponent(forkMessages, noop, noop, initial);
	send(selector.getMessageList(), [...keys]);
	cases.push({ kind: "fork", name, messages: forkMessages, initial: initial ?? "", keys, width, lines: selector.render(width) });
}

// --- login and logout ---
const providers = [
	{ id: "deepseek", name: "deepseek", authType: "api_key" },
	{ id: "ollama", name: "ollama", authType: "api_key", status: { type: "api_key", source: "stored credential" } },
	{ id: "openai", name: "openai", authType: "api_key", status: { type: "api_key", source: "OPENAI_API_KEY" } },
];
for (const [name, mode, list, search, keys] of [
	["login", "login", providers, "", []],
	["loginSearched", "login", providers, "", ["o", "p"]],
	["loginInitial", "login", providers, "olla", []],
	["logout", "logout", [providers[1]], "", []],
	["logoutEmpty", "logout", [], "", []],
] as const) {
	const selector = new OAuthSelectorComponent(mode, [...list], noop, noop, search || undefined);
	send(selector, [...keys]);
	cases.push({
		kind: "auth", name, logout: mode === "logout", search, keys, width: WIDTH,
		providers: list.map((p) => ({ id: p.id, name: p.name, source: p.status?.source ?? "" })),
		lines: selector.render(WIDTH),
	});
}

// --- the login dialog ---
{
	const dialog = new LoginDialogComponent(tui, "deepseek", noop, "deepseek");
	void dialog.showPrompt("Enter API key");
	cases.push({ kind: "login", provider: "deepseek", message: "Enter API key", width: WIDTH, lines: dialog.render(WIDTH) });
}

// --- models ---
const models = [
	{ provider: "deepseek", id: "deepseek-chat", name: "deepseek-chat" },
	{ provider: "openai", id: "gpt-5", name: "gpt-5" },
	{ provider: "ollama", id: "llama", name: "llama" },
	{ provider: "ollama", id: "qwen3", name: "qwen3" },
];
const refs = (list: typeof models) => list.map((m) => ({ provider: m.provider, id: m.id }));
const modelRuntime = {
	getAvailableSnapshot: () => models,
	getModel: (provider: string, id: string) => models.find((m) => m.provider === provider && m.id === id),
	refresh: async () => ({ errors: new Map(), aborted: false }),
	getError: () => undefined,
};
const settingsManager = { setDefaultModelAndProvider() {} };
for (const [name, current, scoped, search, keys, settle] of [
	["refreshing", models[1], [], "", [], false],
	["refreshed", models[1], [], "", [], true],
	["searched", models[1], [], "", ["l", "l", "a"], true],
	["initialSearch", models[0], [], "qwen", [], true],
	["scoped", models[0], [models[2], models[3]], "", [], true],
	["scopedAll", models[0], [models[2], models[3]], "", ["\t", "\x1b[B"], true],
] as const) {
	const selector = new ModelSelectorComponent(tui, current, settingsManager, modelRuntime, scoped.map((m) => ({ model: m })), noop, noop, search || undefined);
	if (settle) await tick();
	send(selector, [...keys]);
	cases.push({
		kind: "model", name, current: { provider: current.provider, id: current.id }, models: refs(models),
		scoped: refs([...scoped]), search, keys, settled: settle, width: WIDTH, lines: selector.render(WIDTH),
	});
	selector.dispose();
}

// --- the scope ---
for (const [name, enabled, status, keys] of [
	["all", null, "Refreshing model catalogs…", []],
	["toggled", null, "", ["\r"]],
	["ordered", ["openai/gpt-5", "nowhere/x", "ollama/llama"], "Model catalogs refreshed.", ["\x1b[B", "\x1b[B"]],
	["searched", ["openai/gpt-5"], "", ["q", "w"]],
] as const) {
	const selector = new ScopedModelsSelectorComponent(
		{ allModels: models, enabledModelIds: enabled ? [...enabled] : null, refreshStatus: status || undefined },
		{ onChange: noop, onPersist: noop, onCancel: noop },
	);
	send(selector, [...keys]);
	cases.push({ kind: "scoped", name, models: refs(models), enabled, refreshStatus: status, keys, width: WIDTH, lines: selector.render(WIDTH) });
}

// --- sessions ---
const now = new Date();
const minute = 60_000;
const sessions = [
	{ path: "/sessions/a.jsonl", id: "a", cwd: "/home/tester/work", name: "named one", offset: 3 * 24 * 60 * minute, messageCount: 2, firstMessage: "the first message", allMessagesText: "the first message and its answer" },
	{ path: "/sessions/b.jsonl", id: "b", cwd: "/home/tester/work", offset: 2 * 60 * minute, messageCount: 4, firstMessage: "second talk about the parser", allMessagesText: "second talk about the parser fixed" },
	{ path: "/sessions/c.jsonl", id: "c", cwd: "/home/tester/other", parentSessionPath: "/sessions/b.jsonl", offset: 10 * minute, messageCount: 1, firstMessage: "forked from b", allMessagesText: "forked from b" },
	{ path: "/sessions/d.jsonl", id: "d", cwd: "/home/tester/work", parentSessionPath: "/sessions/b.jsonl", offset: 5 * 60 * minute, messageCount: 3, firstMessage: "another fork", allMessagesText: "another fork of the parser talk" },
];
const infos = sessions.map((s) => ({
	path: s.path, id: s.id, cwd: s.cwd, name: s.name, parentSessionPath: s.parentSessionPath,
	created: new Date(now.getTime() - s.offset - minute), modified: new Date(now.getTime() - s.offset),
	messageCount: s.messageCount, firstMessage: s.firstMessage, allMessagesText: s.allMessagesText,
}));
for (const [name, keys] of [
	["threaded", []],
	["path", ["\x10"]],
	["recent", ["\x13", "\x1b[B"]],
	["named", ["\x0e"]],
	["searched", ["p", "a", "r", "s"]],
	["regex", ["r", "e", ":", "f", "o", "r", "k"]],
	["deleting", ["\x1b[B", "\x04"]],
	["deleteCurrent", ["\x04"]],
	["rename", ["\x12"]],
] as const) {
	const selector = new SessionSelectorComponent(
		async () => infos, async () => infos, noop, noop, noop, noop,
		{ renameSession: async () => {}, showRenameHint: true, keybindings }, "/sessions/b.jsonl",
	);
	await tick();
	send(selector, [...keys]);
	cases.push({ kind: "session", name, now: now.toISOString(), sessions, currentPath: "/sessions/b.jsonl", keys, width: WIDTH, lines: selector.render(WIDTH) });
}

// --- the tree ---
const at = (n: number) => new Date(Date.UTC(2026, 0, 15, 10, n)).toISOString();
const entries = [
	{ type: "message", id: "e1", parentId: null, timestamp: at(1), message: { role: "user", content: "first question", timestamp: 1 } },
	{ type: "message", id: "e2", parentId: "e1", timestamp: at(2), message: { role: "assistant", content: [{ type: "text", text: "first answer" }, { type: "toolCall", id: "c1", name: "read", arguments: { path: "/home/tester/a.go", offset: 10, limit: 5 } }, { type: "toolCall", id: "c2", name: "custom_tool", arguments: { alpha: "1", beta: "a value long enough to be cut" } }], stopReason: "toolUse", timestamp: 2 } },
	{ type: "message", id: "e3", parentId: "e2", timestamp: at(3), message: { role: "toolResult", toolCallId: "c1", toolName: "read", content: [{ type: "text", text: "the file" }], isError: false, timestamp: 3 } },
	{ type: "message", id: "e4", parentId: "e3", timestamp: at(4), message: { role: "toolResult", toolCallId: "c2", toolName: "custom_tool", content: [{ type: "text", text: "done" }], isError: false, timestamp: 4 } },
	{ type: "message", id: "e5", parentId: "e4", timestamp: at(5), message: { role: "user", content: "second question", timestamp: 5 } },
	{ type: "message", id: "e6", parentId: "e5", timestamp: at(6), message: { role: "assistant", content: [{ type: "text", text: "second answer" }], stopReason: "stop", timestamp: 6 } },
	{ type: "message", id: "e7", parentId: "e4", timestamp: at(7), message: { role: "user", content: "a different second question\nwith a second line", timestamp: 7 } },
	{ type: "message", id: "e8", parentId: "e7", timestamp: at(8), message: { role: "assistant", content: [{ type: "text", text: "another answer" }], stopReason: "stop", timestamp: 8 } },
	{ type: "compaction", id: "e9", parentId: "e8", timestamp: at(9), summary: "what happened", firstKeptEntryId: "e7", tokensBefore: 12345 },
	{ type: "branch_summary", id: "e10", parentId: "e6", timestamp: at(10), fromId: "e6", summary: "explored the other branch" },
	{ type: "model_change", id: "e11", parentId: "e10", timestamp: at(11), provider: "openai", modelId: "gpt-5" },
	{ type: "thinking_level_change", id: "e12", parentId: "e11", timestamp: at(12), thinkingLevel: "high" },
	{ type: "session_info", id: "e13", parentId: "e12", timestamp: at(13), name: "my session" },
	{ type: "label", id: "e14", parentId: "e13", timestamp: at(14), targetId: "e5", label: "the fork" },
	{ type: "message", id: "e15", parentId: "e14", timestamp: at(15), message: { role: "bashExecution", command: "ls -la", output: "", exitCode: 0, cancelled: false, truncated: false, timestamp: 15 } },
	{ type: "message", id: "e16", parentId: "e15", timestamp: at(16), message: { role: "user", content: "third question", timestamp: 16 } },
];
function buildTree(all: any[]) {
	const labels = new Map<string, { label: string; at: string }>();
	for (const e of all) {
		if (e.type !== "label") continue;
		if (e.label) labels.set(e.targetId, { label: e.label, at: e.timestamp });
		else labels.delete(e.targetId);
	}
	const nodes = new Map<string, any>();
	for (const e of all) nodes.set(e.id, { entry: e, children: [], label: labels.get(e.id)?.label, labelTimestamp: labels.get(e.id)?.at });
	const roots: any[] = [];
	for (const e of all) {
		const node = nodes.get(e.id);
		const parent = e.parentId ? nodes.get(e.parentId) : undefined;
		if (parent) parent.children.push(node);
		else roots.push(node);
	}
	for (const node of nodes.values()) node.children.sort((a: any, b: any) => new Date(a.entry.timestamp).getTime() - new Date(b.entry.timestamp).getTime());
	return roots;
}
const treeNow = new Date(Date.UTC(2026, 2, 3, 12, 0)).toISOString();
for (const [name, leaf, filter, initial, keys, width] of [
	["default", "e16", "default", "", [], WIDTH],
	["all", "e16", "all", "", [], WIDTH],
	["noTools", "e16", "no-tools", "", [], WIDTH],
	["userOnly", "e16", "user-only", "", [], WIDTH],
	["labeled", "e16", "labeled-only", "", [], WIDTH],
	["otherBranch", "e9", "default", "", [], WIDTH],
	["initial", "e16", "default", "e5", [], WIDTH],
	["searched", "e16", "default", "", ["s", "e", "c", "o", "n", "d"], WIDTH],
	["folded", "e16", "default", "e5", ["\x1b[1;3D"], WIDTH],
	["labelTime", "e16", "default", "e5", ["T"], WIDTH],
	["labelEdit", "e16", "default", "e5", ["L"], WIDTH],
	["cycled", "e16", "default", "", ["\x0f", "\x0f"], WIDTH],
	["narrow", "e16", "default", "", [], 48],
	["pagedUp", "e16", "default", "", ["\x1b[D"], WIDTH],
] as const) {
	const selector = new TreeSelectorComponent(buildTree(entries), leaf, 30, noop, noop, noop, initial || undefined, filter);
	send(selector, [...keys]);
	cases.push({ kind: "tree", name, entries, leaf, filter, initial, keys, now: treeNow, width, lines: selector.render(width) });
}

// --- the settings list ---
const settingItems = [
	{ id: "autocompact", label: "Auto-compact", description: "Automatically compact context when it gets too large", currentValue: "true", values: ["true", "false"] },
	{ id: "steering-mode", label: "Steering mode", description: "Enter while streaming queues steering messages. 'one-at-a-time': deliver one, wait for response. 'all': deliver all at once.", currentValue: "one-at-a-time", values: ["one-at-a-time", "all"] },
	{ id: "a-very-long-label-indeed-and-more", label: "A very long label indeed and more words", description: "", currentValue: "x", values: ["x", "y"] },
	{ id: "thinking", label: "Thinking level", description: "Reasoning depth for thinking-capable models", currentValue: "off", values: ["off", "low"] },
];
for (const [name, search, keys] of [["plain", false, []], ["searchable", true, []], ["searched", true, ["s", "t", "e"]], ["cycled", false, ["\x1b[B", "\r"]]] as const) {
	const items = settingItems.map((i) => ({ ...i }));
	const list = new SettingsList(items, 10, getSettingsListTheme(), noop, noop, { enableSearch: search });
	send(list, [...keys]);
	cases.push({ kind: "settingsList", name, items: settingItems, searchEnabled: search, keys, width: WIDTH, lines: list.render(WIDTH) });
}

// --- the input ---
for (const [name, value, focused, keys, width] of [
	["empty", "", false, [], 20],
	["short", "hello world", true, ["\x05"], 20],
	["scrolledEnd", "0123456789abcdefghijklmnopqrstuvwxyz", false, ["\x05"], 20],
	["scrolledMiddle", "0123456789abcdefghijklmnopqrstuvwxyz", true, ["\x05", "\x1b[D", "\x1b[D", "\x1b[D", "\x1b[D", "\x1b[D", "\x1b[D", "\x1b[D", "\x1b[D"], 20],
	["wide", "漢字かな混じり文です", false, ["\x1b[C", "\x1b[C"], 12],
] as const) {
	const input = new Input();
	input.setValue(value);
	input.focused = focused;
	send(input, [...keys]);
	cases.push({ kind: "input", name, value, focused, keys, width, lines: input.render(width) });
}

const out = join(dirname(fileURLToPath(import.meta.url)), "golden.json");
writeFileSync(out, JSON.stringify({ cases }, null, "\t") + "\n");
console.log(`${cases.length} cases -> ${out}`);
