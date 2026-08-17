# Pi feature inventory — raw source enumeration

**Source baseline:** `earendil-works/pi@086c32e74530564922d011ade23ff582c9d63116` (the approved parity
denominator). Every row below was read from that exact tree via `git show 086c32e:<path>`.

**Owner:** @cc (task #17). **Consumer:** @gpt-codex merges this into `parity-matrix.md` (task #16)
and owns schema, dedup and omission audit.

**This file records what Pi HAS. It deliberately records no pi-go implementation status** — mixing
the two is how an inventory silently becomes a progress report.

> ⚠️ **The local `~/Project/github/pi` checkout is 34 commits BEHIND this baseline**
> (`HEAD=534bcbffb`). All enumeration therefore reads the pinned commit from the object store rather
> than the working tree. Anyone re-running this must do the same, or they will inventory a different
> product.

## Coverage status — read this before treating any section as complete

| Axis | Status |
| --- | --- |
| Workspace packages | ✅ enumerated |
| CLI flags | ✅ enumerated |
| CLI modes | ✅ enumerated |
| Slash commands | ✅ enumerated |
| RPC-mode commands | ✅ enumerated |
| RPC-mode events | ✅ enumerated |
| Wire protocol (`packages/protocol`) | ✅ enumerated |
| Built-in tools | ✅ enumerated |
| Providers | ✅ enumerated (count + list) |
| Extension events/hooks | 🟡 section headings enumerated; per-hook semantics pending |
| Extension context/API methods | 🟡 headings enumerated; signatures pending |
| Resources (skills / prompts / themes / keybindings / context files) | ❌ not yet enumerated |
| Session workflows (fork/clone/tree/import/export/share/compact) | 🟡 named via commands; semantics pending |
| TUI surfaces | ❌ not yet enumerated |
| Telemetry | ❌ not yet enumerated |
| Evals | ❌ not yet enumerated |
| Install / update | ❌ not yet enumerated |
| Auth / usage / model registry | ❌ not yet enumerated |
| Server / client / session-backends | ❌ not yet enumerated |
| Examples | ❌ not yet enumerated |

**Nothing here may be read as "Pi has been fully inventoried" until every row above is ✅.**

---

## 1. Workspace packages

`git ls-tree 086c32e packages/` — 10 packages.

| Package | Path |
| --- | --- |
| agent | `packages/agent` |
| ai | `packages/ai` |
| client | `packages/client` |
| coding-agent | `packages/coding-agent` |
| evals | `packages/evals` |
| protocol | `packages/protocol` |
| server | `packages/server` |
| session-backends | `packages/session-backends` |
| telemetry | `packages/telemetry` |
| tui | `packages/tui` |

Source-file scale at baseline: **1123** `.ts` files under `packages/`.

## 2. CLI

### 2.1 Modes

`packages/coding-agent/src/modes/`

| Mode | Path |
| --- | --- |
| interactive | `modes/interactive/` |
| print | `modes/print-mode.ts` |
| json-event | `modes/json-event.ts` |
| rpc | `modes/rpc/` |

### 2.2 Flags

`packages/coding-agent/src/cli/args.ts`

| Flag | Alias | Line |
| --- | --- | --- |
| `--help` | `-h` | 77 |
| `--version` | `-v` | 79 |
| `--mode` | | 81 |
| `--continue` | `-c` | 86 |
| `--resume` | `-r` | 88 |
| `--provider` | | 90 |
| `--model` | | 92 |
| `--api-key` | | 94 |
| `--system-prompt` | | 96 |
| `--append-system-prompt` | | 98 |
| `--name` | `-n` | 101 |
| `--no-session` | | 107 |
| `--session` | | 109 |
| `--session-id` | | 111 |
| `--fork` | | 113 |
| `--session-dir` | | 115 |
| `--models` | | 117 |
| `--no-tools` | `-nt` | 119 |
| `--no-builtin-tools` | `-nbt` | 121 |
| `--tools` | `-t` | 123 |
| `--exclude-tools` | `-xt` | 128 |
| `--thinking` | | 133 |
| `--print` | `-p` | 143 |
| `--export` | | 150 |
| `--extension` | `-e` | 152 |
| `--no-extensions` | `-ne` | 155 |
| `--skill` | | 157 |
| `--prompt-template` | | 160 |
| `--theme` | | 163 |
| `--use-theme` | | 166 |
| `--no-skills` | `-ns` | 174 |
| `--no-prompt-templates` | `-np` | 176 |
| `--no-themes` | | 178 |
| `--no-context-files` | `-nc` | 180 |
| `--list-models` | | 182 |
| `--tui-mode` | | 189 |
| `--verbose` | | 203 |
| `--approve` | `-a` | 205 |
| `--no-approve` | `-na` | 207 |
| `--offline` | | 209 |

**40 flags.**

### 2.3 Slash commands

`packages/coding-agent/src/core/slash-commands.ts` — **22 commands**, lines 20–41.

`settings`(20) · `model`(21) · `scoped-models`(22) · `export`(23) · `import`(24) · `share`(25) ·
`copy`(26) · `name`(27) · `session`(28) · `changelog`(29) · `hotkeys`(30) · `fork`(31) ·
`clone`(32) · `tree`(33) · `trust`(34) · `login`(35) · `logout`(36) · `new`(37) · `compact`(38) ·
`resume`(39) · `reload`(40) · `quit`(41)

## 3. Two distinct protocol surfaces

**These are different products and must not be conflated.** An earlier pi-go note recorded "pi has
~30 RPC commands"; that is true of the coding-agent RPC mode and false of `packages/protocol`, which
has 9. Both exist.

### 3.1 `packages/protocol` — CBOR multi-session wire protocol

`packages/protocol/src/schemas.ts`, `PROTOCOL_VERSION = 1` (line 3).

**Commands (9):** `list`(291) · `create`(293) · `attach`(299) · `detach`(300) · `prompt`(301) ·
`steer`(302) · `abort`(303) · `set_model`(305) · `set_thinking`(310)

**Frame types:** `hello`(386) · `request`(392) · `response`(424,430) · `event`(437) ·
`hello_error`(419)

**Server events:** `server_snapshot`(401) · `session_snapshot`(402) · `session_progress`(404) ·
`session_removed`(408)

**Error codes (7):** `version` · `busy` · `session_locked` · `not_found` · `invalid_request` ·
`not_implemented` · `internal_error` (270–276)

**Session phases (5):** `idle` · `turn` · `compaction` · `branch_summary` · `retry` (39–43)

**Thinking levels (7):** `off` · `minimal` · `low` · `medium` · `high` · `xhigh` · `max` (27–33)

**Stream events:** `item_started`(206) · `assistant_delta`(210) · `item_updated`(217) ·
`item_finished`(221)

**Content kinds:** `text`(76) · `thinking`(80) · `image`(85) · `toolCall`(90)

**Stop reasons:** `stop` · `length` · `toolUse`(142) · `error`(147) · `aborted`(153)

### 3.2 coding-agent RPC mode — JSON command surface

`packages/coding-agent/docs/rpc.md`. **32 commands** plus **10 UI-dialog request types**.

| Group | Commands (doc line) |
| --- | --- |
| Prompting | `prompt`(43) · `steer`(80) · `follow_up`(102) · `abort`(124) · `new_session`(137) |
| State | `get_state`(162) · `get_messages`(195) |
| Model | `set_model`(217) · `cycle_model`(235) · `get_available_models`(259) |
| Thinking | `set_thinking_level`(281) · `cycle_thinking_level`(298) · `get_available_thinking_levels`(316) |
| Queue modes | `set_steering_mode`(338) · `set_follow_up_mode`(355) |
| Compaction | `compact`(374) · `set_auto_compaction`(413) |
| Retry | `set_auto_retry`(428) · `abort_retry`(441) |
| Bash | `bash`(456) · `abort_bash`(516) |
| Session | `get_session_stats`(531) · `export_html`(574) · `switch_session`(597) · `fork`(615) · `clone`(643) · `get_fork_messages`(671) · `get_entries`(694) · `get_tree`(724) · `get_last_assistant_text`(752) · `set_session_name`(772) |
| Commands | `get_commands`(793) |

**UI dialog requests (server→client):** `select`(1182) · `confirm`(1199) · `input`(1216) ·
`editor`(1232) · `notify`(1248) · `setStatus`(1264) · `setWidget`(1280) · `setTitle`(1297) ·
`set_editor_text`(1310)

**Events (22):** `agent_start` · `agent_end` · `agent_settled` · `turn_start` · `turn_end` ·
`message_start` · `message_update` · `message_end` · `bash_execution_update` ·
`tool_execution_start` · `tool_execution_update` · `tool_execution_end` · `queue_update` ·
`compaction_start` · `compaction_end` · `auto_retry_start` · `auto_retry_end` ·
`summarization_retry_scheduled` · `summarization_retry_attempt_start` ·
`summarization_retry_finished` · `extension_error` (rpc.md 838+)

> Note the three-way distinction pi draws and pi-go must preserve: `agent_end` is one low-level run
> completing, while `agent_settled` means no retry, compaction retry **or** queued continuation
> remains. Collapsing them loses the only signal a client can wait on.

## 4. Built-in tools

Two separate tool sets exist.

### 4.1 coding-agent tools — `packages/coding-agent/src/core/tools/`

| Tool name | File | Line of `name:` |
| --- | --- | --- |
| `bash` | `bash.ts` | 331 |
| `edit` | `edit.ts` | 304 |
| `find` | `find.ts` | 129 |
| `grep` | `grep.ts` | 134 |
| `ls` | `ls.ts` | 106 |
| `read` | `read.ts` | 216 |
| `write` | `write.ts` | 193 |

Supporting (not model-facing): `edit-diff.ts` · `file-mutation-queue.ts` · `output-accumulator.ts` ·
`path-utils.ts` · `render-utils.ts` · `tool-definition-wrapper.ts` · `truncate.ts`

### 4.2 agent harness tools — `packages/agent/src/harness/tools/`

`bash.ts` · `edit.ts` · `edit-diff.ts` · `image.ts` · `read.ts` · `write.ts`
(+ `file-mutation-queue.ts`, `path-utils.ts`, `tool-context.ts`)

> The harness set includes `image` and excludes `find`/`grep`/`ls`. The two sets are **not** the same
> product surface and each needs its own parity row.

## 5. Providers

`packages/ai/src/providers/` — **44** provider implementation files (excluding generated model data,
auth/stream helpers and `images/`).

`amazon-bedrock` · `ant-ling` · `anthropic` · `azure-openai-responses` · `baseten` · `cerebras` ·
`cloudflare-ai-gateway` · `cloudflare-workers-ai` · `deepseek` · `faux` · `fireworks` ·
`github-copilot` · `google` · `google-vertex` · `groq` · `huggingface` · `kimi-coding` · `minimax` ·
`minimax-cn` · `mistral` · `moonshotai` · `moonshotai-cn` · `nvidia` · `openai` · `openai-codex` ·
`opencode` · `opencode-go` · `openrouter` · `openrouter-images` · `qwen-token-plan` ·
`qwen-token-plan-cn` · `qwen-token-plan-individual` · `radius` · `together` · `vercel-ai-gateway` ·
`xai` · `xiaomi` · `xiaomi-token-plan-ams` · `xiaomi-token-plan-cn` · `xiaomi-token-plan-sgp` ·
`zai` · `zai-coding-cn`

> `faux` is a fake provider — relevant to pi-go because v0's deterministic fixture needs an
> equivalent, and Pi already has a sanctioned one.

## 6. Extension surface — partial

`packages/coding-agent/docs/extensions.md`. Headings enumerated; **per-hook semantics still to be
extracted.**

**Event hooks by group:**

| Group | Hooks (doc line) |
| --- | --- |
| Startup | `project_trust`(352) |
| Resource | `resources_discover`(371) |
| Session | `session_start`(392) · `session_info_changed`(404) · `session_before_switch`(415) · `session_before_fork`(434) · `session_before_compact` / `session_compact`(451) · `session_before_tree` / `session_tree`(484) · `session_shutdown`(507) |
| Agent | `before_agent_start`(521) · `agent_start`/`agent_end`/`agent_settled`(558) · `turn_start`/`turn_end`(574) · `message_start`/`message_update`/`message_end`(588) · `tool_execution_start`/`update`/`end`(624) · `context`(648) · `before_provider_headers`(660) · `before_provider_request`(678) · `after_provider_response`(695) |
| Model | `model_select`(713) · `thinking_level_select`(734) |
| Tool | `tool_call`(751) · `tool_result`(815) |
| User bash | `user_bash`(852) |
| Input | `input`(884) |

**`ExtensionContext` members:** `ctx.ui`(937) · `ctx.mode`(941) · `ctx.hasUI`(945) · `ctx.cwd`(949) ·
`ctx.isProjectTrusted()`(967) · `ctx.sessionManager`(973) · `ctx.modelRegistry`/`ctx.model`/
`ctx.thinkingLevel`/`ctx.scopedModels`(986) · `ctx.signal`(992) · `ctx.isIdle()`/`ctx.abort()`/
`ctx.hasPendingMessages()`(1017) · `ctx.shutdown()`(1021) · `ctx.getContextUsage()`(1039) ·
`ctx.compact()`(1050) · `ctx.getSystemPrompt()`(1066)

**`ExtensionCommandContext`:** `getSystemPromptOptions()`(1086) · `waitForIdle()`(1099) ·
`newSession()`(1112) · `fork()`(1145) · `navigateTree()`(1171) · `switchSession()`(1190) ·
`reload()`(1276)

**`ExtensionAPI` methods:** `pi.on`(1334) · `pi.registerTool`(1338) · `pi.sendMessage`(1389) ·
`pi.sendUserMessage`(1412) · `pi.appendEntry`(1444) · `pi.setSessionName`(1462) ·
`pi.getSessionName`(1470) · `pi.setLabel`(1481) · `pi.registerCommand`(1498) · `pi.getCommands`(1533)
· `pi.registerMessageRenderer`(1566) · `pi.registerMarkdownTransformer`(1570) ·
`pi.registerEntryRenderer`(1591)

> `registerTool` is the reason pi-go's tool-registration seam exists at v0, and
> `before_provider_request` / `after_provider_response` sit exactly on pi-go's model port.

## 7. Documentation set (an inventory axis in its own right)

`packages/coding-agent/docs/` — 36 documents, each describing a user-facing capability:
`compaction` · `containerization` · `custom-provider` · `development` · `environment-variables` ·
`extensions` · `index` · `json` · `keybindings` · `llama-cpp` · `models` · `packages` · `quickstart` ·
`rpc` · `sdk` · `security` · `session-format` · `sessions` · `settings` · `shell-aliases` · `skills` ·
`terminal-setup` · `termux` · `themes` · `tmux` · `tui` · `usage` · `windows`
(+ `docs.json`, images)

Plus `packages/agent/docs/`: `harness` · `search` · `telemetry-schema`.

**Each of these is a candidate parity area** and none should be closed without its own row.

---

## Method

- Every fact above was extracted with `git show 086c32e:<path>` or `git ls-tree 086c32e`, never from
  the working tree.
- Line numbers are from the baseline file, so any reviewer can re-derive a row.
- Counts are stated only where the enumeration is mechanical (grep over a definition list); where a
  count would require judgement about what counts as a "feature", the list is given without one.

## Known gaps in this document

1. The ❌ axes in the coverage table are **not started**.
2. Extension hooks have headings but not semantics; a hook name alone is not enough to build a
   parity row.
3. Per-tool input schemas and options are not extracted.
4. `packages/server`, `packages/client`, `packages/session-backends`, `packages/tui`,
   `packages/telemetry`, `packages/evals` have had **no** enumeration beyond existing.
