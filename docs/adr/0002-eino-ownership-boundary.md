# ADR-0002: Build the agent loop on eino's prebuilt TurnLoop

**Status:** proposed (drafted by task #7 from spike evidence; awaiting review)
**On acceptance:** `architecture.md` §0, §4 and the ADR register must be updated **atomically** —
until then §4 stays `[OPEN]` and the eino edge register is unchanged.
**Date:** 2026-08-15
**Related:** `docs/architecture/architecture.md` §4, `docs/specs/eino-verification-plan.md`,
issues #3 · #4 · #5 · #6 (all closed) · **Supersedes nothing**
**Baselines:** Pi `086c32e` · eino `v0.9.14` (`32759e6…`)

## Context

The architecture deliberately left one question open (§4): does pi-go build its agent loop on eino's
prebuilt orchestration, or own the loop and use eino as a component library?

This was gated on evidence rather than preference, because it is hard to reverse — it determines what
the runtime core imports, what a rewrite of the loop would cost, and how much of pi's behaviour we
must re-implement versus configure. Three spikes were run against a pinned eino, each with a
pre-registered interpretation table and failable assertions; negative controls where noted below.

## Decision

**Build the runtime core on eino's prebuilt `adk.TurnLoop`, with pi-go owning session truth, the
model port, and the observable event contract.**

Concretely:
- **Loop orchestration:** eino `TurnLoop` (`GenInput` / `PrepareAgent` / `OnAgentEvents`).
- **Per-call model and reasoning-level control:** eino `ChatModelAgentMiddleware.WrapModel`.
- **Steering:** `Push(msg, WithPreempt(AfterToolCalls))` **plus pi-go session-truth reconstruction**.
- **Follow-up:** plain `Push`, consumed at the next `GenInput` iteration.
- **Checkpoint resume:** retained as a **recovery** mechanism. **Not** the routine steering path.
- **pi-go owns:** session truth and reconstruction, the model port (ADR-0001 forbids re-exporting
  eino types), and events eino does not emit — notably `model_changed`.

## Evidence

Every claim below is a **direct observation with a failable assertion**, not a reading of
documentation.

**Negative controls** — a paired run where the mechanism is removed and the effect must disappear —
exist for exactly **three** claims:

| Claim | Control | Asserts |
| --- | --- | --- |
| per-call option injection | `TestSpike4NoWrapModelControl` | no handler registered ⇒ model observes `observedTemperature=nil`, `WrapModel:fired`=0 |
| preempt signal | `TestC1aFollowUpContract` | plain `Push` (no `WithPreempt`) ⇒ `Preempted` never closes |
| handler reachability | `TestWrapModelCompositionOrder` (2 arms) | moving the injector inside the substituter ⇒ its `WrapModel` never fires and its option disappears |

**Every other claim rests on a positive observation alone.** Specifically: instance substitution,
reasoning-level control, the streaming path (a second positive arm of C1b, *not* a control),
resume injection, and checkpoint cleanup. These are directly asserted and failable, but nothing
proves the observed effect would vanish if the mechanism were removed.

| Contract | Result | Mechanism |
| --- | --- | --- |
| **C8** per-turn model + reasoning-level change | **PASS** | `WrapModel` fires per model call; instance substitution, common params, and provider-specific reasoning level all verified separately |
| **C1a** follow-up | **PASS** | plain `Push` buffered to the next `GenInput`; in-flight turn untouched |
| **C1b** steering | **PASS** | safe-point truncation + pi-go reconstruction |
| **Handler composition** | **ORDER-SENSITIVE** | lazy, outermost-first; a substituting handler prevents inner handlers from running at all |
| **Tool replay on resume** | **SAFE** | tool invoked exactly once across a checkpoint resume; settled result published exactly once |

**Nesting (N2):** one `PrepareAgent` instance wraps the whole model→tool→model cycle, so
`PrepareAgent` is *not* a per-model-call hook. This falsified the initial hypothesis that
`PrepareAgent ≈ pi's prepareNextTurn`; `WrapModel` is the correct landing point.

## Consequences

**Positive**
- pi-go does not re-implement loop orchestration, cancellation, safe points, or checkpointing.
- ADR-0001's module boundary keeps this reversible: the runtime core reaches the model layer through
  a pi-go-owned port, and eino types are never re-exported. If this ADR is revisited, no module moves.

**Negative — accepted deliberately**
- **Steering is behavioural reconstruction, not native injection.** eino truncates at the safe point
  and starts a new execution; continuity is ours. Verified by contrast: without pi-go session truth
  the same preempt yields `inputs=2 roles=[system user]` — the context is gone.
- **pi-go must emit `model_changed` itself.** eino executes the control plane per call but does not
  interpret it; no event is emitted. Without our own event, a mid-turn model change is invisible to
  RPC clients and session records.
- **A substituting `WrapModel` handler voids every handler registered after it.** Now measured
  (`TestWrapModelCompositionOrder`), and worse than first stated: composition is **lazy** and
  **registration order is outermost-first**, so an outer handler that returns a fresh model never
  calls through — the inner handler's `WrapModel` is **never invoked at all**, not merely discarded.
  pi's per-turn model selection *is* such a substitution. **pi-go must therefore own and pin handler
  order**, and register model selection **last (innermost)**, or anything registered after it stops
  running with no error and no trace.
- **Checkpoint resume carries a v0.9.14 capability gap.** Targeted `ChatModelAgentResumeData` is not
  delivered on the cancel-resume path; only the deprecated, non-target-scoped `WithHistoryModifier`
  works. This is why resume is scoped to recovery rather than steering.
- **Checkpoint cleanup requires `WithSkipCheckpoint`** — it is not automatic.

## Alternatives considered

- **Own the loop, use eino as a component library.** Rejected: every capability that would have
  justified it (C8, steering) is achievable on the prebuilt loop, so the cost of re-implementing
  orchestration would buy nothing currently identifiable.
- **Checkpoint resume as the steering mechanism (arm C).** Rejected as the *routine* path: it is
  safe and context-continuous, but injection depends on a deprecated, non-target-scoped API because
  the supported targeted mechanism is broken at this baseline. Retained for recovery.
- **A pi-go dispatcher behind one eino Model for per-call model swapping.** Demoted to fallback:
  it works, but `WrapModel` is eino's own per-call hook and keeps the swap inside the framework's
  middleware chain.

## Open

- **Re-evaluate if eino fixes targeted resume data.** `TestSpike3ArmCTargetedGap` asserts current
  behaviour and **will fail by design** when that changes — the trigger to revisit arm C.
- ~~**Multi-handler composition** around `WrapModel` needs its own test (v0 wiring acceptance).~~
  **CLOSED** by `TestWrapModelCompositionOrder` (both registration orders, pre-registered as M1-M4).
  Outcome moved into Consequences above. The remaining v0 work is not a spike but a rule: the
  handler-order policy must be enforced where handlers are registered, not left to convention.
- Probe assertions currently match formatted trace strings. If these become long-lived contract
  tests, move to structured fields so a formatting change cannot silently weaken them.
