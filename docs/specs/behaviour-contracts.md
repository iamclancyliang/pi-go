# pi behaviour contracts — the observable semantics pi-go must preserve

**Status:** draft · task #7 deliverable 1
**Source of truth:** `earendil-works/pi` at candidate baseline `086c32e`, chiefly
`packages/agent/src/agent-loop.ts`. Line numbers below refer to that commit.

> **Re-pin provenance.** These contracts were originally read at `534bcbffb`. Re-pinned to the PRD's
> candidate baseline `086c32e` (2026-08-15) after verifying it is safe: the range is 34 commits, and
> **`agent-loop.ts` is not among the changed files** — so every line reference below still resolves
> identically. Re-verify this claim if the baseline moves again; the line numbers are only as good
> as that check.

## Why this document exists

The rewrite is not a file-by-file translation. What must survive the move to Go/eino is pi's
**observable behaviour** — what an outside observer (a user, a client over RPC, a test) can see.
Implementation structure is free to change; the behaviours below are not.

Each contract states the rule, the evidence, why it matters, and the trap a Go implementation is
likely to fall into. Every contract should end up with an executable conformance test in pi-go.

> Contracts marked **[unverified against eino]** have a corresponding entry in
> `docs/specs/eino-verification-plan.md`. Do not assume eino provides them.

---

## C1 — Two loops, and steering ≠ follow-up

**Rule.** The agent runs an inner loop and an outer loop.
- **Inner loop** continues while there are tool calls to run *or* pending messages to inject.
  Pending ("steering") messages are appended to the context **before the next model request**.
- **Outer loop** exists only for **follow-up**: when the agent would otherwise stop, it asks for
  follow-up messages; if any exist they become pending and the inner loop resumes.

**Evidence.** `runLoop`, agent-loop.ts:170–272. Steering is polled at :167 and :259; follow-up at
:263.

**Why it matters.** These are two different user-facing capabilities: correcting an agent
mid-flight, versus queueing the next task while it finishes. Collapsing them into one queue loses
the distinction.

**Go trap.** Modelling both as "a message channel" makes follow-up messages interrupt a running
turn, which pi never does.

---

## C2 — A truncated assistant message executes **zero** tool calls

**Rule.** If `stopReason === "length"`, every tool call in that message is failed without
execution — not the parseable ones, not a subset. All of them.

**Evidence.** agent-loop.ts:211–214, dispatching to `failToolCallsFromTruncatedMessage`.

**Why it matters.** Truncation can silently cut tool arguments mid-JSON. A truncated `write` or
`bash` that still happens to parse is the dangerous case. This is a correctness decision, not
error handling — and it matters more, not less, for an agent that touches user files.

**Go trap.** Validating arguments per-call and executing the ones that unmarshal cleanly. That
inverts the contract.

---

## C3 — Parallel by default; any one sequential tool serialises the whole batch

**Rule.** A batch runs in parallel unless the loop is configured sequential **or any single tool in
the batch declares `executionMode: "sequential"`**, in which case the entire batch runs
sequentially.

**Evidence.** `executeToolCalls`, agent-loop.ts:418–426.

**Why it matters.** Conservative by design: one tool needing isolation is enough to stop
concurrency for everything alongside it.

**Go trap.** Serialising only the sequential tool while the rest run concurrently.

---

## C4 — Three different orderings, one per event kind

This is the subtlest contract in the loop.

### C4.0 — The two modes emit *differently interleaved* streams

Sequential and parallel are not "the same events, different concurrency". The interleaving itself
differs, and it is observable:

| Mode | Emission pattern |
| --- | --- |
| **Sequential** | `startA → endA → resultA → startB → endB → resultB` — each call's result is emitted **before the next call starts** |
| **Parallel** | `startA → startB → …` (all starts first) `→ ends in completion order → all results in source order at the end` |

So in **sequential** mode a `tool_result` precedes the next `tool_execution_start`; in **parallel**
mode **every** `tool_execution_start` precedes **any** `tool_result`.

**Evidence.** Sequential: agent-loop.ts:444–481 — start, prepare, execute, end and result all
inside one loop body. Parallel: :499–548 — starts and preparation in the first loop, results in a
separate loop after `Promise.all`.

**Go trap.** Unifying both into one code path behind a `parallel bool` flag and emitting results
uniformly. That silently breaks whichever mode you didn't design for — and C3 means a single
sequential tool can flip a batch into the other mode at runtime.

### C4.1 — Ordering rules on the parallel path

| What | Order |
| --- | --- |
| `tool_execution_start` | **source order, emitted serially** before any execution begins |
| `prepareToolCall` | **serial**, source order |
| `tool_execution_end` | **completion order** |
| `ToolResultMessage` emission + returned `messages` | **source order** |

**Evidence.** `executeToolCallsParallel`, agent-loop.ts:489–554. The first `for` loop emits starts
and awaits preparation serially (:499–538); only execution is deferred into thunks (:522–534);
`Promise.all` restores source order (:540–542); results are emitted in a second ordered loop
(:544–548).

**Additional subtlety.** A call that fails *preparation* (`preparation.kind === "immediate"`, e.g.
argument validation) emits its `tool_execution_end` **inline in the first loop** — i.e. *before*
later calls emit their `tool_execution_start`. A legal trace is:
`startA → endA → startB → startC → endC → endB`.

**Why it matters.** pi's event stream is a public API (RPC clients render from it). Order is part
of the contract, not an accident of scheduling.

**Go trap.** Fanning out goroutines immediately. Starts then race, which violates this contract
**while still passing a test that only asserts results are ordered**. Write results into a
pre-allocated slice at the source index; never append in completion order.

---

## C5 — A batch terminates only if **every** call agrees

**Rule.** `shouldTerminateToolBatch` is true only when the batch is non-empty **and every**
finalized call has `result.terminate === true`. One tool cannot end the loop on its own.

**Evidence.** agent-loop.ts:582–583.

**Why it matters.** This is loop-control: it decides whether the agent keeps going. Ranked above
C8 in importance for that reason.

**Go trap.** Reading it as "any tool may terminate" — the natural assumption, and the inverse of
what pi does.

---

## C6 — Aborting mid-batch leaves tool calls with no result

**Rule.** On abort, both execution paths `break` out of the dispatch loop. Remaining tool calls
emit **no start event and produce no `ToolResultMessage`**. The assistant message therefore
retains `toolCall` ids with no matching `toolResult`.

**Evidence.** Parallel: `if (signal?.aborted) break` at agent-loop.ts:516–518 and :535–537 —
checked after each start/preparation. Sequential: :478–480 — checked **after** the current call's
result has been pushed, so the in-flight call's result *is* kept and only subsequent calls are
dropped. Both paths therefore orphan the remaining calls; they differ only in where the cut falls.

**Why it matters.** Session persistence, replay, and the next provider round-trip must all
tolerate unpaired tool calls. This is a real state, not a corruption.

**Go trap.** Asserting the invariant "every toolCall has exactly one toolResult" — in
deserialisation, in a database schema, or when rebuilding provider payloads. It is false.

---

## C7 — An errored or aborted turn stops the agent immediately

**Rule.** When `stopReason` is `error` or `aborted`, the loop emits `turn_end` with an **empty**
`toolResults`, then `agent_end`, then returns — **without** checking for follow-up messages.

**Evidence.** agent-loop.ts:196–200.

**Why it matters.** A follow-up queued during a failing turn is *not* consumed. Anything relying on
"the queue always drains" is wrong.

---

## C8 — Each turn may replace the next turn's snapshot

**Rule.** After every turn, `prepareNextTurn` may return a new context, model, and thinking level,
which apply from the next turn onward; `shouldStopAfterTurn` may end the run. Both are optional.

**Evidence.** agent-loop.ts:232–257.

**Why it matters.** Model and reasoning level can change **mid-conversation** — the same session
may start on a cheap model and escalate. **[unverified against eino]**

**Go trap.** Composing a graph once at startup with a fixed model binding.

---

## Contract → test matrix

| # | Contract | Conformance test sketch |
| --- | --- | --- |
| C1 | steering vs follow-up | push mid-turn → lands before next model call; push at idle → revives loop |
| C2 | truncation | truncated message with 3 tool calls → 0 executions, 3 failures |
| C3 | serialisation | 1 sequential + 2 parallel tools → no execution intervals overlap |
| C4.0 | mode interleaving | same 2 tools run both ways → sequential puts `resultA` before `startB`; parallel puts every start before any result |
| C4.1 | orderings | slow first tool + fast second → starts source-ordered, ends completion-ordered, results source-ordered |
| C4b | immediate-prep | invalid args on call A → `endA` precedes `startB` |
| C5 | terminate | all-terminate → loop stops; one-of-three terminate → loop continues |
| C6 | abort | abort during batch → fewer results than calls, no orphan crash |
| C7 | error turn | errored turn with queued follow-up → follow-up not consumed |
| C8 | snapshot | change model at turn N → turn N+1 uses it, context stays continuous |

## Open items
- Contracts marked **[unverified against eino]** → `docs/specs/eino-verification-plan.md`.
- ~~pi's sequential path deserves the same line-level read as the parallel path~~ — **done**; it
  produced C4.0 (the two modes interleave differently) and sharpened C6's abort semantics.
- `prepareToolCall` / `finalizeExecutedToolCall` internals not yet read line-by-line; C2's failure
  shape is taken from the call site. Read before implementing tool-call validation.
