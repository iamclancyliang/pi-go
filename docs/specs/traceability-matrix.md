# Acceptance traceability matrix

**Status:** draft · task #7 · readiness gate #5
**Maps:** PRD acceptance scenarios A1–A16 → requirement → behaviour contract → issue → owner →
planned test → evidence/blocker.
**Sources:** `docs/product/PRD.md`, `docs/specs/behaviour-contracts.md` (C1–C8),
`docs/specs/session-compaction-recovery-contracts.md` (S1–S8), `docs/architecture/architecture.md`

> **Reading rule.** A row is only "covered" when it has a contract *and* an issue *and* a planned
> test. Rows failing any of those are listed in §2 as gaps — they are not quietly marked green.

## 1. Matrix

| A | Requirement | Contract | Issue | Owner | Planned test | Evidence / blocker |
| --- | --- | --- | --- | --- | --- | --- |
| **A1** two tools then answer; full trace | FR-1, FR-5 | C1, C4 | **#18** | @cc | `conformance/a1_trace_test.go` | ✅ **IMPLEMENTED AND PASSING.** Exact trace asserted (not a subsequence), tool events paired by `ToolCallID`, tool-result **content** asserted to reach the 2nd model call, session truth = 5 messages with 0 unmatched. Paired negative control: `TestA1NoToolsControl` |
| **A2** steering during active tool round | FR-2 | **C1** | **#7** | @cc | `conformance/a2_a3_steering_test.go` | ✅ **IMPLEMENTED AND PASSING.** All three C1b properties asserted separately. **Mutation-verified**: swapping `Steer`→`Follow` must turn it red (it did — the earlier draft stayed green, so it was not testing steering at all) |
| **A3** follow-up queued during run | FR-2 | **C1** | **#8** | @cc | `conformance/a2_a3_steering_test.go` | ✅ **IMPLEMENTED AND PASSING.** Also the negative control for A2's preempt: same timing, plain `Push`. Errored-turn case is covered separately by **A14** |
| **A4** truncated msg, 3 parseable calls | FR-3 | **C2** | **#9** | @cc | `conformance/a4_truncation_test.go` | ✅ **IMPLEMENTED AND PASSING.** A truncated message fails every call it carried without running any; all are still reported, so the model is not left waiting on calls it never hears about. **Mutation-verified** against the named trap: running the calls whose arguments still parse ran 2 of 3. Paired control on the identical calls, untruncated, proves the refusal is not the runtime simply never running tools |
| **A5** one sequential tool in a batch | FR-3 | **C3** | **#10** | @cc | `conformance/a5_serialisation_test.go` | ✅ **IMPLEMENTED AND PASSING.** A round containing such a tool runs one call at a time. Paired control: a registered-but-uncalled exclusive tool must NOT serialise the round — the registry-wide shape it replaced leaves every per-call assertion intact and changes only the interleaving |
| **A6** parallel slow-A / fast-B ordering | FR-3, FR-5 | **C4.1** | **#11** | @cc | `conformance/a6_ordering_test.go` | ✅ **IMPLEMENTED AND PASSING.** The calls finish in the opposite order to the request, so completion order and source order cannot coincide. Starts source-ordered, ends completion-ordered, results and history source-ordered, and no result rides on an end. C4.0 and C4b covered separately by **A15/A16** |
| **A7** one result requests terminate | FR-1 | **C5** | **#12** | @cc | `conformance/a7_terminate_test.go` | ✅ **IMPLEMENTED AND PASSING.** A result carries `Terminate` explicitly; the round stops only when every call asks. Cut before the next model call, so a stop means no further call rather than one more. **Mutation-verified** against the named trap: reading it as `any` lets a single call end the conversation. Both directions asserted, and both confirm the results survive in history — a stop that loses work is worse than not stopping |
| **A8** cancel after tool call emitted | FR-3, FR-6 | **C6** | **#13** | @cc | `conformance/a8_unmatched_test.go` | ✅ **IMPLEMENTED AND PASSING.** An abort is not a tool failure: a cut call produces no result, so the assistant's message keeps call ids with nothing matching them. **Mutation-verified**: treating cancellation as an ordinary failure invents a result and leaves nothing unmatched. Covers BOTH execution shapes: the sequential round hands the turn between calls, so a cut leaves a call waiting for a hand-off that never comes — the run hangs rather than fails, which the parallel case cannot detect. Also asserts a cut call does not EXECUTE, using a tool that ignores cancellation: the stream reports nothing for a cut call either way, so only the tool's own record distinguishes prevented from unreported. Paired control confirms a completed round leaves none |
| **A9** next-turn hook changes model | FR-4 | **C8** | **#17** (spike #4 closed) | @cc | `conformance/a9_model_swap_test.go` | ✅ **IMPLEMENTED AND PASSING.** A per-turn hook selects the model for the turns that follow; the turn that chose it keeps the model it ran with, including its post-tool request. The change is announced as a `model_changed` event, because the framework emits none and a mid-run switch would otherwise be invisible. **Mutation-verified**: capturing the model name once instead of asking per call leaves the change applying to nothing. Control asserts no hook means no event and no change |
| **A10** context overflow twice | FR-6 | **S1, S2, S6** | **#22** | @cc | `conformance/a10_overflow_test.go` | ✅ **IMPLEMENTED AND PASSING.** One shortening and one further attempt per input boundary (S1); a second refusal is a durable terminal failure with a code a caller can branch on, and `Reopen` returns it without calling the model or the compactor (S2); the refused attempt stays durable, carries what it cost, and never enters the retry projection (S6). **Mutation-verified**: no budget recurses without end; an empty answer reports a run that answered nothing as success; ignoring the recorded failure loses it across a restart |
| **A11** death after destructive intent, pre-settlement | FR-6, NFR-6 | **S7, S8** — **class N**, see **G6** | **#23** (durable record), **#24** (recovery decision) | @cc | `conformance/a11_settlement_test.go` | ✅ **IMPLEMENTED AND PASSING.** The attempt is durable before the call can take effect and is settled with its result in one write; attempts are paired by the slot they reserved, not by the call id the model chose. Recovery settles what may not be repeated as an unknown outcome and presents what may, without running it. ⚠️ **class N — pi-go net-new requirement, no released Pi counterpart.** Verified at the pinned baseline: the current coding-agent has **no** durable tool-intent/settlement mechanism. "Don't blindly replay a destructive tool" comes from AgentHarness's newer durable design and becomes a **pi-go v1 safety policy**. There is no pi behaviour to conform to — do not look for one |
| **A12** compaction performed | FR-6 | **S3, S4, S5** | **#21** | @cc | `conformance/a12_compaction_test.go` | ✅ **IMPLEMENTED AND PASSING.** History and projection are different data, and a compacted projection reports itself lossy (S3); the checkpoint carries its own summary and retained tail (S4); publication is atomic as seen by whoever reopens — a crash either side gives the complete old context or the summary with its complete tail, never a state between (S5) |
| **A13** Pi client on compatibility surface | FR-8, NFR-4 | **wire-compatibility spec gap (G5)** + C4/C4.0, C6 reused | **placeholder — v3** | @cc | `interop/a13_pi_client_test.go` | **blocked-on-product-decision**: wire A/B/C (architecture §6.4). Do **not** assume a live adapter. The reused loop contracts cover ordering only — **framing, hello/version, schema and error mapping have no contract at all** |

### 1.1 Added after review (A14–A16)

Accepted into the PRD because A1–A13 permitted an implementation to violate C7, C4.0 and C4b while
passing everything. FR-1 was also amended to state that an errored/aborted turn does not consume
follow-up.

| A | Requirement | Contract | Issue | Owner | Planned test | Evidence / blocker |
| --- | --- | --- | --- | --- | --- | --- |
| **A14** follow-up queued during a turn that errors → not consumed; agent stops | FR-1, FR-2 | **C7** | **#14** | @cc | `conformance/a14_error_followup_test.go` | ✅ **IMPLEMENTED AND PASSING.** The test was written first and failed on three counts: the run reported success, the queued follow-up WAS consumed, and a second turn started. A turn's events are now inspected for failure instead of drained, and a failed turn ends the agent without looking behind it |
| **A15** same two tools run sequential vs parallel → interleavings differ as specified | FR-3, FR-5 | **C4.0** | **#15** | @cc | `conformance/a15_interleaving_test.go` | ✅ **IMPLEMENTED AND PASSING.** Same names, arguments, delays and order; only one declaration differs. **Mutation-verified**: collapsing both modes onto one path turns it red — the case a single `parallel bool` path breaks |
| **A16** invalid args on call A → `endA` precedes `startB` | FR-3, FR-5 | **C4b** | **#16** | @cc | `conformance/a16_immediate_fail_test.go` | ✅ **IMPLEMENTED AND PASSING.** A call refused before execution ends inline, while the round is still being announced; only the end moves, results stay source-ordered. Also asserts the refusal reaches the model as a result and that the refused tool never ran |

## 2. Gaps

### G5 — wire contract — **decision landed: option C**
@qy-liang chose **C (semantic equivalence only, no live adapter)** on 2026-08-15. The gap changes
shape rather than closing:

- **No longer needed:** a Pi-compatible framing/hello/schema/error contract. We are not
  interoperating, so there is nothing of Pi's to conform to.
- **Now needed:** pi-go's *own* wire contract, freely designed. Still unwritten — so this remains a
  gap, just a different one. **Owner @cc**, alongside the `wire schema` module (v3).
- **A13 is void as written** (conditional on live compatibility). @gpt-codex is rewriting it as
  **semantic equivalence + an explicit incompatibility boundary + migration acceptance** — better
  than retiring it, since the boundary itself is what now needs to be provable.
- **Consequence to design for:** RPC/protocol/client/server parity can no longer be evidenced by
  interop testing. It needs capability-based acceptance, which is weaker evidence and more open to
  argument — so those acceptance criteria deserve more care, not less.

### G1 — C7 has no acceptance scenario — **CLOSED**
Resolved by **A14** above; FR-1 amended. Retained here for the reasoning: A3 tested follow-up
consumption on normal completion only, so an implementation draining the queue on error passed every
scenario while breaking a real contract.
**C7**: an errored or aborted turn emits `turn_end` with empty `toolResults`, then `agent_end`, then
returns — **skipping the follow-up check**. A3 tests that follow-up is consumed when a run ends
normally; nothing tests that it is *not* consumed when the turn errored.


### G2 — A6 under-covers C4 — **CLOSED**
Resolved by **A15** (C4.0) and **A16** (C4b) above. Reasoning retained:
A6 covers **C4.1** (parallel orderings) only. Two verified behaviours have no scenario:
- **C4.0** — sequential and parallel emit *differently interleaved* streams (sequential puts
  `resultA` before `startB`; parallel puts every start before any result). This is the behaviour a
  single `parallel bool` code path silently breaks.
- **C4b** — a call failing *preparation* emits its `end` inline, so `startA → endA → startB` is legal
  and "all starts precede all ends" is false.


### G3 — A10–A12 have no contracts — **CLOSED**
Resolved by `docs/specs/session-compaction-recovery-contracts.md` (S1–S8, @gpt-codex). A10→S1/S2/S6,
A11→S7/S8 (class N), A12→S3/S4/S5.

Reasoning retained: these were never loop contracts and correctly were not forced onto C1–C8. They
needed their own spec — overflow/retry policy, durable settlement of destructive intent, and the
truth-vs-projection boundary.

**Note carried from the spec's own review:** a compaction checkpoint publishes summary + retained
tail + leaf atomically, **but summarization usage may already have been durably recorded before
that point**. Do not assert "zero cost before checkpoint"; recovery must preserve that usage without
double-counting.

### G6 — A11 is a new requirement, not a parity requirement
Every other row in this matrix traces to a pi behaviour we must conform to. **A11 does not.**
Verified at the pinned baseline: the current coding-agent has no durable tool-intent/settlement
mechanism, so "a destructive tool is not blindly replayed" is a **pi-go v1 safety policy** derived
from AgentHarness's newer durable design — not an existing pi behaviour.

**Why this needs its own marker:** a reader who assumes uniform provenance will go looking for pi
source to conform to, find none, and either fabricate a contract or mark the row unverifiable. The
correct handling is that A11 is *specified*, not *discovered* — its authority is our own product
decision, and it needs an explicit rationale rather than a source citation.

**Resolved — class N added.** Definition (@gpt-codex): **N = pi-go net-new requirement, with no
released Pi counterpart.** Important constraints on it:
- N is **not** parity evidence and never substitutes for B/W/R/I — it is an *additional* product
  gate in the same release.
- Every N row must trace to a PRD/ADR, carry a written rationale, and own its acceptance.
- **NAS-downstream requirements stay in the NAS product's own matrix** unless formally promoted to a
  pi-go contract. "We might reuse it" is not grounds for pre-loading pi-go.

A11 is the first N row.

### G4 — issue coverage — **closed 2026-08-19 except A13**
Every scenario but A13 is ticketed. The mapping, stated here once — the Issue column of the matrix in
§1 is the same set:

| A | Issue |
| --- | --- |
| A1 | **#18** |
| A2–A8, A14–A16 | **#7–#16** |
| A9 | **#17** (spike #4 closed first) |
| A10 | **#22** |
| A11 | **#23** the durable record, **#24** the recovery decision |
| A12 | **#21** |
| A13 | none — v3, blocked on the wire decision |

A11 has two tickets because it has two separable halves: what is recorded before a call takes effect
and how a restart resolves what it finds. The second only became answerable once the first existed, so
splitting it kept the decision about repeating from being buried in the record's design.

**A11 is covered end to end.** Attempts are recorded before a call can take effect and settled with
its result. On recovery, a call that may not be repeated is settled as an unknown outcome without
being asked about — no answer would let it run again. A call whose tool declared a repeat harmless is
**presented and left alone**: repeating it or abandoning it are both answers, and neither is chosen
on the caller's behalf. A run cannot start while an answer is owed, because the conversation holds a
tool call with no result.

Asking rather than repeating automatically is the owner's decision, and the reason is asymmetry: "did
not do it" is visible and can be retried, while "did it twice" may already have changed the user's
files and cannot be seen or undone. Repeating automatically bets that every tool author marked the
declaration correctly.

**What is covered is the contract, not a human being asked.** The question is presented by the
library; no command restores a session, so nothing reaches a person today. `cmd/pi-tracer` keeps its
session in memory and never restores, which is deliberate — it is a contract probe with a scripted
model. A host that recovers a durable session and collects the answer is not built, by decision.

> **An issue is still not coverage** — but the reason has changed. When these rows were first
> ticketed there was no product code to test; now there is, and every row above except A13 has a
> passing conformance test. **Read the Evidence column, not the ticket**: a ticket says the work was
> tracked, and only the test says what is asserted.

## 3. Coverage summary

| | Count |
| --- | --- |
| Total scenarios | **16** (A1–A16) |
| Scenarios with a verified contract | **15** (A1–A12, A14–A16; A1 partial) |
| Scenarios with **partial** contract cover | 1 (A13 — ordering reused, wire surface uncovered) |
| Scenarios with **no** contract | **0** — G3 closed by S1–S8 |
| Scenarios that are **class N** (net-new, no Pi counterpart) | **1** (A11 — see G6) |
| Scenarios blocked on a spike | **0** — A9 unblocked by spike #4 (ADR-0002 accepted) |
| Scenarios blocked on a product decision | 1 (A13) |
| Contracts with **no** scenario | **0** — C7/C4.0/C4b closed by A14/A15/A16 |
| Surfaces with no contract at all | **1** — wire compatibility (G5) |
| Scenarios with an issue | **15** (A1–A9, A14–A16 → #7–#18; A10, A11, A12 → #22, #23 + #24, #21) |
| Scenarios still without an issue | 1 (A13 — v3, blocked on the wire decision, see §2) |
| Scenarios with a **passing** test | **15** (A1–A12, A14–A16) — A13 is the exception |

**Every scenario except A13 now has a passing conformance test**, and A13 is blocked on the wire
decision rather than on implementation. What that does and does not mean:

- A passing test is evidence about the contract it asserts, not about the scenario as a whole. Where a
  row's contract is partial — A1 — the test is partial with it.
- The spike suite proves things about *eino*, not about pi-go. **Do not read green gates as scenario
  coverage.**
- The intermittent failure in `TestC1bSteeringContract` (and its streaming twin, which shares the
  probe) is **fixed**. `TurnLoop.Push` returns a channel that closes when the preempt REQUEST is
  resolved, and resolution includes a no-op — the request can finish having submitted no cancel at
  all. The probe waited on the preempt itself, which never closes in that case; the two-second
  deadline it waited under turned that into a failure report that named machine load rather than the
  mechanism. It now waits on the request's own resolution and then observes, so "contributed" and
  "resolved without contributing" are separate recorded outcomes and neither is a guess about timing.
- One intermittent failure remains: `TestSpike3ArmCTargetedGap`, seen 3 times in 240 executions, every
  one reporting `ExitReason = nil`.
  **Established**: `Stop(WithGraceful())` is a request to cancel at the next chat or tool safe point,
  not a guarantee that a cancel happens; `CancelHandle.Wait` names `ErrExecutionEnded` for a run that
  ended before the cancel took effect; and `IsRootCause` marks a business interrupt, which a graceful
  stop does not itself produce.
  **Observed**: in every failing run the round completed its remaining model call and finished, so
  there was nothing left to cancel.
  **Not yet corrected.** The probe asserts a root-cause interrupt context unconditionally, which
  assumes the stop always wins that race. The correction is to make the precondition deterministic —
  run 1 must still have unfinished work after the safe point — and to require proof that the cancel
  happened and the checkpoint exists before judging targeted resume; a run that fails those is an
  invalid scenario and cannot support any conclusion about resume.
