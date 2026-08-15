# pi-go product requirements document

**Status:** **approved** — scope, architecture, ADR-0001, and Pi baseline approved by @qy-liang 2026-08-15; implementation readiness gates remain

**Owner:** task #8 / @gpt-codex

**Engineering companion:** `docs/architecture/architecture.md` (owned separately; required before implementation)

**Parity inventory:** `docs/product/parity-matrix.md`

**Evidence:** `docs/specs/behaviour-contracts.md`, `docs/specs/eino-verification-plan.md`
**Approved Pi baseline:** `086c32e74530564922d011ade23ff582c9d63116` (parity denominator approved
2026-08-15; upstream additions do not enter automatically, and re-pinning requires explicit review)

## 1. Product summary

pi-go is a complete Go reimplementation of Pi, built with CloudWeGo/Eino. "Complete" means that the
finished product covers Pi's user-visible features, operating modes, extension surfaces, and
observable runtime behaviour against a pinned Pi reference version. Its purpose is still not to
translate the TypeScript repository file by file: Go package boundaries and implementation choices
may differ as long as the resulting product preserves the defined compatibility contract.

The first customer is the team building and reviewing pi-go, so the first product outcome is a
trustworthy vertical slice and conformance suite. The end customer is a Pi user who should be able to
move to the Go implementation without losing the supported Pi workflow. Transfer to an AI NAS Agent
is a later, independent downstream product and must not distort pi-go's compatibility target.

## 2. Problem

Agent frameworks make a basic ReAct demo easy, but they do not automatically preserve the behaviours
that determine whether an agent is safe, steerable, recoverable, or compatible with an existing
product. A naive Go rewrite can appear to work while silently changing:

- when steering and follow-up messages enter the conversation;
- whether truncated tool calls execute;
- tool-batch concurrency and event ordering;
- loop termination and abort behaviour;
- per-turn model changes;
- session, compaction, and recovery semantics.

The project needs an implementation path that distinguishes three things before committing to code:

1. Pi behaviour or feature that is part of the complete parity target;
2. Eino mechanisms that can satisfy it;
3. pi-go code that must own the remaining gap.

Without this separation, "use Eino" or "complete Pi rewrite" is too ambiguous to estimate,
implement, or accept. A feature-parity claim also needs a living inventory; README-level similarity
or a successful ReAct loop is not evidence of completeness.

## 3. Product principles

1. **Observable contracts over source-shape compatibility.** Preserve what users, clients, and tests
   can observe; do not preserve TypeScript class or file boundaries for their own sake.
2. **Evidence over framework-name assumptions.** An Eino API with a similar name is a candidate, not
   proof of semantic equivalence.
3. **Small core, explicit policy seams.** Stable runtime mechanisms belong in the core. Opinionated
   workflows, provider selection, permissions, and product-specific UI should remain replaceable.
4. **Deterministic acceptance first.** Core behaviour must be testable with fake models and tools;
   hosted LLM variability must not be required to decide whether the runtime is correct.
5. **Truth and projection stay separate.** Durable history, model context, provider-input repair, and
   recovery state must not be collapsed into a single message slice.
6. **Side effects fail conservatively.** A malformed, truncated, interrupted, or replay-uncertain
   tool call must never be promoted into a successful execution fact.
7. **Quirks are classified, not copied accidentally.** If Pi exposes a known defect or unsafe edge,
   pi-go must either preserve it because compatibility requires it, record an accepted deviation, or
   define a net-new hardening requirement with its own evidence. A hardening requirement does not
   count as completion of the original parity item.

## 4. Users and primary jobs

### 4.1 Runtime maintainer

Needs to change loop, session, provider, or tool code and know whether Pi-compatible semantics were
preserved. The maintainer's primary job is to run a deterministic conformance suite and inspect a
golden event trace when it fails.

### 4.2 Tool and provider integrator

Needs stable interfaces for adding a model or tool without depending on CLI/TUI internals, and needs
clear cancellation, streaming, error, usage, and execution-mode contracts.

### 4.3 Terminal agent user

Needs to submit a task, observe progress, use tools, steer work in progress, queue follow-up work,
cancel safely, and resume useful context. At the parity release, this user should also retain Pi's
documented CLI modes, resource system, session workflows, extension capabilities, and remote-control
features unless a deviation is explicitly accepted and documented.

### 4.4 Future AI NAS Agent team

Needs evidence about which Pi/pi-go principles transfer to NAS operations. It is a downstream
consumer of architectural lessons, not a reason to add NAS-specific features to pi-go v0 or v1.

## 5. Scope by release

### 5.1 Phase 0 — implementation readiness

No production feature implementation is released from this phase. Its deliverables are:

- this PRD, reviewed against the intended product;
- an approved technical architecture and module dependency diagram;
- a complete Pi feature/parity inventory pinned to a source commit;
- ADR-0001 for module boundaries;
- ADR-0002 for the Eino ownership boundary, decided by executable spikes rather than preference;
- a requirement-to-test traceability matrix;
- fixed toolchain and Eino version baselines.

Only isolated experiments may run before Phase 0 is accepted. Spike code must not become the default
architecture merely because it was written first.

### 5.2 v0 — contract tracer bullet

v0 is a developer-facing vertical slice. It must demonstrate one complete path:

```text
prompt -> model boundary -> agent loop -> one read-only tool -> model answer -> event trace -> session snapshot
```

Required:

- deterministic fake-model and fake-tool execution;
- one read-only tool with explicit execution metadata;
- the inner model/tool loop and the outer follow-up loop;
- steering and follow-up as distinct inputs;
- the C1-C8 behaviour-contract tests;
- machine-readable events and a golden trace;
- cancellation that can preserve an unmatched tool call without crashing persistence or recovery;
- a minimal session/context abstraction sufficient to prove truth-versus-projection boundaries;
- owned core seams for tool registration, event observation, pre-execution policy, and host
  capability discovery, without committing v0 to an extension transport;
- an executable Eino adapter decision based on the Phase 0 spikes.

Not required for v0:

- a polished TUI;
- broad provider support or interactive login;
- durable production storage;
- full Pi extension, skill, prompt, package, RPC, or remote-session compatibility;
- arbitrary write/shell tools;
- automatic compaction using a hosted summarization model.

### 5.3 v1 — usable local coding agent

v1 turns the proven runtime into a local terminal product. It adds:

- at least one real model provider and a documented credential flow;
- read, search, edit/write, and command tools with conservative defaults;
- a usable terminal interaction surface with streaming and cancellation;
- durable local sessions, resume, and model-context reconstruction;
- compaction with explicit cut-point and retained-context semantics;
- bounded overflow recovery;
- configuration and extension seams that do not require changing the agent core;
- usage and error observability sufficient to diagnose a failed run.

v1 is not the end of the complete-replication programme. Exact CLI and on-disk compatibility may be
phased, but every omitted Pi feature remains visible in the parity matrix rather than silently
disappearing from scope.

### 5.4 v2 — self-extensible Pi product surface

v2 targets the product identity beyond the core loop:

- Pi-compatible extension lifecycle, hooks, custom tools, state, and UI capability/degradation rules;
- skills, prompt templates, themes, project/user resource discovery, and Pi package conventions;
- CLI commands and interactive workflows for model, session, tree/fork/clone, compaction, and resource
  management;
- a terminal UI with the user-visible behaviours required by the pinned parity inventory;
- provider catalogue, authentication, usage/cost, retry, and cross-provider message conversion for
  the providers selected by the parity baseline.

### 5.5 v3 — remote and multi-surface parity

v3 adds the Pi surfaces that cross process or UI boundaries:

- RPC session control and event streaming;
- pi-go-native protocol, client, and server packages that provide semantic equivalents for the
  pinned Pi remote-session capabilities;
- an explicit migration and incompatibility boundary: unchanged Pi clients/servers are not promised
  direct interoperability, and pi-go does not ship a live Pi wire adapter;
- remote/headless UI degradation rules;
- pluggable durable session backends;
- telemetry and the behavioural evaluation surface needed to validate the full product.

### 5.6 parity release

The parity release closes the pinned Pi inventory. It requires every item to be one of:

1. **compatible** — implemented and covered by acceptance evidence;
2. **compatible through migration/adapter** — user data or workflow is preserved through an explicit
   compatibility layer;
3. **accepted deviation** — deliberately different, with user impact and rationale approved;
4. **blocked** — not releasable as "complete" while this remains.

The release does not require Go API source compatibility with TypeScript, identical internal package
shapes, or byte-for-byte event/session encoding unless the compatibility matrix explicitly requires
it. It does require full feature accounting and no silent omissions.

Work items carry an implementation class:

- **behaviour-compatible reimplementation** — different Go code, same observable product contract;
- **wire/data compatible** — existing clients, configuration, sessions, or resources require the
  same format or a lossless migration adapter;
- **capability-equivalent redesign** — the source mechanism depends on the TypeScript/runtime model
  and must be redesigned without reducing the user-visible capability;
- **engineering equivalent** — internal generation, evaluation, packaging, or test infrastructure
  needs an equivalent outcome but not identical source shape.
- **net-new pi-go requirement** — an additional product, safety, or operability contract has no
  released Pi behavior to compare against. It must trace to a pi-go PRD/ADR and its own acceptance
  evidence. It is a release gate where assigned, but it is not evidence that any Pi parity item is
  complete and cannot replace one of the compatibility classes above.

The extension system is a known capability-equivalent-redesign risk. Go cannot natively load a
TypeScript module as in-process Go code. Exact TS extension compatibility would require an embedded
or external JavaScript runtime and a bridge; a Go/process-out extension protocol changes the API.
The parity matrix must therefore separate user capabilities (install, add tools/UI, observe hooks,
persist state) from the current in-process TS API. Neither path may be called "identical" without
evidence.

## 6. Functional requirements

### FR-1 — Run lifecycle

- Accept a user prompt and produce a final assistant result.
- Continue model/tool turns until the inner loop stops.
- When the run would stop, read follow-up work and restart only if follow-up exists.
- A turn ending with `error` or `aborted` must emit its terminal events and return without consuming
  queued follow-up work.
- Expose explicit run, turn, message, tool, and terminal events.

### FR-2 — Steering and follow-up

- A steering message received during work must enter context before the next model request.
- A follow-up message must not interrupt the current inner loop; it is read only at the outer-loop
  stop boundary.
- The event trace and tests must reveal which path accepted each message.

### FR-3 — Tool safety and execution

- A response stopped by output length executes zero tool calls from that response.
- Any sequential tool makes the whole batch sequential.
- Parallel execution must preserve Pi's distinct start, completion, and result orderings.
- Batch termination requires all finalized calls to request termination.
- Cancellation may leave calls without results; storage and provider adaptation must tolerate it.
- Tool metadata must state at least execution mode and replay policy before v1 enables side effects.

### FR-4 — Model and Eino boundary

- The runtime must not expose Eino-specific state as its public product contract.
- A turn may replace the model and reasoning configuration for the next turn while preserving
  conversation continuity.
- The selected Eino integration must pass the pinned-version spikes and conformance tests.
- A failed spike must result in an explicit architecture change, not a weakened requirement.

### FR-5 — Events and observability

- Events must be machine-readable, ordered, and testable without a TUI.
- Tool start/preparation, completion, and result events must preserve their documented mode-specific
  interleaving.
- Errors must distinguish retryable provider failure, context overflow, cancellation, tool failure,
  and invalid/truncated model output.
- v1 must expose enough usage data to attribute model and summarization work.

### FR-6 — Session, context, compaction, and recovery

- Durable history and model context must be separate abstractions.
- Selecting a branch or lane must produce a deterministic context projection.
- Compaction must add an auditable checkpoint and change future model input without pretending that
  prior history was erased.
- Provider-facing repair for a dangling tool call must not claim that the tool executed.
- Overflow compact-and-retry must be bounded; the initial target is one automatic recovery attempt.
- Before side-effecting tools ship, crash recovery must distinguish replay-safe, replay-forbidden,
  interrupted, and unknown external-effect states.

### FR-7 — Extensibility

- Core runtime packages must not import terminal-product packages.
- Providers, tools, session backends, and event consumers must be replaceable behind owned pi-go
  interfaces.
- v0 must define the narrow core seams later extensions need: tool registration, event observation,
  pre-execution policy/denial, state namespace ownership, and host capability discovery. v0 does not
  need to ship a process-out or TypeScript transport.
- v1 must document which extension capabilities are in-process only and which, if any, can cross a
  process boundary.
- RPC/session control must not be described as an extension host unless tool registration, hooks,
  state, lifecycle, and capability discovery are actually implemented.
- The parity release must account separately for extension installation/discovery, custom tools,
  hook/event coverage, state, UI capabilities, lifecycle, and host-mode degradation.
- If existing TypeScript extensions are not directly executable, pi-go must provide either a
  documented compatibility bridge/migration tool or an accepted deviation; a new Go extension API
  alone is not silent parity.

### FR-8 — Full Pi parity accounting

The parity matrix must cover, at minimum:

- agent loop, messages, tools, events, steering/follow-up, model/thinking changes;
- model providers, authentication, streaming, usage/cost, retry, and message conversion;
- CLI commands, interactive modes, TUI behaviour, configuration, and environment integration;
- sessions, resume, tree navigation, fork/clone, compaction, branch summaries, and backends;
- extensions, hooks, custom tools, state, UI, skills, prompts, themes, and Pi packages;
- RPC, protocol, client, server, remote/headless behaviour, and capability degradation;
- telemetry, evaluation, migration, packaging, installation, and update workflows that are part of
  the pinned Pi product.

Each item needs a source reference, target release, owner, status, and acceptance evidence. A package
or feature discovered later is added to the matrix; it does not become out of scope by omission.

## 7. Non-functional requirements

### NFR-1 — Determinism

All C1-C8 contract tests run offline with fake models/tools and have stable expected traces.

### NFR-2 — Cancellation and goroutine hygiene

Every streaming or concurrent API accepts `context.Context`; stopping the consumer must not leak a
producer goroutine or leave a mutable session with multiple writers.

### NFR-3 — Dependency direction

The public SDK and internal runtime must compile without importing CLI/TUI packages. Architecture
tests or package placement should make invalid directions difficult to express.

### NFR-4 — Compatibility evidence

Every claimed Pi-compatible behaviour links to a source commit and a conformance test. Behaviour
changes require an explicit decision, not an accidental golden-file update.
Under wire decision C, direct Pi-client/Pi-server interoperability is an explicit accepted deviation,
not a compatibility claim; tests must prove semantic capability coverage and the documented
incompatibility boundary rather than pretending unchanged clients can connect.

### NFR-5 — Version reproducibility

Go, Eino, and other behaviour-relevant dependencies are pinned. Upgrades require rerunning the
relevant spikes and contract suite.

### NFR-6 — Security posture

No side-effecting tool is enabled by default until its validation, cancellation, replay, audit, and
user-control semantics are documented. Application-level permissions and OS/container isolation are
separate controls and must not be conflated.

## 8. Acceptance scenarios

These scenarios are release gates, not illustrative examples.

| ID | Scenario | Expected observable result | Release |
| --- | --- | --- | --- |
| A1 | Fake model requests two tools, then answers | Complete event trace and final answer | v0 |
| A2 | Steering arrives while a tool round is active | Message appears before the next model request | v0 |
| A3 | Follow-up is queued during a run | It is consumed only after the current run would stop | v0 |
| A4 | Output is truncated with three parseable tool calls | Zero tools execute; three failures are observable | v0 |
| A5 | One tool in a three-call batch is sequential | No execution intervals overlap | v0 |
| A6 | Parallel A is slow and B is fast | starts source-ordered; ends B then A; results A then B | v0 |
| A7 | Only one result requests terminate | The inner loop continues | v0 |
| A8 | Cancellation occurs after a tool call is emitted | Session/context can retain an unmatched call and recover | v0 |
| A9 | Next-turn hook changes model configuration | Next model call uses the new config without losing context | v0 |
| A10 | Context overflow happens twice | One compact-and-retry attempt, then a terminal error | v1 |
| A11 | Process dies after destructive-tool intent, before settlement | Tool is not blindly replayed; state is explicit | v1 |
| A12 | Compaction is performed | Durable history remains auditable; model input becomes summary + recent context | v1 |
| A13 | Pi remote-session command/event families are exercised through the pi-go-native client/server, and an unchanged Pi client attempts to connect | Native protocol covers the specified semantic capabilities while preserving C4/C4.0 ordering and C6 unmatched calls; direct Pi-wire interoperability is explicitly unsupported and fails/documented as such, with no live-adapter claim | v3 |
| A14 | A follow-up is queued during a turn that ends with `error` or `aborted` | `turn_end` carries no tool results, `agent_end` follows, and the queued follow-up is not consumed | v0 |
| A15 | The same two tools run once sequentially and once in parallel | Sequential mode emits each result before the next start; parallel mode emits every start before any result | v0 |
| A16 | Parallel call A fails during preparation before calls B and C are prepared | The legal trace includes `startA -> endA -> startB`; the implementation does not impose an all-starts-before-all-ends invariant | v0 |

The detailed C1-C8 evidence and test sketches remain in
`docs/specs/behaviour-contracts.md`. This table defines which product release is blocked by each.

## 9. Success metrics

### Phase 0

- PRD and technical architecture are reviewed with no unresolved contradiction about v0 scope.
- The pinned Pi reference version and first complete parity inventory are recorded.
- Each v0 requirement maps to an issue and an acceptance test.
- Eino spikes produce recorded traces and a decision for ADR-0002.

### v0

- 100% of C1-C8 tests pass offline and under `go test -race`.
- A new contributor can run the tracer bullet and identify each event in its golden trace.
- No v0 acceptance scenario depends on a live model API.
- Eino-specific types do not leak into the public pi-go runtime interface unless an ADR explicitly
  accepts that coupling.

### v1

- A user can complete, cancel, resume, and compact a local coding session without corrupting it.
- Destructive tools cannot be silently replayed after an uncertain crash window.
- Provider and summarization failures are classified and observable rather than returned as one
  generic error.

### Parity release

- The parity matrix contains no `blocked`, `unknown`, or unowned items.
- All compatibility claims have automated or reproducible acceptance evidence.
- Existing Pi users have documented migration paths for configuration, sessions, resources, and
  extensions where direct compatibility is not provided.
- Every accepted deviation is explicit in release documentation; none is inferred from absence.

Feature count, package count, and README claims are not success metrics.

## 10. Product decisions

### 10.1 Open decisions

These require an explicit answer before the affected release; the PRD does not guess them.

| Decision | Blocks | Options to evaluate |
| --- | --- | --- |
| First real provider and auth flow | v1 | provider choice, environment credentials, interactive login, external credential helper |
| Initial terminal implementation | v1 | plain streamed CLI first; custom TUI immediately; reuse a Go terminal library while preserving final Pi behaviour |
| Durable session model | v1 architecture | Pi JSONL v3 compatibility; new harness-style store; migration boundary between them |
| Eino ownership depth | v0 implementation | prebuilt agent loop; custom graph/TurnLoop; component library only |
| Extension implementation model | v0 seam / v2 product | in-process Go; embedded/TS compatibility layer; process-out protocol; hybrid with capability discovery |
| Tool permission boundary | v1 | application approval; OS/container isolation; both with explicitly separate guarantees |
| Distribution target | v1 | source/build only; single binary; package manager releases |

### 10.2 Resolved decisions

| Decision | Resolution | Consequence |
| --- | --- | --- |
| Pinned Pi reference version | `086c32e74530564922d011ade23ff582c9d63116` (accepted 2026-08-15) | this commit is the complete parity denominator; later upstream additions require an explicit re-pin review and regenerated inventory |
| Wire compatibility model | **C — semantic equivalence without a live adapter** (accepted 2026-08-15) | pi-go owns its native protocol. Unchanged Pi clients/servers are not supported for direct connection. Remote command/event capabilities still remain in the parity inventory and require semantic conformance evidence plus explicit migration/incompatibility documentation |

## 11. Implementation readiness gate

Formal feature implementation may start only when all of the following are true:

1. This PRD is reviewed, the complete-replication target is accepted, and a pinned Pi baseline is
   selected for the parity inventory.
2. The technical architecture contains a module/dependency diagram and owned public interfaces.
3. ADR-0001 states module boundaries.
4. The three Eino spikes have executable plans; ADR-0002 either records their result or explicitly
   marks the dependent implementation as blocked.
5. Every v0 acceptance scenario maps to a test owner and an engineering issue, and every discovered
   Pi feature has an entry in the parity inventory even when scheduled after v0.
6. The repository defines the minimum Go version, formatting, test, race-test, and dependency-update
   commands in `AGENTS.md` or equivalent contributor documentation.

Scaffolding that exists before this gate is exploratory. It gains no architectural authority merely
by being first.

## 12. Out of scope for this PRD

- The detailed module and interface design: belongs in the technical architecture.
- The final choice between Pi JSONL v3 and the new AgentHarness storage model: architecture + ADR.
- A complete AI NAS Agent product definition or NAS-specific features: NAS Agent is a separate
  downstream product that may reuse pi-go after Pi parity is established.
- Release/deployment operations beyond a local developer build.
