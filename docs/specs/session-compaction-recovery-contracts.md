# Session, compaction, and recovery contracts

**Status:** draft · closes traceability gap G3 for PRD A10–A12

**Pi baseline:** [`086c32e74530564922d011ade23ff582c9d63116`](https://github.com/earendil-works/pi/commit/086c32e74530564922d011ade23ff582c9d63116)

**Scope:** observable semantics for bounded overflow recovery, durable history versus provider-context
projection, compaction checkpoints, and uncertain destructive-tool recovery.

## Evidence labels

Every contract separates three different kinds of evidence:

- **Pi verified behavior** — executable production code at the pinned commit, normally with a test.
- **Pi implemented primitive** — storage, schema, or pure reduction code exists and is tested, but the
  end-to-end `AgentHarness` execution path is not shipped yet.
- **Pi design precedent / pi-go requirement** — Pi's first-party implementation specification describes
  the target, but the pinned `AgentHarness` still rejects execution and restore. The rule is therefore a
  normative pi-go safety requirement, not a claim that released Pi already enforces it end to end.

This distinction matters most for A11. At the baseline, Pi's changelog calls `AgentHarness` a
"compile-complete scaffold" whose unfinished operation paths reject
[`HarnessNotImplemented`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/CHANGELOG.md#L21-L35).
`AgentHarness.create()` rejects restore when records exist, and `prompt`, `compact`, and `resume` are
unimplemented ([agent-harness.ts L347–381](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/src/harness/agent-harness.ts#L347-L381));
the scaffold test asserts those rejections
([agent-harness-scaffold.test.ts L56–82](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/test/harness/agent-harness-scaffold.test.ts#L56-L82),
[L145–188](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/test/harness/agent-harness-scaffold.test.ts#L145-L188)).

---

## S1 — One automatic overflow recovery attempt per uninterrupted input boundary

**Rule.** For one uninterrupted run boundary, the first recoverable context overflow may trigger
exactly one compaction followed by one more model call. If that call also overflows, the runtime must
not compact or call the model a third time. New projecting user input starts a new recovery budget.

**Pi verified behavior.** `AgentSession` stores `_overflowRecoveryAttempted`; a user message clears it
([agent-session.ts L609–615](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/agent-session.ts#L609-L615)).
On the first recoverable overflow it sets the flag, removes the failed assistant response from live
model context, and runs overflow compaction
([L1988–2021](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/agent-session.ts#L1988-L2021)).
On the second it emits a recovery-failed `compaction_end` and returns without another compaction
([L2001–2012](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/agent-session.ts#L2001-L2012)).
The integration-style faux-provider test asserts two model calls, one overflow compaction, and the
terminal recovery message
([agent-session-compaction.test.ts L328–358](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/test/suite/agent-session-compaction.test.ts#L328-L358)).

**Crash boundary.** Pi's production counter is a process-memory field
([agent-session.ts L328–336](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/agent-session.ts#L328-L336));
the shipping session format has no durable overflow-budget record. Therefore "one attempt even across
restart" is a pi-go strengthening, not verified Pi behavior.

**pi-go requirement.** Count overflow recovery separately from ordinary transient-provider retry.
An overflow must compact before retrying; it must not spend provider retry attempts by resending the
same oversized projection unchanged. Persist enough operation state that a crash cannot reset the
one-recovery budget.

**Test sketch — A10, first half.** A faux model returns overflow twice. A deterministic compactor
succeeds once. Assert:

1. model calls = 2;
2. compactor calls = 1, with reason `overflow`;
3. no third model call occurs, including after close/reopen between calls;
4. a later new user input gets a fresh one-attempt budget.

---

## S2 — A second overflow is a durable terminal operation failure

**Rule.** The second overflow in the same recovery budget must finish the operation as failed, retain
the failing provider attempt for audit/usage, and expose a machine-readable context-overflow failure.
It must not be reported as a successful empty answer.

**Pi verified behavior.** Production `AgentSession` makes recovery terminal operationally: it does
not request another continuation after `_checkCompaction()` returns false
([agent-session.ts L1063–1104](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/agent-session.ts#L1063-L1104)),
and the second-overflow test observes the recovery-failed event. The legacy `prompt(): Promise<void>`
surface does not itself return a typed failed outcome.

**Pi design precedent / pi-go requirement.** The `AgentHarness` specification sends a second overflow
to `failure_drain`
([harness.md L1190–1222](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/docs/harness.md#L1190-L1222),
[L1262–1275](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/docs/harness.md#L1262-L1275)).
pi-go must complete that idea with a typed failed operation result and a durable error code such as
`context_overflow_after_compaction`. This is a deliberate strengthening of the legacy Pi API, while
preserving its call bound and event evidence.

**Test sketch — A10, terminal half.** After two overflows, assert the operation result is `failed`, the
error category is context overflow, both provider attempts and their usage remain auditable, and
close/reopen returns the same terminal result without model or compactor activity.

---

## S3 — Durable conversation history and model-context projection are different data

**Rule.** Compaction must append a new checkpoint entry; it must not edit or delete earlier message,
tool-result, usage, or prior compaction records. Provider context is a deterministic projection of
that durable branch, not the durable branch itself.

**Pi verified behavior.** The production session manager appends every entry, advances the leaf, and
persists the new row
([session-manager.ts L1044–1049](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/session-manager.ts#L1044-L1049));
`appendCompaction` adds a child entry containing summary and retained-boundary metadata rather than
rewriting history
([L1096–1118](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/session-manager.ts#L1096-L1118)).
`getBranch()` returns the full parent path, while `buildSessionContext()` is explicitly the LLM view
([L1255–1285](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/session-manager.ts#L1255-L1285)).
The manager documents entries as append-only and non-deletable
([L1296–1300](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/session-manager.ts#L1296-L1300)).

**Pi design precedent.** The newer harness specification makes the same invariant explicit: entries
and usage are append-only, while compaction changes provider context rather than storage
([harness.md L111–137](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/docs/harness.md#L111-L137),
[L207–214](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/docs/harness.md#L207-L214)).

**pi-go requirement.** The storage port must expose durable branch/history reads independently from
context projection. No API named `Compact` may mutate old entries or make them disappear from audit,
export, fork, or usage accounting.

**Test sketch — A12, history half.** Record old messages, recent messages, and usage; compact; then
assert the durable branch contains every pre-compaction record plus exactly one new compaction entry.
Repeat after reopen. Assert usage totals are unchanged except for the separately attributable
summarization usage.

---

## S4 — A compaction checkpoint is self-contained: summary + retained tail

**Rule.** The preferred pi-go checkpoint format contains the summary and the complete retained
`AgentMessage` tail. Reconstructing provider context must not need to read entries older than the
latest compaction checkpoint.

**Pi implemented primitive.** The newer Pi session schema requires `CompactionEntry.retainedTail`
([types.ts L44–51](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/src/harness/session/types.ts#L44-L51)).
Its context transform selects the latest compaction, projects it to a compaction-summary message plus
the retained tail, then appends later entries
([context.ts L45–99](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/src/harness/session/context.ts#L45-L99)).
The test verifies the projected roles are `compactionSummary`, retained `user`/`assistant`, then the
post-compaction `user`
([context.test.ts L39–68](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/test/harness/session/context.test.ts#L39-L68)).

**Pi compatibility note.** Legacy coding-agent compactions store `firstKeptEntryId` and rebuild the
same logical view from older entries
([session-manager.ts L410–469](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/session-manager.ts#L410-L469)).
Pi's newer format intentionally materializes that range as `retainedTail`; the first-party format
documentation calls it a self-contained checkpoint
([session-format.md L229–248](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/docs/session-format.md#L229-L248),
[L320–342](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/docs/session-format.md#L320-L342)).

**pi-go requirement.** New pi-go writes use the self-contained form. Importers may accept Pi's legacy
pointer form, but must normalize it before relying on the checkpoint for bounded recovery.

**Test sketch — A12, projection half.** Given durable entries `old1, old2, recent1, recent2`, compact
`old1..old2`, then append `after`. Assert the exact provider projection is:

```text
compaction-summary(summary) -> recent1 -> recent2 -> after
```

Instrument storage reads and assert projection reads no entry older than the checkpoint. Assert the
full history query still returns `old1` and `old2`.

---

## S5 — Compaction publication is atomic at the projection boundary

**Rule.** A crash exposes either the complete pre-compaction projection or a complete checkpoint with
its summary, retained tail, and new leaf. It must never expose a summary without its tail, move the
leaf without the checkpoint, or lose the old projection before the new one is durable. Summarization
attempt usage is a separate append-only fact: usage already settled before checkpoint publication
remains auditable and must not be erased or charged again during recovery.

**Pi design precedent / pi-go requirement.** Pi's `AgentHarness` specification publishes a successful
in-run compaction as one transaction containing the result entry, leaf update, and resumed operation
state
([harness.md L1333–1362](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/docs/harness.md#L1333-L1362)).
The same design settles each nested summarization request's usage before final result publication, so
failed or crash-interrupted work stays in the ledger rather than being folded into checkpoint
atomicity
([harness.md L1345–1362](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/docs/harness.md#L1345-L1362)).
The storage invariant says transactions are all-or-none
([L115–127](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/docs/harness.md#L115-L127)).
This transaction-based publication is not end-to-end shipped by the scaffold at the pinned commit;
it is required in pi-go so A12 remains true across process death.

**Test sketch.** Inject a crash immediately before and immediately after checkpoint publication.
Before: reopen to the old leaf and old projection with no compaction entry; any summarization usage
settled before the crash remains recorded exactly once. After: reopen to exactly one complete
checkpoint and the new projection, without duplicating any summarization usage.

---

## S6 — An overflow attempt remains durable but is absent from the retry projection

**Rule.** A provider overflow response is part of durable history and usage accounting, but must not
be sent back to the provider during compact-and-retry.

**Pi verified behavior.** Production `AgentSession` persists assistant messages on `message_end`
([agent-session.ts L639–667](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/agent-session.ts#L639-L667)),
then removes the recoverable overflow response from live context before compaction
([L2014–2021](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/agent-session.ts#L2014-L2021))
and removes it again if context reconstruction retained it
([L2188–2198](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/agent-session.ts#L2188-L2198)).

**Pi design precedent.** The harness design normalizes overflow to an `error`, keeps it in the tree,
and excludes errors through the ordinary projection rule
([harness.md L1281–1306](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/docs/harness.md#L1281-L1306));
its worked example shows the response absent from both summary input and retained tail while remaining
durable
([L1364–1404](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/docs/harness.md#L1364-L1404)).

**pi-go requirement.** Exclude the failed overflow response from both summarization input and the
retry projection. Legacy Pi proves only that it is removed from the live/rebuilt model context; the
stronger compactor-input exclusion follows the AgentHarness design and prevents a provider error
from being summarized as conversation truth.

**Test sketch.** Capture the first and second provider payloads. Assert the first overflow response is
present in durable history and usage, absent from the compactor input, absent from retained tail, and
absent from the second provider payload.

---

## S7 — Repeat-sensitive tools use durable intent → uncertain effect → settlement

**Rule.** Before invoking any side-effecting tool, pi-go must durably record an intent containing at
least operation id, tool-call identity, tool identity/version, exact effective arguments, reserved
result id, and replay policy. Settlement must durably record the result and the next operation state.
The interval between intent and settlement is explicitly `effect_pending`/unknown; absence of a
settlement must never be interpreted as proof that the effect did not happen.

**No shipped Pi contract.** The production `AgentTool` surface has preparation, execution, and
execution mode but no replay, idempotency, rollback, or destructive-effect declaration
([types.ts L385–409](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/src/types.ts#L385-L409)).
Shipping execution directly awaits the tool and converts thrown failures to error results
([agent-loop.ts L670–710](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/src/agent-loop.ts#L670-L710));
the append-only coding-agent session records messages, not a durable per-tool started/settled journal.
Thus a crash after a filesystem mutation but before `toolResult` persistence is ambiguous in released
Pi. A11 is not parity with a shipped Pi guarantee.

**Pi implemented primitive.** Pi's durable session types include `ToolStartedRecord` with exact args,
reserved result id, and `replay: "never" | "safe"`
([types.ts L150–160](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/src/harness/session/types.ts#L150-L160)).
Its tested pure reducer reconstructs whether a tool has a start record, whether its result exists,
and whether the batch is unresolved
([reducer.ts L445–503](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/src/harness/reducer.ts#L445-L503),
[reducer.test.ts L881–923](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/test/harness/reducer.test.ts#L881-L923)).
These are real implemented primitives, but the harness interpreter that would execute the recovery
policy is not implemented at the pinned commit.

**Pi design precedent / pi-go requirement.** Pi names this the "effect sandwich": intent commit,
uncertain external effect, settlement commit
([harness.md L125–137](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/docs/harness.md#L125-L137)).
pi-go adopts it as a safety boundary. It does **not** promise exactly-once external effects; it
promises explicit uncertainty and no blind replay.

**Test sketch — A11, state.** Use a destructive faux tool backed by an external counter and crash
hooks at: before intent, after intent/before dispatch, after mutation/before settlement, and after
settlement. After reopen, assert the durable state unambiguously distinguishes no intent, unsettled
intent, and settled result. Never infer outcome from a missing result alone.

---

## S8 — Unsafe uncertain effects are never replayed; safe replay requires dual agreement

**Rule.** On restore of an unsettled tool intent:

- replay only if both the persisted intent and the currently registered tool declaration say the
  operation is replay-safe;
- otherwise do not invoke the tool and settle the reserved result id with a synthetic
  `interrupted`/unknown-effect error;
- an already-settled result is authoritative and is never re-executed or re-settled.

This maps the PRD's four required states: **replay-safe** and **replay-forbidden** are policy;
**unknown external effect** is the unsettled intent; **interrupted** is its conservative synthetic
settlement.

**Pi design precedent / pi-go requirement.** Pi's crash example says an unsafe deletion is not run
again and receives a synthetic result; a safe read may be re-executed
([harness.md L180–205](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/docs/harness.md#L180-L205)).
The formal recovery table requires both stored and current declarations to say `safe`; otherwise it
writes `interrupted` under the reserved id
([L1894–1916](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/docs/harness.md#L1894-L1916)).
Again, this is first-party design precedent, not shipped end-to-end behavior at the pinned commit.

**Test sketch — A11, policy.** For the crash-after-mutation case with `replay-forbidden`, reopen and
resume; assert the external mutation count remains 1, the tool implementation is not called again,
the reserved result id receives one synthetic interrupted result, and the operation's terminal state
is explicit. Repeat with a replay-safe read and assert it may run again only while both declarations
remain safe. Change the current declaration to forbidden before reopen and assert replay is blocked.

---

## Acceptance mapping

| PRD scenario | Contracts | Minimum conformance assertion |
| --- | --- | --- |
| **A10** overflow twice | S1, S2, S6 | two model calls, one compaction, no loop, typed terminal failure, attempts remain auditable |
| **A11** death after destructive intent, before settlement | S7, S8 | durable unknown state; destructive effect not replayed; one synthetic interrupted settlement |
| **A12** compaction performed | S3, S4, S5 | old history remains; provider projection is summary + retained tail + later context; reopen is identical |

## Non-goals and boundaries

- **No exactly-once claim.** A crash after an external effect but before settlement is fundamentally
  uncertain. The contract prevents blind repetition; it cannot prove whether the first effect ran.
- **No deletion claim.** Compaction is context reduction, not erasure or retention-policy enforcement.
- **No provider-specific overflow oracle.** Classification may use adapter signals, error matching,
  and under-limit `length` heuristics, but the resulting bounded-recovery behavior must be identical.
- **No dependency on Eino checkpoint semantics yet.** These are pi-go-owned observable contracts.
  Eino may implement mechanisms underneath them only after conformance proves the same durable states
  and crash outcomes.
