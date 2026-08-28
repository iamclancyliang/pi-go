# pi-go parity matrix

**Status:** implementation active · this matrix is the parity audit, not a precondition for it

**Approved source baseline:** `earendil-works/pi@086c32e74530564922d011ade23ff582c9d63116` (approved 2026-08-15; re-pin requires explicit review)
**Product requirement:** complete Pi feature accounting with no silent omissions

**Inventory authority:** @qy-liang decided on 2026-08-17 that the top-level denominator was not
enough to continue implementation, and lifted that hold on 2026-08-28. Implementation now proceeds
against axes whose semantics are already recorded, while the raw census, this matrix and the
source-coverage ledger continue toward every check in
`docs/product/feature-inventory-schema.md`. Satisfying those checks is still what lets pi-go claim
parity; it is no longer what lets it write feature code.

## How to use this matrix

`docs/product/pi-feature-inventory.md` is the raw primary-source census. This file normalizes that
census into product accounting; it must not invent missing Pi semantics or treat a package-level row
as feature-level coverage. Stable IDs, required fields and completeness checks are defined in
`docs/product/feature-inventory-schema.md`.

Every Pi feature or engineering surface must have:

- a pinned source/document reference;
- an implementation class;
- a target release;
- an owner and issue;
- reproducible acceptance evidence;
- a final status of `compatible`, `compatible-via-adapter`, or `accepted-deviation`.

`unknown`, `unowned`, and `blocked` prevent the parity release.

### Implementation classes

| Code | Meaning |
| --- | --- |
| B | Behaviour-compatible Go reimplementation; internal source shape may differ |
| W | Wire/data compatibility or a lossless migration adapter is required |
| R | Capability-equivalent redesign is expected because the runtime/language mechanism differs |
| I | Engineering equivalent for internal generation, testing, packaging, or evaluation infrastructure |
| N | Net-new pi-go requirement with no released Pi counterpart; requires its own PRD/ADR rationale and acceptance evidence |

An item can have multiple classes. `R` does not waive parity: it says the mechanism changes while
the user capability still needs acceptance evidence. `N` is an additional product gate, not parity
evidence; it cannot replace `B`, `W`, `R`, or `I` for a Pi surface.

## Initial inventory

This is the approved top-level denominator, not the feature-by-feature completion claim. It remains
as an index while the raw census is normalized into stable feature IDs. No row below is evidence
that its area is completely inventoried unless the source-coverage ledger and the corresponding
feature records are complete.

| Area | Pi source surface | Class | Target | Initial acceptance boundary | Status |
| --- | --- | --- | --- | --- | --- |
| Agent loop and messages | `packages/agent` | B | v0 | C1-C8 conformance traces, streaming and cancellation | in-spec |
| Tool execution | `packages/agent`, coding-agent tools | B | v0/v1 | validation, batch modes, ordering, abort, side-effect policy | in-spec |
| Model/provider layer | `packages/ai` | B/I | v1-v2 | provider calls, message conversion, streaming, auth, usage/cost, generated catalogue | inventory-needed |
| Coding-agent assembly | `packages/coding-agent` | B | v1-v2 | documented commands, settings, modes, resources, tools and workflows | inventory-needed |
| Terminal UI library | `packages/tui` | B/R | v2 | rendering, input, overlays, width handling, streaming UX | inventory-needed |
| Sessions and compaction | coding-agent session/compaction; AgentHarness | B/W/N | v1-v2 | resume, tree, fork/clone, summaries, context projection, migration; net-new crash-safe replay policy | partial-spec |
| Session storage port | session abstractions and harness storage contract | B/R | v1 | narrow persistence interface, atomicity/recovery needs, in-memory conformance implementation | architecture-needed |
| Concrete session backends | `packages/session-backends/*` | B/R/W | v3 | file/sqlite backends, migrations, leases and recovery | inventory-needed |
| Extensions | coding-agent extensions + examples | R/W | v0 seam / v2 product | v0 core seams for tools/events/policy/state/capabilities; v2 discovery, UI, lifecycle, degradation and TS migration; evaluate pigo's Node-host bridge without treating its no-op capabilities as parity | architecture-risk |
| Skills and prompt resources | coding-agent docs/resource loader | B/W | v2 | user/project discovery, precedence, invocation and compatible formats | inventory-needed |
| Themes | coding-agent themes | B/W | v2 | discovery, selection, rendering semantics and migration | inventory-needed |
| Pi packages / package manager | coding-agent package surface | B/R/W | v2 | install/update/remove, resource contribution, versioning and migration | inventory-needed |
| RPC mode | coding-agent RPC docs/implementation | B/W | v3 | bidirectional JSONL commands/events: prompt/steer/follow-up; state/model/thinking/queue modes; compaction/retry/bash; session switch/fork/clone/tree; extension UI; errors, ordering and degradation | inventory-needed |
| Protocol | `packages/protocol` | B/R | v3 | pi-go-native framing/schema/errors/events with semantic coverage of Pi's remote capabilities; direct Pi-wire interoperability and a live adapter are explicit non-goals under decision C; pigo provides no implementation shortcut | decision-set/inventory-needed |
| Client | `packages/client` | B/R | v3 | native remote-session control and error semantics; C4/C4.0 event ordering and C6 unmatched calls; unchanged Pi client connection is explicitly unsupported and tested/documented | decision-set/inventory-needed |
| Server | `packages/server` | B/R | v3 | native session hosting/lifecycle and compatibility with the pi-go client/protocol; Pi-wire migration/incompatibility boundary is explicit | decision-set/inventory-needed |
| Telemetry | `packages/telemetry` | B/R | v1-v3 | event/callback meaning, usage attribution and observability | inventory-needed |
| Behavioural evaluations | `packages/evals` | I | Phase 0-parity | equivalent regression/evaluation coverage for compatibility claims | inventory-needed |
| Installation and update | coding-agent installer/update paths | I/R/W | v2-parity | distribution, update, rollback and compatible user data handling | inventory-needed |
| Examples and dogfooding | examples, `.pi/` | I | rolling | representative examples run or have documented Go equivalents | inventory-needed |
| Documentation | package docs and product docs | B/I | rolling | every shipped compatible surface is documented for Go users | inventory-needed |

## Native remote-capability checklist (wire decision C)

ADR-0006 rejects live Pi wire interoperability. Therefore “a remote client can send input” is not
enough evidence for RPC/protocol/client/server parity. Each capability family below must end as
`native-equivalent`, `accepted-deviation`, or `incomplete`; `incomplete` blocks the relevant parity
release. The eventual feature inventory expands these families into individual commands, events,
errors, and state fields.

| Capability family | Pinned Pi evidence | pi-go acceptance boundary |
| --- | --- | --- |
| Connection and envelopes | `packages/protocol/src/schemas.ts`: protocol version, client/server hello, request id, success/error response, event envelope | native version negotiation, correlation, typed errors, and event delivery are specified and tested; no claim that Pi framing/schema is accepted |
| Server/session inventory | protocol `list`, `create`, `server_snapshot`, `session_removed`; server session registry | list/create/removal and metadata revisions have native equivalents with deterministic lifecycle semantics |
| Attachment and ownership | protocol `attach`, `detach`; session `attached`/`locked` state | native client ownership, contention, detach, and reconnect behavior are explicit; deviations from Pi locking are itemized |
| Live session control | protocol `prompt`, `steer`, `abort`, `set_model`, `set_thinking` | equivalent control points exist and preserve steering timing, cancellation, per-turn model/thinking changes, and error categories |
| Snapshot and transcript projection | server/session snapshots; transcript item/progress schemas | authoritative snapshots plus started/delta/updated/finished progress are reconstructable; C4/C4.0 ordering and C6 unmatched tool calls are preserved |
| RPC prompt lifecycle | `rpc-types.ts`: `prompt`, `steer`, `follow_up`, `abort`, `new_session`, `get_state`, `get_messages` | native headless surface covers input queues, state inspection, and terminal/error behavior without collapsing steer and follow-up semantics |
| RPC model and queue policy | `set_model`, `cycle_model`, model inventory; thinking-level commands; steering/follow-up modes | every setting has a native equivalent or explicit deviation, including supported-value discovery and mode-specific queue behavior |
| RPC compaction, retry, and shell | `compact`, auto-compaction, auto-retry/abort-retry, `bash`/`abort_bash`, session stats | native operations expose lifecycle, cancellation, typed failure, and usage evidence; compaction obeys S1–S6 |
| RPC session navigation and export | switch, fork, clone, fork messages, entries, tree, last assistant text, session name, HTML export | tree/navigation/export capabilities are individually accounted for; data-format differences require migration documentation rather than silent omission |
| Resource commands and extension UI | `get_commands`; extension UI request/response methods for select/confirm/input/editor/notify/status/widget/title/editor text | discovery and interactive methods are individually accounted for, with unsupported headless capabilities declared as degradation rather than no-op parity |

The source unions are the starting denominator, not the final specification. The G5 native-wire
contract owns framing, state machines, errors, and delivery rules; this checklist prevents that new
protocol from shrinking the Pi capability set unnoticed.

## Known parity risks

### Extensions are not a source-level port

Pi loads TypeScript extensions at runtime and gives them in-process access to hooks, tools, state,
and UI APIs. Go cannot natively reproduce that loading model. The architecture must choose among:

- embedding or launching a JavaScript/TypeScript runtime with a compatibility bridge;
- a new process-out protocol;
- a Go-native in-process API plus migration tooling;
- a hybrid.

The decision must track capability loss and migration impact per extension API. The presence of a
new Go plugin interface is not proof that existing Pi extensions remain usable.

### `pi-tui` is a product-sized subsystem

Eino does not provide Pi's differential terminal renderer. TUI parity is an independent workstream,
not a thin adapter around the agent loop.

### `pi-ai` is more than a chat-model interface

The parity inventory must include provider-specific message conversion, generated model metadata,
authentication flows, subscription/plan behaviour where present, streaming, retry, and usage/cost
accounting. Eino `ChatModel` coverage alone is insufficient.

### Evaluation parity is evidence infrastructure

`packages/evals` may not need matching internal APIs, but complete-replication claims require
equivalent behavioural evidence. It remains in the denominator as class `I`, not silently omitted.

### pigo is not a client/server wire precedent

Primary-source review at `smallnest/pigo@ef2c447b754b114b0eea87ff2ad1228bcb11dc84` found no Pi
CBOR codec, four-byte framing, hello/version schema, or standalone protocol/client/server package.
Its headless JSONL, subprocess JSON-RPC, and browser WebSocket control are pigo-owned surfaces and
cannot interoperate directly with Pi's client/server. They are partial analogues, not completed
class-R parity. See `docs/research/pigo-wire-compatibility.md`.

pigo is useful evidence for a different boundary: its embedded Node Pi-extension host loads the
real Pi SDK and adapts selected tools/commands onto pigo's JSON-RPC plugin protocol. That validates
the feasibility of a TS bridge candidate for ADR-0003, while its inert/no-op session, model, UI,
provider, and widget actions demonstrate why capability degradation must remain explicit.

## Inventory completion order

1. Pin the upstream baseline and inventory every documented coding-agent feature/command.
2. Expand `packages/agent` into the existing C1-C8 contract rows.
3. Inventory extension API events, UI methods, tools, state, and host-mode degradation.
4. Inventory session formats and both current session runtimes without conflating them.
5. Inventory `pi-ai` provider/auth/usage surfaces separately from Eino components.
6. Inventory TUI primitives and user-visible interaction behaviours.
7. Inventory RPC/protocol/client/server wire and event contracts.

This order organizes the work; it does not permit implementation to restart area by area. The hold
is lifted only after the whole inventory passes C0–C8 and the repository owner explicitly approves
reopening implementation.
