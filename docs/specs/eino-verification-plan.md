# eino capability verification plan

**Status:** draft · task #7 deliverable 2
**Pinned baseline:** cloudwego/eino **v0.9.14**, tag commit
`32759e61861d8f3773b9a21d2a7db2e60c58354e` (released 2026-08-13). Deliberately **not** the v0.10
alpha line.
**Companion:** `behaviour-contracts.md` (the pi semantics being matched).

## Posture

eino **has** candidate mechanisms for the hard parts. It is **not** established that they are
semantically equivalent to pi's. This plan exists to convert "the mechanism exists" into "the
mechanism reproduces the contract, and here is the trace that proves it."

Judge every spike on **observable results** — context contents, event order, checkpoint state —
never on API naming.

## Verified API surface (read from the pinned tag)

From `adk/turn_loop.go` (2056 lines), all exported:

```go
func (l *TurnLoop[T, M]) Run(ctx context.Context)
func (l *TurnLoop[T, M]) Push(item T, opts ...PushOption[T, M]) (bool, <-chan struct{})
func (l *TurnLoop[T, M]) Stop(opts ...StopOption)

func WithPreempt[T, M](safePoint SafePoint) PushOption[T, M]
func WithPreemptTimeout[T, M](safePoint SafePoint, timeout time.Duration) PushOption[T, M]

type SafePoint int
const (
    AfterChatModel SafePoint = 1 << iota  // finish the current chat-model call first
    AfterToolCalls                        // finish the current tool-call round first
    AnySafePoint = AfterChatModel | AfterToolCalls
)
```

Plus `StopOption`s (`WithGraceful`, `WithImmediate`, `WithGracefulTimeout`, `WithSkipCheckpoint`,
`WithStopCause`, `UntilIdleFor`), `InterruptError`, `TurnLoopExitState`, `TurnContext`, and
checkpoint/resume types. Sibling files at the same tag: `runner.go`, `interrupt.go`, `cancel.go`,
`turn_buffer.go`.

**Baseline provenance.** The signatures above were first read at v0.9.13 (`c5e6aef…`) and re-pinned
to v0.9.14. Verified that this is safe: `v0.9.13...v0.9.14` is 2 commits, and the only files
changed under `adk/` are `middlewares/patchtoolcalls/{patchtoolcalls.go,patchtoolcalls_test.go}`
and `middlewares/summarization/summarization.go`. **`turn_loop.go`, `runner.go`, `cancel.go` and
`react.go` are untouched**, so every signature above still holds verbatim.

Worth noting for later: both files that *did* change are relevant to work ahead —
`summarization` borders on pi's compaction, and `patchtoolcalls` borders on malformed-tool-call
handling (contract C2). Re-read them when those contracts come up.

---

## Spike 1 — per-turn model / thinking-level swap

**Contract under test:** C8 — `prepareNextTurn` may replace model and thinking level between turns
while the conversation continues.

**Question.** Can a running eino loop change model and reasoning level for turn N+1 without
restarting the session?

**Pass criteria (all required).**
1. The second model call demonstrably uses the new configuration.
2. Message context is continuous across the switch — no truncation, no restart.
3. Event order is unchanged from the single-model baseline.

**Risk.** Graph-style orchestration tends toward binding a model at composition time. If the swap
requires rebuilding the graph, check whether rebuilding preserves 2 and 3.

---

## Spike 2 — steering (inject into a running turn)

**Contract under test:** C1 — steering messages join the context *before the next model request*,
and are distinct from follow-up.

**Question.** Does `Push(item, WithPreempt(safePoint))` reproduce pi's steering?

**Pass criteria (all required).**
1. The pushed message enters context only **after a safe point and before the next model call**.
2. It is **not** treated as a new, independent session.
3. Already-completed tool results are **not** discarded.

**The real question — do the landing points coincide?** pi injects at the top of its inner loop,
after tool results have been appended. eino preempts at `AfterChatModel` or `AfterToolCalls`.
`AfterToolCalls` is the closest analogue and is the one to test first. Equivalence here is the
crux; API presence alone proves nothing.

---

## Spike 3 — preempt is not the same thing as steering

**Purpose.** Guard against mistaking cancel-and-resume for injection.

**Method.** Take one identical input and run it two ways:
- **(a)** safe-point cancel, then resume from checkpoint
- **(b)** non-cancelling injection into the next turn

**Compare.** Checkpoint state, event sequence, and resulting context. Record where they diverge.

**Why it matters.** If (a) is the only mechanism that works, pi's steering must be rebuilt on top
of cancel/resume — a materially larger and more failure-prone job than "call Push". That
difference belongs in ADR-0002's cost estimate.

---

## What each outcome means for ADR-0002

| Outcome | Consequence |
| --- | --- |
| Spikes 1 & 2 both pass | Build on eino's prebuilt agent loop; write pi's contracts as tests over it |
| Spike 2 fails, 1 passes | Custom Graph orchestration for the loop; keep eino components (ChatModel, ToolsNode) |
| Spike 3 shows only cancel/resume works | Steering costs materially more — surface it in the estimate before committing |
| Both fail | Own the loop entirely; use eino as a component library, not a framework |

## Out of scope here
Provider catalogue, subscription/plan semantics, usage-cost accounting — eino's `ChatModel` covers
the component interface only, and these sit outside it. They need their own capability matrix
(pi carries them in `pi-ai`; pigo rebuilt them rather than inheriting them).
