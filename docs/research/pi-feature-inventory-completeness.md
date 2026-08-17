# Pi feature-inventory completeness at `086c32e`

## Conclusion

A complete Pi inventory cannot be produced by expanding the current package-level parity rows by
hand. It needs a repeatable source-candidate extractor over the pinned Git tree, followed by an
explicit disposition for every candidate. The governing equation should be:

```text
all extracted candidates at the pinned tree
= inventory rows
+ reviewed exclusions with a reason and owner
```

The extractor must cover public APIs, discriminated unions, runtime registries, documentation,
examples, dogfooding resources, generated catalogues, packaged assets, migrations, tests, evals,
and release/install machinery. Pi's root README is not an adequate denominator: it lists five
packages, while the repository contains additional protocol, client, server, session-backend, and
evaluation surfaces under `packages/` ([root README, lines 26-35](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/README.md#L26-L35),
[workspace declaration, lines 5-12](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/package.json#L5-L12),
[`packages/` tree](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages)).

This report defines the authoritative scan surfaces, stable feature identifiers, mechanically
checkable completeness invariants, and known omission traps. It does not attempt to assign release
targets or implementation status to every feature.

## Pinned baseline and evidence rule

- Source baseline: [`earendil-works/pi@086c32e74530564922d011ade23ff582c9d63116`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116).
- All extraction must read Git objects at that commit (`git ls-tree`, `git show
  <sha>:<path>`), not the mutable working tree. This matters because generated catalogues, package
  locks, examples, and docs are part of the pinned product evidence, even if a local checkout has
  unrelated edits.
- A source anchor is stable when it contains the commit, path, and a semantic selector such as an
  exported symbol, object key, discriminant literal, JSON pointer, Markdown heading, or test name.
  Line numbers are useful review links but must not be the identity: the same source file already
  defines many independent contracts, such as every RPC command in one union
  ([`rpc-types.ts`, lines 20-73](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/modes/rpc/rpc-types.ts#L20-L73)).
- Directory evidence is authoritative for tracked-file enumeration. File/line evidence is
  authoritative for individual semantics. A README or changelog can propose a candidate but cannot
  erase a source/API candidate that it fails to mention.

## Authoritative source surfaces that must be scanned

### 1. Repository topology, packaging, release, and policy

| Surface | Mandatory inputs | What the scanner must derive |
| --- | --- | --- |
| Workspace topology | [`package.json`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/package.json), every tracked `**/package.json`, [`package-lock.json`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/package-lock.json) | Every package, executable, subpath export, published file set, workspace-only package, dependency edge, build/test/generation command, and install-time dependency surface. The root build order names TUI, telemetry, AI, agent, SQLite backend, protocol, client, server, and coding-agent packages ([lines 14-34](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/package.json#L14-L34)). |
| Product and security policy | [`README.md`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/README.md), [`SECURITY.md`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/SECURITY.md), [`CONTRIBUTING.md`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/CONTRIBUTING.md), [`AGENTS.md`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/AGENTS.md) | Explicit product promises, security boundary, compatibility/process constraints, and documented non-features. For example, Pi intentionally runs with the launching process's permissions and delegates stronger isolation to containers/sandboxes ([README, lines 38-46](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/README.md#L38-L46); [security docs, lines 31-37](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/docs/security.md#L31-L37)). |
| Build/release/install | [`scripts/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/scripts), [`.github/workflows/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/.github/workflows), [`packages/coding-agent/install-lock/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/install-lock), `.npmrc`, `.husky/` | Standalone binaries, source archives, npm publication, model-catalog publication, shrinkwrap/install locks, platform artifacts, update paths, audit/signature checks, and smoke tests. The root README makes standalone source archives, offline model data, shrinkwrap, release smoke tests, and `pi update --self` observable distribution commitments ([lines 63-88](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/README.md#L63-L88)). |
| Tracked non-code assets | All images, WASM, native binaries/C, SQL migrations, JSON schemas/themes, HTML/CSS/JS templates, generated JSON data, docs, examples, and lock files returned by `git ls-tree -r` | Each shipped asset and the behavior that consumes it. The coding-agent build explicitly copies themes, images, HTML-export assets, docs, examples, and Photon WASM into distribution artifacts ([`packages/coding-agent/package.json`, lines 27-40](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/package.json#L27-L40)). |

### 2. Runtime and package surfaces

Every top-level package is a mandatory inventory partition. Public exports are feature candidates;
implementation registries and discriminated unions are contract candidates; tests and changelogs
are evidence and regression-discovery surfaces.

| Package/surface | Mandatory directories and files | Required extraction |
| --- | --- | --- |
| Agent runtime and durable harness | [`packages/agent/src/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/agent/src), [`packages/agent/src/index.ts`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/src/index.ts), [`packages/agent/docs/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/agent/docs), [`packages/agent/test/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/agent/test) | Agent events, loop and batch contracts, message conversion, steering/follow-up, hooks, stream defaults, search, tools, compaction, durable session records, recovery, storage ports, telemetry spans, and testing/conformance exports. The root package export includes the loop, harness, compaction, sessions, skills, tools, search, telemetry, and proxy surfaces ([`index.ts`, lines 43-145](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/src/index.ts#L43-L145)). |
| AI/provider layer | [`packages/ai/src/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src), [`packages/ai/scripts/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/ai/scripts), [`packages/ai/test/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/ai/test), [`packages/ai/package.json`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/package.json) | API identifiers, provider factories, model IDs and metadata, dynamic catalogues, message transforms, streaming events, retries, deferred operations, auth methods and stores, OAuth flows, API-key/env resolution, image generation, usage/cost, telemetry, compatibility shims, CLI, and generated-data provenance. The package deliberately exposes provider/API subpaths and OAuth/Bedrock/Bun entry points rather than only one root API ([package exports, lines 13-45](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/package.json#L13-L45)). |
| Coding-agent product | [`packages/coding-agent/src/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src), [`packages/coding-agent/src/index.ts`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/index.ts), [`packages/coding-agent/docs/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/docs), [`packages/coding-agent/test/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/test) | CLI commands/flags/modes, SDK exports, tools, settings, keybindings, sessions, compaction, resource discovery, context files, skills/prompts/themes/packages, extensions, auth/model UI, interactive/print/JSON/RPC modes, HTML export/import/share, project trust, shell behavior, platform support, update/install, and all user-visible TUI workflows. The package exports CLI, SDK, runtime, sessions, resources, extensions, tools, run modes, and UI components ([`src/index.ts`, lines 1-25](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/index.ts#L1-L25), [198-262](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/index.ts#L198-L262), [332-351](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/index.ts#L332-L351)). |
| Generic TUI | [`packages/tui/src/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/tui/src), [`packages/tui/native/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/tui/native), [`packages/tui/test/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/tui/test), [`packages/tui/README.md`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/tui/README.md) | Renderers, differential rendering, synchronized output, viewport/scrolling, overlays/focus, components, editor/input/key semantics, ANSI/width behavior, images, terminal capabilities/colors, LaTeX/Markdown, autocomplete, native modifier/console support, and platform fallbacks. The public export index alone contains components, key handling, images, terminal APIs, overlays, both renderers, and width utilities ([`src/index.ts`, lines 3-61](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/tui/src/index.ts#L3-L61), [63-147](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/tui/src/index.ts#L63-L147)); the README defines user-observable rendering features ([lines 3-16](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/tui/README.md#L3-L16)). |
| Remote protocol | [`packages/protocol/src/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/protocol/src), [`packages/protocol/test/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/protocol/test), [`packages/protocol/README.md`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/protocol/README.md) | Framing, codec, strict schemas, protocol version, transcript/state schemas, every command/result/event/error, hello/envelope rules, and malformed-input behavior. The command and result unions are closed source denominators ([`schemas.ts`, lines 291-376](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/protocol/src/schemas.ts#L291-L376)); so are the hello, response, and event envelopes ([lines 384-450](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/protocol/src/schemas.ts#L384-L450)). |
| Remote client | [`packages/client/src/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/client/src), [`packages/client/test/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/client/test), [`packages/client/README.md`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/client/README.md) | Connection lifecycle, hello/version behavior, correlation, snapshots/events, session control, Unix transport, disconnection/error semantics, and public client exports. The package publishes both root and `./unix` entry points ([`package.json`, lines 8-18](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/client/package.json#L8-L18)). |
| Remote server | [`packages/server/src/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/server/src), [`packages/server/test/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/server/test), [`packages/server/README.md`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/server/README.md) | Registry/session lifecycle, transport abstraction, Unix server, locking/attachment, command handlers, snapshots/revisions, progress projection, testing adapters, and error mapping. The package publishes root, testing, and Unix subpaths ([`package.json`, lines 8-20](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/server/package.json#L8-L20)). |
| Session backends | [`packages/session-backends/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/session-backends), especially [`sqlite-node/src/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/session-backends/sqlite-node/src), migrations and tests | Every backend, storage capability, migration, transaction/sequence rule, branch cache, search, facts, records, stats, writer lease, and conformance result. The SQLite backend's build copies migrations into its distribution and depends on both agent-core and AI ([`package.json`, lines 19-38](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/session-backends/sqlite-node/package.json#L19-L38)). |
| Telemetry | [`packages/telemetry/src/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/telemetry/src), [`packages/telemetry/test/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/telemetry/test), agent telemetry definitions and generated [`telemetry-schema.md`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/docs/telemetry-schema.md) | Generic span semantics, typed schema types, in-memory adapter behavior, testing conformance, every domain span/event/attribute/status/parent rule, cardinality/sensitivity metadata, and generated-doc consistency. The schema document is generated, not hand-authored ([lines 1-7](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/docs/telemetry-schema.md#L1-L7)). |
| Behavioral evaluations | [`packages/evals/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/evals) | Harness options, eval scenarios, judges, repetitions/comparisons, artifact formats, model/provider selection, and which parity claims have model-backed evidence. Evals are behavioral, real-model workflows that attach native session artifacts ([`README.md`, lines 1-5](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/evals/README.md#L1-L5)); they are a private engineering package, not a user package ([`package.json`, lines 1-18](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/evals/package.json#L1-L18)). |

### 3. Documentation, examples, and dogfooding

These are mandatory independent surfaces, not optional supplements to `src/`.

| Surface | Mandatory scan | Why it is authoritative evidence |
| --- | --- | --- |
| Coding-agent documentation | Every tracked Markdown file under [`packages/coding-agent/docs/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/docs), plus `docs.json` and referenced assets | Docs are shipped in the npm/binary package ([`package.json`, lines 27-40](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/package.json#L27-L40)) and contain user-facing surfaces such as environment variables, platform behavior, installation, shell aliases, and security. `docs.json` is not exhaustive: the documentation index separately links environment variables and `llama.cpp` ([`index.md`, lines 39-80](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/docs/index.md#L39-L80)), while the navigation manifest does not list both of them ([`docs.json`, lines 1-145](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/docs/docs.json#L1-L145)). |
| Package READMEs and changelogs | Every `packages/*/{README.md,CHANGELOG.md}` and backend equivalents | READMEs define public behavior and examples; changelogs are a discovery source for compatibility quirks and regressions. They are candidates/evidence, not substitutes for current source. Agent-core's README, for example, specifies parallel preflight/execution/result ordering and per-tool sequential downgrade behavior ([lines 113-124](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/README.md#L113-L124)). |
| Coding-agent examples | Every tracked file under [`packages/coding-agent/examples/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/examples), including nested `package.json`, lockfiles, WASM/C, SDK examples, and RPC demo | Examples exercise capability combinations that a type-only scan will miss: runtime tool registration, safety hooks, custom UI, state persistence, provider registration, RPC UI, OS sandboxes, and resource discovery are all explicitly catalogued ([extension examples, lines 15-137](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/examples/extensions/README.md#L15-L137)); SDK examples cover minimal through full-control and session-runtime embeddings ([SDK examples, lines 7-23](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/examples/sdk/README.md#L7-L23)). Some example extensions are real npm workspaces, so their dependency and loading behavior is part of the example surface ([root `package.json`, lines 8-12](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/package.json#L8-L12)). |
| Repository dogfooding | Every tracked file under [`.pi/`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/.pi) | These are first-party uses of Pi's own extension, prompt, and skill formats. They reveal workflows such as token-speed UI notifications ([`.pi/extensions/tps.ts`, lines 10-46](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/.pi/extensions/tps.ts#L10-L46)), session import/rewrite tooling ([`.pi/extensions/import-repro.ts`, lines 1-16](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/.pi/extensions/import-repro.ts#L1-L16)), and prompt-resource frontmatter/arguments ([`.pi/prompts/wr.md`, lines 1-16](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/.pi/prompts/wr.md#L1-L16)). Dogfood is evidence of a capability and compatibility pressure, but the canonical contract remains the corresponding public API/docs. |
| Tests and repros | Every tracked `test/`, `*.test.*`, eval file, smoke entry, benchmark/repro file, and test fixture | Tests identify observable edge cases, especially ones not advertised in docs. They must map to feature IDs as evidence or to a reviewed internal-only exclusion; test filenames in AI and TUI explicitly cover provider-specific conversion/auth/retry behavior and terminal width/overlay regressions ([AI tests tree](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/ai/test), [TUI tests tree](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/tui/test)). |

## Proposed stable feature IDs

Feature IDs must survive line movement and normal refactoring. They should be human-readable,
namespace-scoped, and derived from stable public names or discriminants. Paths and symbols remain
source anchors, not the ID itself.

| Namespace | ID pattern | Examples |
| --- | --- | --- |
| Packages and distribution | `pkg:<npm-name>`, `dist:<artifact>`, `install:<method>`, `update:<target>` | `pkg:pi-ai`, `dist:standalone-binary`, `install:npm-global`, `update:self` |
| CLI and application modes | `cli:command:<name>`, `cli:flag:<long-name>`, `mode:<name>` | `cli:command:update`, `cli:flag:no-context-files`, `mode:rpc` |
| Slash commands/settings/keys | `slash:<name>`, `setting:<json-path>`, `key:<action-id>` | `slash:compact`, `setting:compaction.reserveTokens`, `key:app.message.followUp` |
| Agent/runtime behavior | `runtime:<contract-name>`, `event:<event-literal>`, `tool:<tool-name>` | `runtime:steering-before-next-model-call`, `event:tool_execution_end`, `tool:grep` |
| Extension system | `ext:event:<literal>`, `ext:ui:<method>`, `ext:api:<method>`, `ext:mode:<literal>` | `ext:event:session_before_compact`, `ext:ui:setWidget`, `ext:api:registerProvider`, `ext:mode:rpc` |
| SDK/public exports | `sdk:<package>:<export-name>`, `subpath:<package>:<export-key>` | `sdk:coding-agent:createAgentSessionRuntime`, `subpath:client:unix` |
| RPC JSONL | `rpc:command:<literal>`, `rpc:response:<command>`, `rpc:ui-request:<method>`, `rpc:ui-response:<variant>` | `rpc:command:follow_up`, `rpc:ui-request:editor` |
| Remote protocol | `wire:command:<literal>`, `wire:result:<literal>`, `wire:event:<literal>`, `wire:error:<literal>`, `wire:schema:<name>` | `wire:command:attach`, `wire:event:session_progress`, `wire:error:session_locked` |
| AI/provider | `ai:api:<id>`, `ai:provider:<id>`, `ai:model:<provider>/<model>`, `ai:auth:<provider>/<method>`, `ai:option:<path>` | `ai:api:anthropic-messages`, `ai:provider:radius`, `ai:auth:openai-codex:oauth`, `ai:option:cacheRetention` |
| Sessions and persistence | `session:jsonl-entry:<type>`, `harness:entry:<type>`, `harness:record:<type>`, `storage:method:<name>`, `backend:<id>:<capability>` | `session:jsonl-entry:compaction`, `harness:record:tool_started`, `backend:sqlite-node:writer-lease` |
| TUI | `tui:component:<export>`, `tui:behavior:<name>`, `tui:native:<platform>/<capability>` | `tui:component:ScrollView`, `tui:behavior:differential-render`, `tui:native:win32:console-mode` |
| Resources | `resource:skill:<discovery-rule>`, `resource:prompt:<format-rule>`, `resource:theme:<schema-rule>`, `resource:package:<capability>` | `resource:prompt:frontmatter-argument-hint`, `resource:package:filtered-autoload` |
| Docs/examples/evidence | `doc:<relative-path>#<heading>`, `example:<relative-path>`, `eval:<suite>/<case>`, `telemetry:span:<name>` | `doc:security.md#project-trust`, `example:extensions/permission-gate.ts`, `telemetry:span:pi.harness.tool` |

Normalization rules:

1. Preserve externally visible spelling, including underscores in protocol literals (`follow_up`)
   and case in registered UI methods (`setWidget`).
2. Use the containing public type or registry to disambiguate repeated literals. `rpc:command:abort`
   and `wire:command:abort` are different features because Pi exposes two different protocols.
3. Use dotted paths for nested settings and schema fields. Array membership becomes its own ID only
   when the member is independently observable.
4. Renames create an alias/migration relation; they do not silently mutate history. Keybinding name
   migrations are explicit source data ([`keybindings.ts`, lines 209-269](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/keybindings.ts#L209-L269)), and session versions/migrations are explicit too
   ([`session-manager.ts`, lines 230-295](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/session-manager.ts#L230-L295)).
5. Do not derive IDs from display titles, descriptions, line numbers, model array indexes, or hashes.
   A hash can detect source drift, but it is not a usable feature identity.

## Minimum inventory-row schema

Each feature row should contain at least:

```yaml
id: ext:ui:setWidget
title: Extension widgets above or below the editor
kind: user-capability
source_anchors:
  - commit: 086c32e74530564922d011ade23ff582c9d63116
    path: packages/coding-agent/src/core/extensions/types.ts
    selector: ExtensionUIContext.setWidget
surfaces: [extension, tui, rpc]
implementation_class: R
target_release: v2
status: inventory-only
owner: null
issue: null
acceptance: []
relations:
  - rpc:ui-request:setWidget
  - example:extensions/widget-placement.ts
deviation: null
```

The important distinction is between `kind` and `status`. Internal code can be classified as
`implementation-detail` or `evidence` without becoming a parity feature, but it still needs a
reviewed disposition. This is how the inventory avoids both silent omission and the opposite error
of treating every source file as a user feature.

## Mechanically checkable completeness invariants

### I1. Immutable source manifest

Generate a manifest from `git ls-tree -r --full-tree <sha>` containing every tracked path, blob ID,
mode, and size. The inventory build must fail when the commit or tree changes without an explicit
re-pin. This captures generated files, native binaries, WASM, SQL, docs, and locks that an extension
filter such as `*.ts` would miss. Pi ships native Darwin and Windows TUI artifacts alongside source
and prebuilds ([`packages/tui/native/` tree](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/tui/native)).

### I2. Every tracked file has a disposition

Every manifest path must be linked to at least one feature/evidence/infrastructure row or appear in
an explicit exclusion table with `reason`, `reviewer`, and `expiry/revisit condition`. There must be
no implicit directory exclusions except Git metadata, because `.pi/`, examples, generated data,
tests, and workflows are all meaningful first-party surfaces.

### I3. Package and export set equality

Parse every tracked `package.json`; expand workspace globs against the pinned tree; then assert:

```text
declared package roots = discovered package roots - explicitly classified example packages
package.json exports/bin/files/scripts candidates = inventory package/distribution candidates
```

For TypeScript entry points, use the TypeScript compiler API to enumerate the effective exported
symbol set, following `export *` and named re-exports. A grep of `export` lines is not sufficient.
The coding-agent root, for example, re-exports extensive extension, runtime, SDK, session, tool, and
mode APIs from other modules ([`src/index.ts`, lines 52-167](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/index.ts#L52-L167), [198-251](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/index.ts#L198-L251)).

### I4. Closed-union and registry set equality

Use AST extraction to compare exact literal/member sets with inventory IDs. At minimum, the gate
must cover:

- CLI modes, long flags, top-level management commands, and thinking levels
  ([`args.ts`, lines 11-60](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/cli/args.ts#L11-L60), [248-308](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/cli/args.ts#L248-L308));
- built-in slash commands ([`slash-commands.ts`, lines 13-42](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/slash-commands.ts#L13-L42));
- settings keys and nested settings ([`settings-manager.ts`, lines 12-140](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/settings-manager.ts#L12-L140));
- app and TUI keybinding action IDs ([`keybindings.ts`, lines 13-58](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/keybindings.ts#L13-L58), [64-207](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/keybindings.ts#L64-L207));
- extension event overloads, event union, UI methods, API methods, modes, tool names, and result
  variants ([`types.ts`, lines 1198-1244](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/extensions/types.ts#L1198-L1244), [1246-1436](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/extensions/types.ts#L1246-L1436));
- RPC command, response, extension-UI request, and extension-UI response unions
  ([`rpc-types.ts`, lines 20-73](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/modes/rpc/rpc-types.ts#L20-L73), [115-231](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/modes/rpc/rpc-types.ts#L115-L231), [237-283](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/modes/rpc/rpc-types.ts#L237-L283));
- remote protocol thinking levels, phases, transcript variants/progress, error codes, commands,
  results, events, and envelopes ([`schemas.ts`, lines 26-45](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/protocol/src/schemas.ts#L26-L45), [120-231](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/protocol/src/schemas.ts#L120-L231), [269-325](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/protocol/src/schemas.ts#L269-L325), [400-445](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/protocol/src/schemas.ts#L400-L445));
- coding-agent JSONL entry types and harness entry/record types
  ([`session-manager.ts`, lines 30-156](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/session-manager.ts#L30-L156), [`harness/session/types.ts`, lines 14-74](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/src/harness/session/types.ts#L14-L74), [80-212](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/src/harness/session/types.ts#L80-L212));
- known AI API/provider/image-provider IDs and every registered provider factory
  ([`ai/src/types.ts`, lines 17-80](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/types.ts#L17-L80), [`providers/all.ts`, lines 88-155](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/providers/all.ts#L88-L155));
- telemetry span names, event names, attribute names, status values, and parent rules from the
  typed schema definitions, with generated-doc set equality. The generated document shows that
  span attributes and enum values are part of the schema contract, not prose only
  ([`telemetry-schema.md`, lines 9-48](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/docs/telemetry-schema.md#L9-L48)).

Any extracted member without exactly one inventory ID fails the completeness gate. Any inventory ID
whose selector no longer resolves is an orphan and also fails.

### I5. Handler/producer/consumer closure

A declared command, event, setting, or protocol variant is not complete merely because its type is
inventoried. Build cross-references and require the expected roles:

```text
declared input -> parser/decoder -> handler -> result/event -> docs -> tests/evidence
```

Not every role is required for every kind, but missing roles need an explicit explanation. For
example, RPC commands and their success responses are separate unions
([`rpc-types.ts`, lines 20-73](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/modes/rpc/rpc-types.ts#L20-L73), [115-231](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/modes/rpc/rpc-types.ts#L115-L231)); an extractor should verify each command has a handler and a terminal response/error path rather than assuming union membership proves implementation.

### I6. Documentation coverage and contradiction detection

Parse every tracked Markdown heading and frontmatter block. Each heading that promises a command,
setting, environment variable, file format, mode, platform behavior, install method, or security
boundary becomes a candidate or an evidence link. Then compare docs against source-extracted sets:

- documented-but-unresolved items require a docs-only feature row, a stale-doc defect, or an
  accepted external dependency;
- source-only public items require documentation debt or an explicit internal-only classification;
- conflicting defaults/allowed values fail the gate.

This must scan files rather than only `docs.json`, because the documentation index includes
environment variables and platform/setup pages beyond the navigation manifest
([`index.md`, lines 69-84](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/docs/index.md#L69-L84)). Environment variables are independently observable inputs/outputs; the docs distinguish process configuration, process markers, and bash-tool session metadata
([`environment-variables.md`, lines 1-18](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/docs/environment-variables.md#L1-L18), [20-49](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/docs/environment-variables.md#L20-L49), [75-94](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/docs/environment-variables.md#L75-L94)).

### I7. Generated-data provenance closure

Generated outputs, generators, source data, manifests, and runtime registries must form a closed
chain. For AI models, assert all of the following at the pinned commit:

```text
generated aggregator provider IDs
= provider *.models.ts shards
= provider data JSON files
= manifest file keys
```

Pi already implements these exact checks in its model-data tooling
([`model-data.ts`, lines 85-115](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/scripts/model-data.ts#L85-L115), [193-229](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/scripts/model-data.ts#L193-L229)). The inventory should reuse the same sets and add disposition coverage for every provider/model/API/cost/capability field. It must also include dynamic providers that have no static generated catalogue: Pi explicitly says `KnownProvider` includes dynamic providers such as Radius beyond generated `BuiltinProvider` keys
([`providers/all.ts`, lines 50-53](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/providers/all.ts#L50-L53)). Image models/providers need a parallel inventory because image API IDs are distinct from chat APIs
([`ai/src/types.ts`, lines 31-33](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/types.ts#L31-L33), [78-80](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/types.ts#L78-L80)).

### I8. Example and dogfood coverage

Every tracked example and `.pi` resource must map to:

1. the canonical feature IDs it demonstrates;
2. an executable/syntax/typecheck status, when applicable;
3. any capability it deliberately degrades or mocks;
4. `example-only` if it is not a Pi parity promise.

The extension examples are a useful candidate catalogue because they exercise lifecycle/safety,
tools, commands/UI, git, compaction, resources, renderers, sessions, providers, and dependency
loading ([examples README, lines 17-45](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/examples/extensions/README.md#L17-L45), [47-81](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/examples/extensions/README.md#L47-L81), [90-137](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/examples/extensions/README.md#L90-L137)). They are not the canonical API denominator; that is the exported extension type surface.

### I9. Format, migration, and backend closure

For every persisted or wire format, inventory must separately cover:

- current variants and fields;
- version/negotiation rule;
- reader and writer;
- migration/compatibility behavior;
- malformed/partial/crash states;
- storage backend behavior;
- import/export/adapter paths;
- tests and fixtures for each supported old version.

The coding-agent file format is JSONL v3 with a tree and automatic v1/v2 migration
([`session-format.md`, lines 1-27](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/docs/session-format.md#L1-L27)). The newer agent harness has a different durable model with lanes, operation records, queue records, tool-start records, usage records, and a `SessionStorage` port
([`harness/session/types.ts`, lines 80-212](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/src/harness/session/types.ts#L80-L212), [290-318](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/src/harness/session/types.ts#L290-L318)). They must not be collapsed into one generic “sessions” row. The concrete SQLite backend adds migrations, branch caches, search, facts, records, statistics, and writer leases
([SQLite source tree](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/session-backends/sqlite-node/src)).

### I10. Acceptance-evidence closure

Every parity feature must link to deterministic tests, protocol fixtures, golden traces, behavioral
evals, manual platform evidence, or an explicit `evidence-needed` state. Tests/evals are not
automatically parity features, but no parity claim can be `compatible` without evidence.

Pi's eval harness supports real model selection, Pi auth, temporary project/agent directories,
reload steps, native session artifacts, comparisons, and model-backed judges
([`packages/evals/README.md`, lines 7-34](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/evals/README.md#L7-L34), [57-85](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/evals/README.md#L57-L85), [104-150](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/evals/README.md#L104-L150)). The inventory should map each eval case to feature IDs and record what it proves; the existence of an eval package is not evidence that every feature is covered.

### I11. Cross-surface alias and degradation closure

Require explicit relations whenever the same capability appears through multiple surfaces:

- CLI, SDK, interactive command, RPC command, remote command;
- TUI versus print/JSON/RPC behavior;
- extension UI in interactive versus RPC/headless modes;
- built-in provider versus extension provider;
- coding-agent JSONL versus harness storage versus remote transcript projection;
- npm versus standalone-binary install/update.

The extension API itself says each mode supplies its own UI implementation
([`extensions/types.ts`, lines 127-131](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/extensions/types.ts#L127-L131)) and exposes `tui`, `rpc`, `json`, and `print` modes with a separate `hasUI` capability flag
([lines 302-313](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/extensions/types.ts#L302-L313)). Therefore a feature row such as `ext:ui:custom` needs a per-mode support/degradation matrix; “extension API exists” is not sufficient.

### I12. Release-gate set equality

At a claimed parity release, the following query must return zero rows:

```text
status in {unknown, inventory-only, unowned, blocked, evidence-needed, incomplete}
OR missing source anchor
OR missing target/owner/issue
OR missing acceptance evidence
OR unresolved deviation
OR orphaned source selector
```

Before the full parity release, incomplete items may remain, but their presence must be visible and
release-scoped rather than silently absent.

## Omission traps and required countermeasures

### Generated model data and other generated artifacts

**Trap:** scanning hand-written providers or `KnownProvider` only. Pi's model catalogue is generated,
validated against provider shards and JSON data, and accompanied by a manifest; some providers are
dynamic and intentionally absent from the static catalogue
([`models.generated.ts`, lines 1-4](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/models.generated.ts#L1-L4), [`providers/all.ts`, lines 50-76](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/providers/all.ts#L50-L76)). Generated themes, HTML-export assets, native prebuilds, Photon WASM, shrinkwrap, install lock, SQL migrations, and generated telemetry docs create the same class of risk
([coding-agent build assets, lines 37-40](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/package.json#L37-L40); [SQLite package build, lines 19-24](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/session-backends/sqlite-node/package.json#L19-L24)).

**Countermeasure:** generate candidates from both sources and generated outputs; assert their
provenance/set equality; inventory runtime fields such as modalities, reasoning, context window,
max tokens, and four cost rates, which Pi's validator treats as required model metadata
([`model-data.ts`, lines 147-184](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/scripts/model-data.ts#L147-L184)).

### Examples and repository dogfooding

**Trap:** treating examples as non-product noise or treating them as canonical API definitions.
They demonstrate real capability combinations and migration pressure, but may include games,
diagnostics, dependencies, or repository-specific workflows rather than parity requirements.

**Countermeasure:** inventory every tracked example/dogfood resource as evidence, link it to
canonical feature IDs, and require an explicit `example-only`/`repo-workflow` disposition where it
is not a product promise. The extension examples explicitly include permission gates, sandboxing,
custom providers, RPC UI, custom editors, games, reload, compaction, and subagents
([examples README, lines 17-81](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/examples/extensions/README.md#L17-L81), [90-137](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/examples/extensions/README.md#L90-L137)).

### Documentation-only or documentation-led behavior

**Trap:** generating rows only from exported TypeScript symbols. Install commands, environment
variables, terminal/platform setup, project-trust rules, security boundaries, shell aliases, and
some operational defaults are user-visible without being a single exported symbol. Pi documents
three distinct environment-variable roles and a large process-configuration table
([`environment-variables.md`, lines 1-9](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/docs/environment-variables.md#L1-L9), [75-94](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/docs/environment-variables.md#L75-L94)).

**Countermeasure:** parse every tracked doc and heading, not just `docs.json`; reconcile docs with
source/env reads and tests; classify unresolved promises rather than dropping them. Project trust,
for example, has explicit discovery inputs, persistence, precedence, mode-specific behavior, and a
clear statement that it is not a sandbox
([`security.md`, lines 5-29](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/docs/security.md#L5-L29)).

### RPC and protocol unions

**Trap:** representing “RPC mode” or “remote protocol” with one row. Pi has two separate protocols:
coding-agent RPC is stdin/stdout JSONL
([`rpc-types.ts`, lines 1-6](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/modes/rpc/rpc-types.ts#L1-L6)), while `pi-protocol` is a transport-neutral CBOR remote-session protocol
([`packages/protocol/package.json`, lines 1-5](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/protocol/package.json#L1-L5)). Each contains multiple closed unions.

**Countermeasure:** one stable ID per command/result/event/error/UI method/schema variant, plus
handler and acceptance links. Assert exact set equality from the source unions. Keep JSONL RPC and
remote protocol namespaces separate even where names overlap.

### Extensions, hooks, UI, and mode degradation

**Trap:** counting `ExtensionAPI` as one feature, or counting only tool registration. The extension
surface includes lifecycle/resource/session/provider/input/tool events, result overrides, tool
metadata and rendering, commands, shortcuts, flags, message/entry/Markdown renderers, message
injection, session metadata, shell execution, tool activation, model/thinking control, provider
registration, an event bus, and a large UI interface
([`extensions/types.ts`, lines 1-9](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/extensions/types.ts#L1-L9), [131-282](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/extensions/types.ts#L131-L282), [1198-1436](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/extensions/types.ts#L1198-L1436)).

**Countermeasure:** extract every overload/method/literal, then create a capability-by-mode matrix
for TUI/RPC/JSON/print. Link each RPC-supported UI method to the JSONL union
([`rpc-types.ts`, lines 237-283](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/modes/rpc/rpc-types.ts#L237-L283)) and require an explicit degradation for extension UI methods absent from that union.

### Provider auth, streaming, usage, and catalogue behavior

**Trap:** assuming an Eino chat-model adapter or a provider-name list covers `pi-ai`. Pi providers
own auth, model listing/refresh/filtering, streaming, and optional deferred fetch/cancel behavior
([`models.ts`, lines 88-149](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/models.ts#L88-L149)). The collection adds auth checks, available-model filtering, OAuth login/logout, header transforms, completion, and deferred operations
([lines 151-229](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/models.ts#L151-L229)). Request options include transport, cache retention, session affinity, retry/timeout caps, proxyable fetch, headers, payload/response hooks, and telemetry
([`types.ts`, lines 119-173](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/types.ts#L119-L173), [175-218](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/types.ts#L175-L218)).

**Countermeasure:** create a per-provider matrix for APIs, auth methods, credential sources,
catalogue type, input modalities, reasoning/thinking mapping, transports, message/tool/image
conversion, retries, caching, deferred operations, usage fields, cost calculation, telemetry, tests,
and coding-agent integration. Provider-extension configuration also exposes OAuth subscription
semantics, refresh, model costs, capabilities, and custom streams
([`extensions/types.ts`, lines 1443-1516](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/extensions/types.ts#L1443-L1516)).

### TUI as a product-sized subsystem

**Trap:** counting only coding-agent screens or only exported components. The generic TUI owns two
renderer modes, differential/synchronized rendering, application-owned scrolling, overlays/focus,
terminal input/key protocols, width/ANSI handling, inline images, terminal capability detection,
and native platform helpers
([TUI README, lines 3-16](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/tui/README.md#L3-L16), [57-83](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/tui/README.md#L57-L83)). Coding-agent then adds its own interactive components, selectors, transcript rendering, themes, and workflows
([interactive source tree](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/modes/interactive)).

**Countermeasure:** inventory generic TUI exports and behaviors separately from coding-agent UI;
map platform-native artifacts and regression tests; require visual/virtual-terminal evidence for
rendering, widths, overlays, scrolling, images, and terminal restoration.

### Installation, packaging, and update

**Trap:** one `install/update` row. Pi supports npm and curl installation, multiple uninstall
commands, package install/remove/list/config/update, self/Pi updates, model-catalog refresh,
standalone binaries, source archives, offline model data, platform-specific self-update, locks,
audits, and release smoke tests
([docs index, lines 7-27](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/docs/index.md#L7-L27); [`args.ts`, lines 248-261](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/cli/args.ts#L248-L261); [root README, lines 63-88](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/README.md#L63-L88)). The package manager also resolves four resource types with precedence, filtering, local/npm/git sources, install/remove/update, and progress events
([`package-manager.ts`, lines 57-118](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/package-manager.ts#L57-L118), [159-201](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/package-manager.ts#L159-L201)).

**Countermeasure:** separate IDs for each install/distribution/update/package operation and target;
inventory packaged assets and locks; map workflows/scripts to evidence; record platform and offline
behavior separately.

### Session formats, projections, migrations, and backends

**Trap:** treating coding-agent JSONL, the agent harness's durable storage model, protocol transcript
snapshots, and SQLite as one format. They have different discriminants, recovery models, and
compatibility duties. The remote protocol itself distinguishes streaming/complete/error/aborted
assistant items and running/complete/error tools
([`schemas.ts`, lines 126-201](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/protocol/src/schemas.ts#L126-L201)).

**Countermeasure:** maintain separate namespaces and explicit projection/migration relations among
coding-agent JSONL, harness entries/records, storage backends, RPC messages, protocol transcript
items, HTML export/import, and session migration. Require old-version fixtures, crash/partial-state
cases, and backend conformance for each supported route.

### Evals and telemetry

**Trap:** omitting them because they are “internal,” or counting their existence as proof of
compatibility. Evals are evidence infrastructure and telemetry is both a generic public contract and
a domain schema. The telemetry package exports typed schema utilities, a no-op context, an in-memory
implementation, and a testing subpath ([`telemetry/src/index.ts`, lines 1-24](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/telemetry/src/index.ts#L1-L24), [`telemetry/package.json`, lines 8-26](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/telemetry/package.json#L8-L26)).

**Countermeasure:** inventory telemetry APIs and every declared domain span/attribute/event/status;
map eval/test cases to feature IDs; record coverage gaps. Keep eval runner/report/artifact behavior as
class-I engineering features while using individual eval results as acceptance evidence for the
features they exercise.

## Recommended extraction pipeline

1. **Manifest:** emit the pinned Git tree manifest and package roots.
2. **AST/JSON extraction:** emit public exports, package exports/bins/files/scripts, literal unions,
   overload registries, object registries, schema objects, settings/keybinding keys, provider/model
   IDs, and session/telemetry discriminants.
3. **Markdown extraction:** emit every tracked doc heading, frontmatter key, documented command,
   option, setting, environment variable, file path/format, install method, platform, and security
   promise.
4. **Asset extraction:** emit every packaged/generated/native/migration asset and its consumer/build
   rule.
5. **Evidence extraction:** emit test/eval names and example/dogfood files, then map them to feature
   IDs.
6. **Relation pass:** join declaration, implementation, docs, examples, tests, protocols, formats,
   migrations, and distribution paths.
7. **Disposition pass:** require each source candidate to resolve to a feature, evidence,
   infrastructure, internal detail, obsolete compatibility input, or reviewed exclusion.
8. **Gate:** run invariants I1-I12 and publish a machine-readable missing/orphan/contradiction
   report alongside the human parity matrix.

The first implementation should preserve the raw extracted candidate list as an artifact. Manual
curation should add product meaning and acceptance, not erase the machine denominator. On a future
Pi re-pin, diffing the two candidate sets will then show exactly which packages, commands, union
members, providers/models, docs headings, examples, assets, formats, tests, or workflows entered or
left scope.
