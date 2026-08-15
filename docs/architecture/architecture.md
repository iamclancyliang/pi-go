# pi-go technical architecture

**Status:** **accepted** — approved by @qy-liang 2026-08-15 · task #7
**Product companion:** `docs/product/PRD.md`, `docs/product/parity-matrix.md`
**Evidence:** `docs/specs/behaviour-contracts.md` (C1–C8), `docs/specs/eino-verification-plan.md`
**Upstream baseline:** Pi `086c32e` — **approved 2026-08-15 as the parity denominator**; upstream additions after this commit do not enter automatically, re-pinning needs its own approval · **Framework baseline:** eino `v0.9.14` (`32759e6…`)

## 0. What this document decides, and what it deliberately does not

**Decides:** the module partition, the dependency rules between modules, which seams must exist at
v0, and the register of open decisions.

**Does not decide:** whether pi-go's agent loop is built on eino's prebuilt loop or owns its own
orchestration. That is **ADR-0002**, and it is gated on spikes (#4/#5/#6). This document is written
so that *either* answer can land without restructuring the modules — see §4.

Anything below marked **[OPEN]** is not yet a decision. Do not implement against it.

---

## 1. Module partition (normative)

The partition follows one rule: **modules merge when their contracts are mutually referential, and
stay separate when one must remain ignorant of the other.**

pi's own dependency graph already encodes most of this answer, and it is the best available
evidence — what pi made zero-internal-dependency must stay independent; what pi forbids depending
upward must stay ignorant; what pi keeps in a single file must not be split.

### 1.1 Merged modules

| Module | Absorbs (parity-matrix rows) | Why it is one module |
| --- | --- | --- |
| **runtime core** | agent loop, tool execution | The batch contracts (C3–C6: mode downgrade, three orderings, abort cut, all-agree terminate) **are** loop contracts. Splitting them forces the event sink and batch state across a module boundary, widening and shallowing the interface. |
| **session** | sessions, compaction, storage port | Compaction is a **projection over session truth**, not a separate concern (PRD principle 5). A shared model with one narrow persistence port. |
| **wire schema** | protocol (schema only) | The deep module is the **schema**: message/command/event types and their encoding, defined once. Defining the RPC command surface separately from the protocol types guarantees drift. |

**client, server, and RPC mode are *not* part of that module.** They are **thin adapters that depend
on `wire schema`**, and they remain separately deployable — a server process and a client library
have different lifecycles and release boundaries. The merge claim is about the *schema* being
single-source, not about collapsing four deployables into one implementation.

### 1.2 Independent subsystems

| Subsystem | Must not know about | Rationale |
| --- | --- | --- |
| **tui** | agents, sessions, wire | Zero internal dependencies in pi — the strongest independence signal in its graph. A terminal rendering library, not an agent component. |
| **ai** (model/provider) | agent workflow | pi forbids `ai → agent-core`. **Critical here:** eino's `ChatModel` lives in this layer; if agent semantics leak in, eino stops being swappable and ADR-0002 loses the option it exists to protect. |
| **extension host** | core internals | An independent subsystem. **Transport is undecided — see ADR-0003** (in-process Go, TS bridge, out-of-process, or hybrid are all still open). What is decided: the core owns the five seams in §2 and nothing more; the host is whatever sits behind them. |
| **telemetry** | everything | Contracts and schemas only; zero dependencies in pi. |
| **install/update**, **evals** | — | Packaging and test infrastructure. **One-way only:** they may depend on the runtime; the runtime must never depend on them. Precedent: pi's `coding-agent-install` is a lockfile root that isn't even a workspace member — distribution stays out of the architecture graph. |

### 1.3 Dependency graph

Arrows point from dependent to dependency and are **normative**. Changes that alter the module
partition or violate a §1.5 rule require an ADR; rule-compliant edges go through normal review
(same gate as §1.4).

```text
   packaging & test — may depend on anything; nothing may depend on them
   ┌──────────────────┐   ┌───────┐
   │ install / update │   │ evals │
   └────────┬─────────┘   └───┬───┘
            │                 │
            ▼                 ▼
   ┌──────────────────────────────────────────────────────────────┐
   │ coding-agent assembly   (primary composition root)           │
   └──┬───────┬──────────┬───────────┬───────────┬──────────┬─────┘
      │       │          │           │           │          │
      ▼       ▼          ▼           ▼           ▼          ▼
  ┌──────┐ ┌─────┐ ┌──────────┐ ┌─────────┐ ┌─────────┐ ┌──────────┐
  │ tui  │ │ ai  │ │ runtime  │ │ session │ │ext host │ │ RPC mode │
  │      │ │     │ │ core     │ │         │ │         │ │          │
  └──────┘ └─────┘ └────┬─────┘ └────┬────┘ └─────────┘ └────┬─────┘
              ▲         │            │        (no edge:      │
              │         │            │      transport open,  │
              └─────────┘            ▼        ADR-0003)      │
            via model port     ┌──────────┐                  │
        (ai's published face)  │ storage  │                  │
                               │ port     │                  │
                               │(session  │                  │
                               │  owns)   │                  │
                               └──────────┘                  │
                                                             │
              ┌────────┐        ┌────────┐                   │
              │ client │        │ server │                   │
              └───┬────┘        └───┬────┘                   │
                  │                 │                        │
                  └────────┬────────┴────────────────────────┘
                           ▼
                 ┌───────────────────┐
                 │   wire schema     │
                 └───────────────────┘

   telemetry ── contracts only: others may depend on it; it depends on nothing
```

**Reading notes** (the diagram and these notes must stay mechanically consistent).
- **Arrows point from dependent to dependency**, without exception.
- **model port** is drawn as a labelled edge, not a node: `runtime core ──model port──▶ ai`. It is
  the interface **`ai` defines and implements**; eino and every provider adapter are hidden **inside
  `ai`'s implementation**. The port is `ai`'s published face, so there is no separate node and no
  cycle.
- **storage port** is defined and owned by `session`.
- **`extension host` has no outgoing edge.** It is assembled by the composition root and reaches the
  runtime only through the §2 seams. It deliberately does **not** depend on `wire schema` — drawing
  that edge would pre-decide ADR-0003 in favour of a protocol transport. If ADR-0003 selects
  out-of-process, that edge is added then, and registered here.
- `runtime core` does **not** depend on `tui`, `wire schema`, or the extension host. It reaches the
  outside world only through the §2 seams and the model port.
- `client`, `server`, and `RPC mode` each depend on `wire schema`; they do not depend on each other.
- `telemetry` is drawn detached: contracts only, zero dependencies, as in pi.

### 1.4 Seam and interface ownership

PRD gate #2 requires named owners. **"Owner" = the module that defines the interface.**

**When a change needs an ADR** (ADRs record hard-to-reverse, non-obvious decisions with real
trade-offs — not routine work):
- changing the module partition;
- inverting or exempting a §1.5 dependency rule;
- widening an already-published compatibility contract.

Ordinary dependency and interface changes that stay inside the rules go through normal review.

| Interface / seam | Owned by | Consumed by | Release | Notes |
| --- | --- | --- | --- | --- |
| model port | `ai` | runtime core | v0 | `ai` defines **and implements** it; eino + provider adapters hidden inside `ai` (§4) |
| tool registration seam | runtime core | extension host, coding-agent | v0 | §2 |
| event observation seam | runtime core | extension host, telemetry, wire adapters | v0 | event stream is a public contract (§3.2) |
| pre-execution policy / denial seam | runtime core | extension host | v0 | **policy + denial only.** Argument rewrite is *not* approved — see §6.3 |
| state namespace seam | runtime core | extension host | v0 | per-extension isolation |
| host capability discovery | runtime core | extension host | v0 | **degradation declared here** (§2) |
| storage port | session | runtime core, coding-agent | **v1** (memory adapter) | v0 has only a minimal session/context abstraction; concrete backends v3 |
| wire schema | wire schema | client, server, RPC mode | v3 | **decided: option C, semantic-only** (§6.4, ADR-0006) — schema is pi-go's own; **never claim Pi wire compatibility** |
| render surface | tui | coding-agent | v2 | tui must not know agents |

### 1.5 Dependency rules

1. Dependencies point **inward and downward** only; no cycles.
2. `ai`, `tui`, `telemetry` must be usable without importing the runtime core.
3. The runtime core must not import `tui`, `wire`, or `extensions`.
4. Test and packaging subsystems may depend on anything; nothing may depend on them.
5. Every cross-module edge that exists because of eino must be listed in §4, so ADR-0002 can be
   re-decided without archaeology.

---

## 2. Seams required at v0

These exist in v0 **even though the features that use them ship later**, because retrofitting a seam
means reworking the core.

**Extension seams** (product surface is v2; the seams are v0):
- tool registration
- event observation
- pre-execution policy / denial
- state namespace
- host capability discovery

**Capability degradation is part of the contract, from v0.** pi learned this the hard way: a large
set of its extension UI methods silently degrade to no-ops under RPC and headless modes, and it
could only document that after the fact. A process boundary loses more capability than a module
boundary, so any seam we expose must declare what it does under each host mode rather than
discovering it later.

**Session storage port** (concrete backends are v3; the port is v1): an in-memory implementation
must prove the port, atomicity, and recovery contracts so that adding file/sqlite backends later
does not rework the model.

---

## 3. Cross-cutting models

### 3.1 Truth vs projection
Durable history, model context, provider-input repair, and recovery state are **separate
representations**, never one message slice. Compaction produces a projection; it must not mutate
truth. This is what makes C6 (unmatched tool calls) survivable rather than corrupting.

### 3.2 Event model
The event stream is a **public contract**, not a debugging aid — RPC clients render from it. Three
orderings must hold simultaneously (C4), and the sequential/parallel paths **interleave
differently** (C4.0). Implementation note: a single emitter behind a `parallel bool` flag will break
one of the two modes, and because one sequential-declaring tool downgrades a whole batch at runtime,
both interleavings must be correct in the same build.

### 3.3 Conservative failure
A malformed, truncated, interrupted, or replay-uncertain tool call must never be promoted into a
successful execution fact (C2, C6). This is a correctness rule, not error handling.

---

## 4. The eino boundary **[OPEN — ADR-0002]**

**Settled:** eino provides the model/provider component (`ChatModel`) inside the `ai` subsystem.

**Open:** whether the agent loop is built on eino's prebuilt loop (`TurnLoop` / Runner /
ChatModelAgent) or pi-go owns orchestration and uses eino as a component library.

Verified present at v0.9.14 (`adk/turn_loop.go`, exported): `TurnLoop.Push(item, opts...)`,
`WithPreempt(SafePoint)`, `SafePoint{AfterChatModel, AfterToolCalls, AnySafePoint}`, Stop options,
interrupt/checkpoint types. **Presence is not equivalence** — see the spike plan.

Decision table (spike outcome → conclusion) lives in `eino-verification-plan.md` and issue #3, so
this ADR is *looked up*, not re-argued.

**Structural requirement either way:** every module edge that exists only because of eino is listed
here, and the runtime core talks to the model layer through a pi-go-owned interface. If ADR-0002
lands on "own the loop", no module moves.

**Current eino edge register — exactly one edge today:**

| # | Edge | Status |
| --- | --- | --- |
| E1 | `ai` implementation → eino `ChatModel` + provider adapters | **settled**; hidden behind the model port |
| — | any `runtime core` → eino loop/TurnLoop edge | **candidate only, pending ADR-0002** |

No other eino edge may be added without updating this table.

---

## 5. ADR register

| ADR | Subject | Status | Gate |
| --- | --- | --- | --- |
| 0001 | Module boundary (`internal/` + one public SDK package) | **accepted** — `docs/adr/0001-module-boundary.md` | approved 2026-08-15 |
| 0002 | eino ownership boundary | **blocked** | spikes #4/#5/#6 |
| 0003 | Extension transport (in-process / out-of-process / hybrid) | **[OPEN]** | v0 seams first; transport at v2 |
| 0004 | Session storage port shape | proposed | v1, in-memory implementation |
| 0005 | Event emission strategy (dual interleaving) | proposed | C4/C4.0 conformance tests |
| 0006 | Wire strategy — **option C, semantic equivalence only** | **accepted** — `docs/adr/0006-native-wire-semantic-equivalence.md` | decided by @qy-liang 2026-08-15; see §6.4 |

## 6. Decisions from cross-review, and what remains open

### 6.1 Scheduler — **decided: no public interface at v0**
Tool concurrency, the C3 runtime downgrade, and the C4/C4.0 orderings stay **inside** the runtime
core. A private batch coordinator is fine; tests go through the runtime core's own interface. Only
promote it to a public seam when a second real implementation exists — otherwise it is a shallow
abstraction bought at full cost.

### 6.2 TUI — **decided: independent architecture module, single Go module for now**
`tui` is an independent **architecture** module from day one, but lives in this repository and the
root Go module. Independence is enforced by import direction and an architecture test, not by a
nested `go.mod`. Split into its own Go module only when it has an independent release cadence or a
real external consumer — version-management cost should follow need, not anticipation.

### 6.3 Pre-execution seam scope — **[OPEN — product decision]**
The PRD approves **policy and denial** at the pre-execution seam. An earlier draft of this document
also listed **argument rewrite** (a hook mutating tool arguments before execution); that is a
capability the PRD has not approved and it has been removed from §1.4.

It is worth raising deliberately rather than dropping silently: pi's own ecosystem has the
equivalent (pigo's hook protocol exposes `updatedInput` on its pre-tool-use event), and for a
file-touching agent, rewriting a path is a genuinely different safety primitive from refusing a
call. But it widens the seam's blast radius — a rewriting hook can *cause* an effect, not just
prevent one. **Product decision, not architectural; defaults to "not included" until approved.**

### 6.4 Wire compatibility — **DECIDED: C (semantic equivalence only)**

**@qy-liang, 2026-08-15: option C.** pi-go's protocol is entirely its own. **Existing Pi clients
cannot connect to a pi-go server, and pi-go clients cannot connect to Pi.** No live adapter is built.

C does **not** reduce the work: pi-go must still implement a native protocol, client, and server.
What changes is only the **compatibility consequence** — that part is discharged by *documentation*
(a migration path and an explicit, recorded incompatibility boundary), **not by a bundled adapter or
gateway**. If a gateway is ever built it is a separate product decision, not an implied consequence
of choosing C.

**What this changes downstream:**
- The `wire schema` module is **free to be designed for Go** — it is no longer derived from Pi's
  schema, and Pi's protocol evolution no longer constrains us. This is the benefit purchased.
- **Native wire compatibility must never be claimed** in docs, README, or release notes.
- **RPC/protocol/client/server parity can no longer be demonstrated by interoperability.** Those
  rows need capability-based acceptance instead — "pi-go can do what Pi's protocol lets you do" —
  which is a weaker and more argument-prone form of evidence. Worth designing deliberately.
- **A13 now reads as**: verify pi-go's native semantic capability, and prove the incompatibility
  boundary — an unchanged Pi client is not presented as supported, and migration documentation does
  not imply a bundled live adapter. The boundary itself is the thing that must be provable.

Note: this aligns pi-go with `smallnest/pigo`, which also has no Pi wire compatibility (own
JSON-RPC + WebSocket). We now share that trade-off rather than differing from it.

The options as presented, retained for the record:

**Terminology.** The bar is **wire-compatible**, not "byte-identical": an unmodified Pi client must
complete framing and handshake, parse fields and errors, and observe the same commands and events.
Byte-for-byte output equality is only meaningful where Pi mandates canonical encoding — and
**verified at `086c32e`, it does not**: `packages/protocol/src/{cbor/*,codec.ts,framing.ts}` contain
no canonical / deterministic / key-ordering requirement, and framing is a 4-byte length header plus a
CBOR payload. CBOR maps are unordered, so byte equality is a stricter bar than Pi itself imposes.

Three options, not two:

| | Shape | Interop | Autonomy |
| --- | --- | --- | --- |
| **A. Native Pi wire** | Pi's schema *is* pi-go's public protocol | direct | public wire follows the pinned Pi baseline |
| **B. Own protocol + built-in Pi adapter** | adapter translates Pi frames ↔ internal commands/events, hosted **inside** the pi-go server | direct, via a compatibility endpoint — no separate gateway to deploy | runtime/session/domain model not shaped by Pi's wire |
| **C. Semantic equivalence only** | no live adapter | old clients cannot connect; offline migration or user-deployed gateway | highest |

B was recommended during review; **@qy-liang chose C**. A and B are retained above only as a record
of what was considered and rejected — **neither is a live option**, and the acceptance criteria they
would have required (golden frames, two-way interop tests, a lossless Pi↔pi-go mapping) do **not**
apply.

**One point from the B analysis still applies under C**, in altered form. The hardest thing to get
right was never fields — it was **event ordering**: a translation layer can map every field
perfectly and still break consumers by reordering, merging, or "helpfully" completing events.
Under C there is no Pi client to break, but the same trap moves to **capability comparison**: two
implementations can expose matching command lists and still differ in observable event sequence.
So the per-command-family parity checks must assert **event sequence identity** (C4/C4.0) and that
an aborted batch still exposes **unmatched tool calls** (C6) rather than a fabricated result —
otherwise "semantically equivalent" degrades into "has the same feature names".
