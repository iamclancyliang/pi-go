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

## Spike 1 — per-turn model / thinking-level swap — **RESOLVED: PASS (2026-08-15)**

> **Outcome.** C8 is achievable on eino's prebuilt loop via `adk.ChatModelAgentMiddleware.WrapModel`,
> which fires **once per model call** (twice inside one `PrepareAgent` instance, bracketing the tool).
> Directly observed with failable assertions and negative controls: **instance substitution**
> (modelA→modelB), **common params** (temperature 1.0→2.0 via `GetCommonOptions`), **provider-specific
> reasoning level** (low→high via `GetImplSpecificOptions` *only*, so the common path cannot produce a
> false pass), **context continuity** (2→4 messages incl. assistant+tool), **unchanged event order**.
>
> **No custom loop and no dispatcher required** — the dispatcher is demoted to a fallback.
>
> **Two things this does NOT settle, recorded side by side with the mechanism fact:**
> 1. **eino executes the control plane; it does not interpret it.** No `model_changed` event is
>    emitted. That event contract is **pi-go-owned and mandatory** — otherwise a mid-turn model change
>    is invisible to RPC clients and session records.
> 2. **Composition constraint (v0 wiring acceptance):** returning a brand-new model from `WrapModel`
>    **skips the inner endpoint it discards**. The model-selection handler needs explicit
>    ordering/ownership so it cannot silently bypass other user middleware; eino's default event
>    sender/retry still wrap it externally. Multi-handler composition needs its own test.
>
> **Nesting baseline established separately: N2** — **one `PrepareAgent` instance wraps the whole
> model→tool→model cycle**, so `PrepareAgent` is *not* the C8 landing point. (Phrased by observable
> layer deliberately: the pre-registered table never defined "eino's turn", and using that word here
> would imply a term equivalence we never established.) My prior hypothesis that
> `PrepareAgent ≈ prepareNextTurn` was **falsified** by the pre-registered table.

### Original plan (retained)

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

## Spike 2 — steering — **RESOLVED: PASS (2026-08-15)**

> **Outcome — and the distinction matters more than the verdict.**
>
> **C1a (follow-up):** a plain `Push` during a turn is buffered and consumed at the **next**
> `GenInput` iteration. The in-flight turn completes untouched — call#2 delivers `userContents=[hello]`,
> and only call#3 carries `INJECTED`. `GenInput` covers follow-up.
>
> **C1b (steering): PASS — but by behavioural reconstruction, NOT by injection into a running run.**
> `Push(msg, WithPreempt(AfterToolCalls))` makes eino **truncate the turn at the safe point and start
> a new TurnLoop execution** — the preempted turn makes exactly one model call. Continuity is then
> supplied by **pi-go-owned session truth**: messages materialised from real events
> (`MessageOutput.GetMessage()`, covering streaming and non-streaming) and reconstructed into the next
> input. Observed at the model: `roles=[system user assistant tool user]`,
> `userContents=[hello INJECTED]` exactly once, and the tool pairing intact
> (`assistantToolCallID=tc1` / `toolMsgToolCallID=tc1` / `content="tool-result"`).
>
> **State it precisely:** eino supplies the safe-point cut and the next execution; **pi-go supplies
> the continuity**. This is *behaviourally equivalent reconstruction*, **not** injection into the same
> agent run. Proven by contrast — before pi-go maintained session truth, the identical preempt
> produced `inputs=2 roles=[system user]`: the context was gone. The only variable was who owns history.
>
> **Covered on both paths.** The streaming variant is asserted to actually use `model:Stream` (and to
> use `Generate` zero times), because pi's production path is streaming — verifying only the
> non-streaming path would test a route pi never takes.
>
> Regression gates: `TestC1aFollowUpContract`, `TestC1bSteeringContract`,
> `TestC1bSteeringContractStreaming` — each negative-controlled.

### Original plan (retained)

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

## Spike 3 — checkpoint resume vs safe-point reconstruction — **RESOLVED 2026-08-15**

> **Arm C splits in two, and the split is the result.**
>
> | | Status |
> | --- | --- |
> | **C-runopt** — inject via `GenResumeResult.RunOpts` + `adk.WithHistoryModifier` | **PASS**, but the API is **deprecated** and **not target-scoped** (applies to the whole resumed run) |
> | **C-targeted** — inject via `ResumeParams.Targets` + `ChatModelAgentResumeData` | **Does not deliver** on this path — capability gap in v0.9.14 |
>
> **Safety gate: PASSED.** Checkpoint resume does **not** replay the tool and does **not** re-publish
> a settled result. Resume lands on the model call *after* the settled tool result
> (`inputs=4 roles=[system user assistant tool]`, `tc1` pairing intact), tool invoked once across the
> whole lifecycle.
>
> ⚠️ **An earlier "safety FAILED" claim was wrong and is retracted.** It was a harness artefact: run 2
> built a fresh scripted model that re-issued its canned `tc1` call. Caught by @gpt-codex. The probe
> now uses a history-aware model that answers `final` once it sees a settled `tc1`.
>
> **Cleanup works but is REQUIRED WIRING, not automatic.** `Stop(UntilIdleFor(...), WithSkipCheckpoint())`
> produces `store:Delete` and a genuinely absent checkpoint. Without `WithSkipCheckpoint` the teardown
> writes another checkpoint instead. Asserted on both *Delete called* and *checkpoint absent* — a
> no-op delete would satisfy the first alone.
>
> **Attribution boundary.** This repository proves the gap on the **TurnLoop cancel-resume path**.
> @gpt-codex independently reproduced zero-delivery against `Runner.Run + WithCancel` →
> `ResumeWithParams` with both `InterruptCtx.ID` and `Address` keys, indicating the cancel-resume path
> itself rather than the TurnLoop bridge — cited as **external verification**, since that experiment
> is not reproducible in this repo.
>
> **Cost comparison for ADR-0002 — the point of the spike:**
> - **B** (`WithPreempt` + pi-go session truth): non-deprecated interfaces, precise injection point;
>   cost is that we own history maintenance.
> - **C** (checkpoint resume): safe, context-continuous, cleanup controllable — but injection today
>   depends on a **deprecated, non-target-scoped** API, because the supported targeted path is broken
>   in v0.9.14.
>
> That is a factual difference in what each arm must rely on, not a preference.
>
> Gates: `TestSpike3ArmCRunOpt`, `TestSpike3ArmCTargetedGap` — both negative-controlled.
> The targeted test asserts *current* behaviour: **if eino fixes it, that test fails by design**, so
> the gap gets re-evaluated rather than silently outliving its cause.

### Re-scoping rationale (retained)

> **Why the original comparison is void.** It pitted "safe-point cancel + resume" against
> "non-cancelling injection". **C1a has since proven plain `Push` is follow-up, not steering** — so
> that second arm was never a steering mechanism. Comparing against it would have re-packaged
> follow-up as steering and produced a meaningless choice.
>
> **The real comparison, both arms genuinely viable:**
>
> | Arm | Mechanism |
> | --- | --- |
> | **B (baseline, PASSED as spike #2)** | `WithPreempt(AfterToolCalls)` + pi-go session-truth reconstruction |
> | **C** ~~(under test)~~ — **SUPERSEDED, see the RESOLVED block above** | `Stop(WithGraceful)` → checkpoint → real `GenResume` / Runner resume, with `INJECTED` supplied through resume data |
>
> **Source-level prediction to respect:** TurnLoop's **preempt branch returns nil and does not run
> `finalizeCheckpoint()`** — checkpoint saving lives on the Stop / business-interrupt path. So pairing
> `WithPreempt` with a `Store` and expecting an automatic checkpoint would **test the wrong
> mechanism**. C must be driven by `Stop(WithGraceful)`.
>
> **Observation order for C (observe first, assert after):**
> 1. First `Wait`: is `ExitReason` a `*CancelError`? `CheckpointAttempted=true`, no `CheckpointErr`,
>    `InterruptContexts` non-empty. **Record the real contexts — do not pre-guess a target ID.**
> 2. Second loop/`Run` on the same `Store` + `CheckpointID`: confirm it actually enters **`GenResume`,
>    not `GenInput`**, and inject `INJECTED` via `ResumeParams.Targets` +
>    `ChatModelAgentResumeData.HistoryModifier`.
> 3. Compare B vs C on: next model input · tool result `tc1` survival · **whether the tool
>    re-executes** · event re-emission/duplication · model-call position · whether the checkpoint is
>    finally cleaned up · required pi-go state and wiring complexity.
>
> **Hard gate:** if safe-point checkpoint resume **re-runs the tool**, C is disqualified as a steering
> implementation — replaying a destructive tool is exactly what contract C6 and scenario A11 forbid.

### Original plan (retained for audit)

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
