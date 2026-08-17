# Pi feature inventory — raw source census

**Source baseline:** `earendil-works/pi@086c32e74530564922d011ade23ff582c9d63116` (approved parity
denominator). Every row was read from that exact tree via `git show 086c32e:<path>`.

**Owner:** @cc (task #17). **Consumer:** @gpt-codex (task #16) owns schema, merge, dedup and the
omission audit in `parity-matrix.md`. **This file records what Pi HAS and deliberately records no
pi-go implementation status.**

> ⚠️ **The local `~/Project/github/pi` checkout is 34 commits BEHIND this baseline**
> (`HEAD=534bcbffb`). Everything here reads the pinned commit from the object store, never the
> working tree. Re-runners must do the same or they will inventory a different product.

## Schema

`Feature ID` = `<area>.<surface>.<slug>` · `Kind` ∈ command/flag/mode/tool/event/hook/ui-api/
public-api/workflow/data-format/provider/auth/usage/component/engineering-evidence ·
`Semantics` = observable behaviour, never just the symbol name · `Pi evidence` = `path:line` at
`086c32e` · `Docs evidence` = `path:line` or `none` · `Coverage state` ∈ `enumerated` /
`semantics-needed` / `schema-needed` / `source-gap`.

One feature = one ID with multiple evidence rows. Distinct commands/events/hooks/tools/API methods
always get distinct IDs.

## Coverage ledger

| Axis | State |
| --- | --- |
| Workspace packages | `enumerated` |
| CLI modes | `enumerated` |
| CLI flags | `enumerated` (40) · semantics per flag `semantics-needed` |
| Slash commands | `enumerated` (22) · semantics from descriptions |
| Wire protocol (CBOR) | `enumerated` |
| coding-agent RPC commands | `enumerated` (32, **re-derived from `RpcCommand` union**) · payloads partially typed at `rpc-types.ts:117-210` |
| coding-agent RPC events | `enumerated` (**24**, source union; 3 are `source-only`) |
| RPC UI-dialog requests | `enumerated` (9, from source type) |
| Built-in tools ×2 sets | `enumerated`; coding-agent input schemas **closed** (§15); harness schemas `schema-needed` |
| Providers | `enumerated` (**42** IDs = 40 text + 1 image + 1 fake; registry-derived) |
| **Model catalogue** | **`source-gap` — not in the repo, see §7.2** |
| Auth / OAuth | `enumerated` (files) · flows `semantics-needed` |
| Extension hooks | `enumerated` (names); semantics done for `tool_call`/`tool_result` (§19), rest `semantics-needed` |
| Extension context / API | `enumerated` (names) · signatures `schema-needed` |
| TUI | `enumerated` (components) · `semantics-needed` |
| Telemetry | `enumerated` (files) · `schema-needed` |
| Evals | `enumerated` (files) · `semantics-needed` |
| server / client / session-backends | `enumerated` (files) · `semantics-needed` |
| Resources (skills/prompts/themes/keybindings/context files) | `enumerated` (modules) · `semantics-needed` |
| Settings keys | `enumerated` (§16) |
| Environment variables | `enumerated` (§17, **docs-derived — needs source cross-check**) |
| Tool system-prompt contributions | `enumerated` (§15.1) |
| SDK embedding surface | `enumerated` via examples (§18) · API `schema-needed` |
| Install / update | `enumerated` (files) · `semantics-needed` |
| Examples | `enumerated` (§18) — 13 SDK + ~80 extension |
| Session workflows | named via commands · `semantics-needed` |

**No axis is closed for parity purposes while any of its rows is `semantics-needed`,
`schema-needed` or `source-gap`.** Names alone cannot build a parity row.

---

## 1. Packages — `Kind: engineering-evidence`

| Feature ID | Path |
| --- | --- |
| `pkg.agent` | `packages/agent` |
| `pkg.ai` | `packages/ai` |
| `pkg.client` | `packages/client` |
| `pkg.coding-agent` | `packages/coding-agent` |
| `pkg.evals` | `packages/evals` |
| `pkg.protocol` | `packages/protocol` |
| `pkg.server` | `packages/server` |
| `pkg.session-backends` | `packages/session-backends` |
| `pkg.telemetry` | `packages/telemetry` |
| `pkg.tui` | `packages/tui` |

Scale: 1123 `.ts` files under `packages/` at baseline.

## 2. CLI modes — `Kind: mode`

| Feature ID | Semantics | Pi evidence | Docs |
| --- | --- | --- | --- |
| `coding-agent.mode.interactive` | Full TUI session | `packages/coding-agent/src/modes/interactive/` | `docs/tui.md` |
| `coding-agent.mode.print` | One-shot, non-interactive output | `src/modes/print-mode.ts` | `docs/usage.md` |
| `coding-agent.mode.json-event` | Emits the event stream as JSON lines | `src/modes/json-event.ts` | `docs/json.md` |
| `coding-agent.mode.rpc` | Long-lived JSON command/event server | `src/modes/rpc/` | `docs/rpc.md` |

## 3. CLI flags — `Kind: flag`, `coding-agent.flag.*`

All from `packages/coding-agent/src/cli/args.ts`. Coverage `enumerated`; per-flag semantics
`semantics-needed` except where the name is self-evident.

`help`(77,`-h`) · `version`(79,`-v`) · `mode`(81) · `continue`(86,`-c`) · `resume`(88,`-r`) ·
`provider`(90) · `model`(92) · `api-key`(94) · `system-prompt`(96) · `append-system-prompt`(98) ·
`name`(101,`-n`) · `no-session`(107) · `session`(109) · `session-id`(111) · `fork`(113) ·
`session-dir`(115) · `models`(117) · `no-tools`(119,`-nt`) · `no-builtin-tools`(121,`-nbt`) ·
`tools`(123,`-t`) · `exclude-tools`(128,`-xt`) · `thinking`(133) · `print`(143,`-p`) · `export`(150) ·
`extension`(152,`-e`) · `no-extensions`(155,`-ne`) · `skill`(157) · `prompt-template`(160) ·
`theme`(163) · `use-theme`(166) · `no-skills`(174,`-ns`) · `no-prompt-templates`(176,`-np`) ·
`no-themes`(178) · `no-context-files`(180,`-nc`) · `list-models`(182) · `tui-mode`(189) ·
`verbose`(203) · `approve`(205,`-a`) · `no-approve`(207,`-na`) · `offline`(209)

**40 flags.** `--tui-mode` constrained to `regular|fullscreen` (195).

## 4. Slash commands — `Kind: command`, `coding-agent.slash.*`

`packages/coding-agent/src/core/slash-commands.ts:20-41`. Semantics are the file's own descriptions.

| ID | Semantics | Line |
| --- | --- | --- |
| `settings` | Open settings menu | 20 |
| `model` | Select model (selector UI); arg `<provider/model>` | 21 |
| `scoped-models` | Enable/disable models for Ctrl+P cycling | 22 |
| `export` | Export session (HTML default, or `.html`/`.jsonl` path) | 23 |
| `import` | Import and resume a session from JSONL | 24 |
| `share` | Share session as a secret GitHub gist | 25 |
| `copy` | Copy last agent message to clipboard | 26 |
| `name` | Set session display name | 27 |
| `session` | Show session info and stats | 28 |
| `changelog` | Show changelog entries | 29 |
| `hotkeys` | Show all keyboard shortcuts | 30 |
| `fork` | New fork from a previous user message | 31 |
| `clone` | Duplicate current session at current position | 32 |
| `tree` | Navigate session tree (switch branches) | 33 |
| `trust` | Persist project trust decision | 34 |
| `login` | Configure provider auth; arg `<provider>` | 35 |
| `logout` | Remove provider auth | 36 |
| `new` | Start a new session | 37 |
| `compact` | Manually compact session context | 38 |
| `resume` | Resume a different session | 39 |
| `reload` | Reload keybindings, extensions, skills, prompts, themes, context files | 40 |
| `quit` | Quit | 41 |

## 5. Wire protocol (CBOR) — `wire.protocol.*`

`packages/protocol/src/schemas.ts`. `PROTOCOL_VERSION = 1` (:3). Framing `src/framing.ts`; codec
`src/codec.ts`; CBOR encoder/decoder `src/cbor/`.

**Commands (9)** `Kind: command` — `list`(291) · `create`(293) · `attach`(299) · `detach`(300) ·
`prompt`(301) · `steer`(302) · `abort`(303) · `set_model`(305) · `set_thinking`(310)

**Frames** `Kind: data-format` — `hello`(386) · `request`(392) · `response`(424,430) ·
`event`(437) · `hello_error`(419)

**Server events** `Kind: event` — `server_snapshot`(401) · `session_snapshot`(402) ·
`session_progress`(404) · `session_removed`(408)

**Error codes (7)** — `version` · `busy` · `session_locked` · `not_found` · `invalid_request` ·
`not_implemented` · `internal_error` (270–276)

**Session phases (5)** — `idle` · `turn` · `compaction` · `branch_summary` · `retry` (39–43)

**Thinking levels (7)** — `off` · `minimal` · `low` · `medium` · `high` · `xhigh` · `max` (27–33)

**Stream events** — `item_started`(206) · `assistant_delta`(210) · `item_updated`(217) ·
`item_finished`(221)

**Content kinds** — `text`(76) · `thinking`(80) · `image`(85) · `toolCall`(90)

**Stop reasons** — `stop`/`length`/`toolUse`(142) · `error`(147) · `aborted`(153)

## 6. coding-agent RPC mode — `coding-agent.rpc.*`

`packages/coding-agent/docs/rpc.md`. **A DIFFERENT SURFACE from §5 — never merge the two.**
Per-command request/response payloads are `schema-needed`.

**Commands — 32, RE-DERIVED FROM SOURCE.** Authority is the `RpcCommand` union,
`packages/coding-agent/src/modes/rpc/rpc-types.ts:22-73` (responses: `:117-210`). The docs-derived
count of 32 **matched** the source union — recorded because a confirmation is evidence too, and this
number had been carrying the same proxy risk that made the event and provider counts wrong.

`Kind: command` — prompting: `prompt`(43) · `steer`(80) · `follow_up`(102) ·
`abort`(124) · `new_session`(137) | state: `get_state`(162) · `get_messages`(195) | model:
`set_model`(217) · `cycle_model`(235) · `get_available_models`(259) | thinking:
`set_thinking_level`(281) · `cycle_thinking_level`(298) · `get_available_thinking_levels`(316) |
queue: `set_steering_mode`(338) · `set_follow_up_mode`(355) | compaction: `compact`(374) ·
`set_auto_compaction`(413) | retry: `set_auto_retry`(428) · `abort_retry`(441) | bash: `bash`(456) ·
`abort_bash`(516) | session: `get_session_stats`(531) · `export_html`(574) · `switch_session`(597) ·
`fork`(615) · `clone`(643) · `get_fork_messages`(671) · `get_entries`(694) · `get_tree`(724) ·
`get_last_assistant_text`(752) · `set_session_name`(772) | commands: `get_commands`(793)

**UI-dialog requests — 9** `Kind: ui-api`, server→client.

Source of truth is `RpcExtensionUIRequest`, `packages/coding-agent/src/modes/rpc/rpc-types.ts:238-273`
(4 dialogs + 5 fire-and-forget), matching `docs/rpc.md:1161-1162`.

`select`(1182) · `confirm`(1199) · `input`(1216) · `editor`(1232) · `notify`(1248) ·
`setStatus`(1264) · `setWidget`(1280) · `setTitle`(1297) · `set_editor_text`(1310)

> Tranche 1 said "10". That was a miscount against a 9-item list; corrected here from the source
> type rather than from the doc headings.

**Events — 24 by SOURCE UNION, not the 21 the docs table lists** `Kind: event`

Counting from `docs/rpc.md:838+` gives 21 and is **wrong**. The emitted set is the union of three
sources, and RPC session subscription forwards session events straight to stdout via
`output(toJsonEvent(event))` — so anything in `AgentSessionEvent` reaches an RPC client.

| Layer | Evidence | Unique types |
| --- | --- | --- |
| `AgentEvent` | `packages/agent/src/types.ts:428-443` | 10 |
| `AgentSessionEvent` adds | `packages/coding-agent/src/core/agent-session.ts:141-185` | +13 |
| RPC adds | `extension_error` | +1 |
| **Total** | | **24** |

From `AgentEvent` (10): `agent_start` · `agent_end` · `turn_start` · `turn_end` · `message_start` ·
`message_update` · `message_end` · `tool_execution_start` · `tool_execution_update` ·
`tool_execution_end`

Added by `AgentSessionEvent` (13): `agent_settled` · `queue_update` · `compaction_start` ·
`compaction_end` · **`entry_appended`** · **`session_info_changed`** · **`thinking_level_changed`** ·
`auto_retry_start` · `auto_retry_end` · `summarization_retry_scheduled` ·
`summarization_retry_attempt_start` · `summarization_retry_finished` · `bash_execution_update`

Added by RPC: `extension_error`

**`source-only` / docs gap — three events are emitted but undocumented:**

| Feature ID | Evidence | Coverage state |
| --- | --- | --- |
| `coding-agent.rpc.event.entry_appended` | `agent-session.ts:~152`; docs `none` | `source-only` |
| `coding-agent.rpc.event.session_info_changed` | `agent-session.ts:~153`; docs `none` | `source-only` |
| `coding-agent.rpc.event.thinking_level_changed` | `agent-session.ts:~154`; docs `none` | `source-only` |

> **Method correction this forced.** Tranche 1 enumerated RPC events by grepping the docs table.
> That is docs-derived, not source-derived, and it silently dropped three real events. Event and
> command enumeration must take the **source union first** and use docs only as a cross-check in
> both directions. My own count of that same docs table was also off by one (21 rows, reported 22).

> **Independent observation while verifying:** `auto_retry_end` is declared **twice** in the
> `AgentSessionEvent` union (once after `auto_retry_start`, once after
> `summarization_retry_finished`), with identical shape. Harmless in TypeScript (unions dedupe) but
> it is a real duplication in Pi's source, and it is why a naive line-count of the union
> over-reports by one.

> **`agent_end` and `agent_settled` are separate features and must not be merged.** `agent_end` is
> one low-level run finishing; `agent_settled` means no automatic retry, compaction retry **or**
> queued continuation remains. Only the latter is safe for a client to wait on.

## 7. Models, providers, auth

### 7.1 Providers — `Kind: provider`

**The authoritative source is the registry, not the file listing.** Tranche 2 said "44 providers";
that was a count of `.ts` filenames after an ad-hoc filter, which silently merged four different
meanings. Split properly:

| Meaning | Count | Evidence |
| --- | --- | --- |
| **Built-in text providers** | **40** | `providers/all.ts`, `builtinProviders()` |
| **Built-in image providers** | **1** (`openrouter-images`) | `providers/all.ts`, `builtinImagesProviders()` |
| **Sanctioned fake provider** | **1** (`faux`) | `providers/faux.ts`; **deliberately NOT in `all.ts`** |
| **Total provider feature IDs** | **42** | |
| Supporting modules (NOT providers) | 3 | `cloudflare-auth.ts` · `cloudflare-stream.ts` · `radius-config.ts` |
| Generated model wrappers | per provider | `<p>.models.ts` — see §7.2, counted as generated surface |

**`ai.provider.builtin.*` — the 40 registered text providers** (`all.ts`, in registry order):

`amazon-bedrock` · `ant-ling` · `anthropic` · `azure-openai-responses` · `baseten` · `cerebras` ·
`cloudflare-ai-gateway` · `cloudflare-workers-ai` · `deepseek` · `fireworks` · `github-copilot` ·
`google` · `google-vertex` · `groq` · `huggingface` · `kimi-coding` · `minimax` · `minimax-cn` ·
`mistral` · `moonshotai` · `moonshotai-cn` · `nvidia` · `openai` · `openai-codex` · `opencode` ·
`opencode-go` · `openrouter` · `qwen-token-plan` · `qwen-token-plan-cn` ·
`qwen-token-plan-individual` · `radius` · `together` · `vercel-ai-gateway` · `xai` · `xiaomi` ·
`xiaomi-token-plan-ams` · `xiaomi-token-plan-cn` · `xiaomi-token-plan-sgp` · `zai` · `zai-coding-cn`

**`ai.provider.images.openrouter-images`** — the only registered image provider.

**`ai.provider.faux`** — Pi's own fake provider, present in source but not registered as built-in.
Directly relevant to pi-go: Pi already sanctions a deterministic fake, so pi-go's fixture model has
an upstream counterpart rather than being a test-only invention.

### 7.2 Model catalogue — **`source-gap`, and this one matters**

| Layer | Feature ID | Evidence |
| --- | --- | --- |
| generator | `ai.models.generator` | `packages/ai/scripts/generate-models.ts` |
| generator input | `ai.models.data-manifest` | `packages/ai/scripts/model-data.ts`, `check-model-data.ts` |
| generated aggregate | `ai.models.generated-index` | `packages/ai/src/models.generated.ts:1-2` ("auto-generated … do not edit") |
| generated per provider | `ai.models.catalog.<provider>` | `packages/ai/src/providers/<p>.models.ts` |
| **catalogue data** | `ai.models.catalog-data` | **ABSENT from the tree** |

`packages/ai/src/providers/<p>.models.ts` imports `./data/<p>.json`, but
**`packages/ai/src/providers/data/` is gitignored** (`.gitignore:11`) and is not present at
`086c32e`.

**Consequence for parity:** the number and identity of models Pi supports **cannot be evidenced from
the approved baseline**. Any "Pi supports N models" claim needs a separate, dated evidence source
(a generator run, or an upstream catalogue snapshot) and must be pinned independently of `086c32e`.
Counting the 44 provider files and calling models inventoried would be exactly the false-completion
this gate exists to prevent.

Image models are committed and countable: `packages/ai/src/image-models.generated.ts` — **45**
entries. Registry: `packages/ai/src/images-api-registry.ts`.

### 7.3 Auth — `Kind: auth`, `ai.auth.*`

`context` · `credential-store` · `helpers` · `resolve` (`packages/ai/src/auth/`)
OAuth flows (`packages/ai/src/auth/oauth/`): `anthropic` · `device-code` · `github-copilot` ·
`kimi-coding` · `load` · `oauth-page` · `openai-codex` · `openrouter` · `pkce` · `radius` · `xai`

Flow semantics `semantics-needed`. Docs: `docs/custom-provider.md`, `docs/models.md`.

## 8. Built-in tools — TWO SETS

### 8.1 `coding-agent.tool.*` — `packages/coding-agent/src/core/tools/`

| ID | `name:` line |
| --- | --- |
| `coding-agent.tool.bash` | `bash.ts:331` |
| `coding-agent.tool.edit` | `edit.ts:304` |
| `coding-agent.tool.find` | `find.ts:129` |
| `coding-agent.tool.grep` | `grep.ts:134` |
| `coding-agent.tool.ls` | `ls.ts:106` |
| `coding-agent.tool.read` | `read.ts:216` |
| `coding-agent.tool.write` | `write.ts:193` |

Supporting (not model-facing): `edit-diff` · `file-mutation-queue` · `output-accumulator` ·
`path-utils` · `render-utils` · `tool-definition-wrapper` · `truncate`

### 8.2 `agent-harness.tool.*` — `packages/agent/src/harness/tools/`

`bash` · `edit` · `edit-diff` · `image` · `read` · `write`
(support: `file-mutation-queue`, `path-utils`, `tool-context`)

> **Has `image`; lacks `find`/`grep`/`ls`.** Two distinct product surfaces, two sets of parity rows.
> Tool input schemas are `schema-needed` for both sets.

## 9. Extension surface — `extension.*`

`packages/coding-agent/docs/extensions.md`. Names `enumerated`; behaviour `semantics-needed`.

**Hooks** `Kind: hook` — startup: `project_trust`(352) | resources: `resources_discover`(371) |
session: `session_start`(392) · `session_info_changed`(404) · `session_before_switch`(415) ·
`session_before_fork`(434) · `session_before_compact`/`session_compact`(451) ·
`session_before_tree`/`session_tree`(484) · `session_shutdown`(507) | agent:
`before_agent_start`(521) · `agent_start`/`agent_end`/`agent_settled`(558) ·
`turn_start`/`turn_end`(574) · `message_start`/`message_update`/`message_end`(588) ·
`tool_execution_start`/`update`/`end`(624) · `context`(648) · `before_provider_headers`(660) ·
`before_provider_request`(678) · `after_provider_response`(695) | model: `model_select`(713) ·
`thinking_level_select`(734) | tool: `tool_call`(751) · `tool_result`(815) | bash: `user_bash`(852) |
input: `input`(884)

**`ExtensionContext`** `Kind: public-api` — `ui`(937) · `mode`(941) · `hasUI`(945) · `cwd`(949) ·
`isProjectTrusted()`(967) · `sessionManager`(973) · `modelRegistry`/`model`/`thinkingLevel`/
`scopedModels`(986) · `signal`(992) · `isIdle()`/`abort()`/`hasPendingMessages()`(1017) ·
`shutdown()`(1021) · `getContextUsage()`(1039) · `compact()`(1050) · `getSystemPrompt()`(1066)

**`ExtensionCommandContext`** — `getSystemPromptOptions()`(1086) · `waitForIdle()`(1099) ·
`newSession()`(1112) · `fork()`(1145) · `navigateTree()`(1171) · `switchSession()`(1190) ·
`reload()`(1276)

**`ExtensionAPI`** — `on`(1334) · `registerTool`(1338) · `sendMessage`(1389) ·
`sendUserMessage`(1412) · `appendEntry`(1444) · `setSessionName`(1462) · `getSessionName`(1470) ·
`setLabel`(1481) · `registerCommand`(1498) · `getCommands`(1533) · `registerMessageRenderer`(1566) ·
`registerMarkdownTransformer`(1570) · `registerEntryRenderer`(1591)

## 10. Resources — `coding-agent.resource.*`

| ID | Module | Docs |
| --- | --- | --- |
| `resource.skills` | `src/core/skills.ts` | `docs/skills.md` |
| `resource.prompt-templates` | `src/core/prompt-templates.ts` | none found |
| `resource.themes` | (loader) | `docs/themes.md` |
| `resource.keybindings` | `src/core/keybindings.ts` | `docs/keybindings.md` |
| `resource.loader` | `src/core/resource-loader.ts` | — |
| `resource.system-prompt` | `src/core/system-prompt.ts` | — |
| `resource.context-files` | via `--no-context-files`(180) | — |

Discovery is also extension-visible via `resources_discover`(371). Semantics `semantics-needed`.

## 11. TUI — `tui.component.*` — `packages/tui/src/`

Components: `alt-screen-flash` · `box` · `cancellable-loader` · `editor` · `h-stack` · `image` ·
`input` · `loader` · `markdown` · `scroll-view` · `select-list` · `settings-list` · `spacer` ·
`stack` · `text` · `truncated-text` · `v-stack`

Subsystems: `alt-screen-search` · `autocomplete` · `editor-component` · `fuzzy` · `keybindings` ·
`keys` · `kill-ring` · `latex` · `layout`/`layout-node` · `native-modifiers` · `stdin-buffer` ·
`terminal` · `terminal-colors` · `terminal-image` · `tui-alt-screen` · `tui-main-screen` · `tui` ·
`undo-stack` · `word-navigation`

Docs: `docs/tui.md`, `docs/themes.md`, `docs/keybindings.md`, `docs/terminal-setup.md`,
`docs/tmux.md`, `docs/termux.md`, `docs/windows.md`. Semantics `semantics-needed`.

## 12. server / client / session-backends

**`server.*`** — `connection` · `errors` · `listener` · `protocol` · `server` · `sessions` ·
`snapshots` · `types` · transports `unix/{index,listener,preset,types}` · testing harness
`testing/{client,server,service}`

**`client.*`** — `client` (`PiClient`) · `connection` · `errors` · `promise` · `session-handle`
(`PiSessionHandle`, `SessionLease`, `SessionLeaseMode`, `AcquireSessionOptions`) · `state` ·
`transport` (`ByteTransport`, `ByteTransportFactory`, `ByteTransportHandlers`) · `types` · `unix`

**`session-backends.*`** — only `sqlite-node` exists: `sqlite/{branch-cache,index,migrations,repo,
search-backend,sql}`, storage `{branch-entries,branch-tips,entries,facts,lanes,records,
session-sequences,session-stats}`, migration `001_initial.sql`

> Session **persistence schema** is a first-class parity surface (`data-format`), evidenced by
> `001_initial.sql` and `docs/session-format.md` — `schema-needed`.

## 13. Telemetry / evals / install

**`telemetry.*`** — `index` · `memory` · `noop` · conformance harness `testing/{conformance,types}`.
Schema doc: `packages/agent/docs/telemetry-schema.md`. `schema-needed`.

**`evals.*`** — `extensions.eval` · `smoke.eval` · `pi-harness` · vitest integration
`vitest-evals/{artifacts,harness-table,reporter,setup,summary}`. `semantics-needed`.

**`install.*`** — `packages/coding-agent/install-lock/{package.json,package-lock.json}` ·
generator `scripts/generate-coding-agent-install-lock.mjs` · `src/utils/windows-self-update.ts` ·
test `test/git-update.test.ts`. `semantics-needed`.

## 14. Documentation set — `Kind: engineering-evidence`

`packages/coding-agent/docs/` (36 files): `compaction` · `containerization` · `custom-provider` ·
`development` · `environment-variables` · `extensions` · `index` · `json` · `keybindings` ·
`llama-cpp` · `models` · `packages` · `quickstart` · `rpc` · `sdk` · `security` · `session-format` ·
`sessions` · `settings` · `shell-aliases` · `skills` · `terminal-setup` · `termux` · `themes` ·
`tmux` · `tui` · `usage` · `windows`

`packages/agent/docs/`: `harness` · `search` · `telemetry-schema`

Each is a candidate parity area; none may be closed without its own row.

---


## 15. Tool input schemas — `schema-needed` closed for the coding-agent set

Authority is each tool's `<name>Schema` declaration, not the description text.
Path prefix `packages/coding-agent/src/core/tools/`.

| Tool | Schema line | Required | Optional |
| --- | --- | --- | --- |
| `bash` | `bash.ts:41` | `command` | `timeout` (seconds; **no default timeout**) |
| `edit` | `edit.ts:45` | `path`, `edits[]` | — |
| `find` | `find.ts:29` | `pattern` (glob) | `path`, `limit` (default 1000) |
| `grep` | `grep.ts:24` | `pattern` | `path`, `glob`, `ignoreCase`, `literal`, `context`, `limit` (default 100) |
| `ls` | `ls.ts:14` | — | `path`, `limit` (default 500) |
| `read` | `read.ts:21` | `path` | `offset` (1-indexed), `limit` |
| `write` | `write.ts:15` | `path`, `content` | — |

`edit`'s `edits[]` carries a documented constraint that is behavioural, not cosmetic: **each edit is
matched against the ORIGINAL file, not incrementally**, and overlapping or nested edits are
forbidden (`edit.ts:48-51`). A Go port that applies edits sequentially to a mutating buffer would
pass naive tests and silently diverge here.

`read` truncates to a max line count **or** byte budget, whichever hits first, and instructs
continuation via `offset` (`read.ts:218`).

### 15.1 Tools contribute to the system prompt — a surface not previously recorded

Every tool exports a `<name>ToolSystemPromptContribution` with a `snippet` and `guidelines`, which
are assembled into the system prompt (`parameters`/`promptSnippet`/`promptGuidelines` fields on the
definition, e.g. `read.ts:218-220`).

| Tool | Snippet | Guidelines |
| --- | --- | --- |
| `bash` | "Execute bash commands (ls, grep, find, etc.)" | inspect `PI_*` env vars for model/session details |
| `read` | "Read file contents" | use `read` instead of `cat` or `sed` |
| `write` | "Create or overwrite files" | only for new files or complete rewrites |
| `edit` | "Make precise file edits with exact text replacement, including multiple disjoint edits in one call" | — |
| `find` | "Find files by glob pattern (respects .gitignore)" | — |
| `ls` | "List directory contents" | — |
| `grep` | (see `grep.ts`) | — |

**This is a parity surface in its own right:** the prompt a model sees is composed from the
registered tool set, so tool selection changes the prompt. Any port that hard-codes a system prompt
loses this coupling.

## 16. Settings — `coding-agent.setting.*`

Authority: `packages/coding-agent/src/core/settings-manager.ts` (the settings type, :13-130).
Grouped sub-objects: `CompactionSettings`(13) · `BranchSummarySettings`(19) ·
`ProviderRetrySettings`(24) · `RetrySettings`(30) · `TerminalSettings`(40) · `ImageSettings`(47) ·
`ThinkingBudgetsSettings`(52) · markdown/rendering(61) · `anthropicExtraUsage`(66) ·
`PackageSource`(82-87).

Top-level keys (:91-130): `lastChangelogVersion` · `defaultProvider` · `defaultModel` ·
`defaultThinkingLevel` · `transport` · `steeringMode` · `followUpMode` · `theme` · `compaction` ·
`branchSummary` · `retry` · `hideThinkingBlock` · `showCacheMissNotices` · `externalEditor` ·
`shellPath` · `quietStartup` · `defaultProjectTrust` · `shellCommandPrefix` · `npmCommand` ·
`collapseChangelog` · `enableInstallTelemetry` · `enableAnalytics` · `trackingId` · `packages` ·
`extensions` · `skills` · `prompts` · `themes` · `enableSkillCommands` · `terminal` · `images` ·
`enabledModels` · `defaultTools` · `doubleEscapeAction` · `treeFilterMode` · `thinkingBudgets` ·
`editorPaddingX` · `outputPad` · `autocompleteMaxVisible` · `showHardwareCursor`

Notable defaults that are behaviour, not preference: `steeringMode`/`followUpMode` ∈
`all | one-at-a-time`; `doubleEscapeAction` default `tree`; `treeFilterMode` ∈
`default|no-tools|user-only|labeled-only|all`; `enableAnalytics` default **false** (opt-in) while
`enableInstallTelemetry` default **true**.

## 17. Environment variables — `coding-agent.env.*`

Authority: `packages/coding-agent/docs/environment-variables.md` (**docs-derived — needs a source
cross-check before being treated as closed**, per the counting discipline above).

**Exported into tool/bash context** (:26-30): `PI_SESSION_ID` · `PI_SESSION_FILE` (unset for
ephemeral sessions) · `PI_PROVIDER` · `PI_MODEL` · `PI_REASONING_LEVEL`

**Read as configuration** (:81-92): `PI_CODING_AGENT_DIR` · `PI_CODING_AGENT_SESSION_DIR` ·
`PI_PACKAGE_DIR` · `PI_OFFLINE` · `PI_SKIP_VERSION_CHECK` · `PI_TELEMETRY` · `PI_CACHE_RETENTION` ·
`PI_SHARE_VIEWER_URL` · `PI_HARDWARE_CURSOR` · `PI_TUI_ESC_TIMEOUT` · `VISUAL`/`EDITOR` ·
`HTTP_PROXY`/`HTTPS_PROXY`

## 18. Examples — `Kind: engineering-evidence`

`packages/coding-agent/examples/`. These are executable demonstrations of the extension and SDK
surfaces, so they double as a **capability checklist**: anything demonstrated here is something a
user can do today.

**SDK examples (13)** — `01-minimal` · `02-custom-model` · `03-custom-prompt` · `04-skills` ·
`05-tools` · `06-extensions` · `07-context-files` · `08-prompt-templates` ·
`09-api-keys-and-oauth` · `10-settings` · `11-sessions` · `12-full-control` · `13-session-runtime`

> The SDK is an embedding surface, i.e. a public API for building on Pi. It is a parity axis in its
> own right and is **not** covered by the CLI or RPC rows.

**Extension examples (~80 files)** covering, among others: auto-commit-on-exit · bash-spawn-hook ·
bookmark · border-status-editor · built-in-tool-renderer · claude-rules · commands ·
confirm-destructive · custom-compaction · custom-footer/header · dirty-repo-guard · doom (WASM
component) · dynamic-tools · entry-renderer · event-bus · file-trigger · git-checkpoint ·
git-merge-and-resolve · github-issue-autocomplete · handoff · hidden-thinking-label · inline-bash ·
input-transform(-streaming) · interactive-shell · kimi-deferred-tools · mac-system-theme ·
message-renderer · minimal-mode · modal-editor · model-status · notify · overlay-qa-tests ·
permission-gate · pirate · preset · project-trust · prompt-customizer · protected-paths ·
provider-payload · qna · question(naire) · rainbow-editor · reload-runtime · rpc-demo ·
send-user-message · session-name · shutdown-command · snake · space-invaders · ssh · status-line ·
structured-output · subagents (`agents.ts` + planner/reviewer/scout/worker prompts) · summarize ·
system-prompt-header · tic-tac-toe · timed-confirm · titlebar-spinner · todo · tool-override ·
tools · trigger-compact · truncated-tool · widget-placement · working-indicator

Plus `examples/rpc-extension-ui.ts` at the top level.

> **Two capabilities visible only here**, not in the hook list: a **subagent** pattern
> (`agents.ts` with separate planner/reviewer/scout/worker prompt files) and **WASM components**
> running inside the TUI (the doom example). Both are worth explicit parity rows or explicit
> exclusions.


## 19. Hook semantics — first batch, and one finding that contradicts a pi-go design position

### 19.1 ⚠️ `extension.hook.tool-call` — Pi DOES allow extensions to rewrite tool arguments

| | |
| --- | --- |
| Feature ID | `extension.hook.tool-call` |
| Kind | `hook` |
| Semantics | Fires after `tool_execution_start` and **before** the tool executes. **Can block.** `event.input` is **mutable, and mutating it in place changes the arguments the tool actually runs with.** |
| Pi evidence | `coding-agent/src/core/agent-session.ts:480-499` (`beforeToolCall` passes `input: args` by reference); `core/extensions/types.ts:901`, `:1072` |
| Docs evidence | `docs/extensions.md:751-790` |
| Coverage state | `enumerated` |

Stated guarantees (`docs/extensions.md:761-766`):
- mutations to `event.input` affect the actual execution;
- later handlers see earlier handlers' mutations;
- **no re-validation is performed after mutation**;
- blocking is `{ block: true, reason?, terminate? }`;
- `terminate` applies only to a blocked call, and the agent stops early only when **every** finalized
  result in the batch is terminating.

Ordering guarantees worth porting exactly:
- before `tool_call` runs, Pi **drains previously emitted agent events** so session state is current
  through the assistant's tool-calling message;
- in parallel mode, sibling calls from one assistant message are **preflighted sequentially, then
  executed concurrently** — so a `tool_call` handler is *not* guaranteed to see sibling results.

> **Why this is flagged rather than filed quietly.** pi-go's runtime currently offers a
> policy-and-denial check *only*, and its own source comment states that argument rewriting is
> deliberately not offered. That position was taken on the belief that argument rewriting was
> borrowed from another project rather than being a Pi capability. **It is a Pi capability**, in the
> source and in the docs.
>
> For a complete Go replica this is therefore either a **gap to implement** or an **explicit accepted
> deviation with a written reason** — not something to leave as an unstated design preference. This
> is exactly the class of omission the inventory-first decision was meant to catch, and it was found
> by enumerating Pi rather than by reviewing pi-go.

### 19.2 `extension.hook.tool-result`

| | |
| --- | --- |
| Semantics | Fires after execution with `toolName`, `toolCallId`, the `input` used, plus `content` and `details` of the result |
| Pi evidence | `agent-session.ts:501-512` (`afterToolCall`) |
| Docs evidence | `docs/extensions.md:815` |

### 19.3 Remaining hooks

Still `semantics-needed`: the session group, the agent group (including `context`,
`before_provider_headers`, `before_provider_request`, `after_provider_response`), `model_select`,
`thinking_level_select`, `user_bash`, `input`, `project_trust`, `resources_discover`.

The provider hooks matter most for pi-go next, because they sit exactly on its model boundary.

## Counting discipline — learned the hard way

Three counts in this document were wrong before review caught them, and **all three failed the same
way: counting a convenient proxy instead of the authoritative source.**

| Claim | Wrong | Right | Proxy I counted | Authority |
| --- | --- | --- | --- | --- |
| RPC events | 21/22 | **24** | the docs table | source union of `AgentEvent` + `AgentSessionEvent` + RPC |
| RPC UI requests | 10 | **9** | doc headings | the `RpcExtensionUIRequest` type |
| Providers | 44 | **42** | `.ts` filenames | `builtinProviders()` / `builtinImagesProviders()` |

**Rules this imposes on the rest of the census:**

1. **Enumerate from the source union first.** Docs are a two-way cross-check, never the source.
   Docs-derived enumeration cannot find a source-only feature *by construction* — which is exactly
   the omission class this gate exists to catch.
2. **A file listing is not a registry.** If the product has a registration point (`all.ts`,
   a union type, a command table), that is the count. File names are a proxy that silently merges
   implementations, helpers, generated wrappers and test doubles.
3. **State which meaning a number has.** "44 providers" merged four different things. Every count
   here now names its authority.
4. **A count that has not been re-derived from its authority is `semantics-needed`, not a fact.**

## Method and limits

- Extraction used `git show 086c32e:<path>` / `git ls-tree 086c32e` exclusively.
- Counts are given only where enumeration is mechanical. Where "feature" would require judgement,
  the list is given without a count.
- Generated surfaces record generator, generated artefact and data separately (§7.2).

**Not yet started:** `packages/coding-agent/examples/` (extension and SDK examples), per-command RPC
payload schemas, per-tool input schemas, per-hook semantics, settings/environment-variable keys.
