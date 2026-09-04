# ADR-0009: The JSON stream publishes pi-go's two event streams and does not merge them

**Status:** accepted — @qy-liang, 2026-09-04
**Date:** 2026-09-04
**Decision owner:** @qy-liang
**Related:** ADR-0006, ADR-0001 · `docs/product/pi-feature-inventory.md` §6.1, §21.3 · `docs/specs/streaming-contract.md` · `docs/product/parity-matrix.md` · issues #31, #28, #32, #36

## Context

#31's first step is finished. Pi's 24 event payloads (§6.1) and its 32 command response payloads
(§21.3) are recorded field by field from the pin. `--mode json` and `--mode rpc` still refuse, and
what blocks them now is the decision ADR-0006 forces: Pi's wire is explicitly **not** the target, so
pi-go's equivalent has to be designed rather than copied.

The census turned up the fact this decision turns on.

**Pi has one event stream and it carries two different things.** Run lifecycle — `agent_start`,
`turn_end`, `compaction_end` — and assistant content deltas, which arrive as `message_update`
wrapping an `AssistantMessageEvent`. One reply produces a handful of the first and thousands of the
second.

**That merge is why `toJsonEvent` exists.** Every non-terminal `AssistantMessageEvent` carries
`partial: AssistantMessage` — the cumulative message so far. Serialised as it stands, a long reply
sends the growing message once per delta, and the output is quadratic in the length of the answer.
So the wire transform drops `message`, lifts `usage` out of it, and strips `partial`
(`modes/json-event.ts`), undoing on the way out a merge the type system made on the way in.

**pi-go already made the other choice, in the architecture rather than on the wire.**
`runtime.Config` has two subscription seams:

| Seam | Carries | Vocabulary |
| --- | --- | --- |
| `Observers` (`events.Observer`) | run lifecycle | 10 kinds, each with `Seq` |
| `ReplyObservers` (`ai.StreamEvent`) | one reply arriving, block by block | the same twelve variants Pi's `AssistantMessageEvent` has, aligned row by row in `docs/specs/streaming-contract.md` |

`internal/runtime/loop.go:1205` gives the reason in as many words: lifecycle events describe what the
agent is doing, and folding thousands of content deltas into that stream "drowns the events a client
watches for".

So pi-go does not have to un-merge on the way out. What it has to decide is whether to **merge on the
way out** in order to look like Pi.

## Decision

### The wire carries both streams and keeps them apart

One JSONL stream on stdout, one object per line, every line naming which family it belongs to.
Lifecycle lines are `events.Event` serialised; reply lines are `ai.StreamEvent` serialised.

Not merging is the substance of this decision. Merging would put thousands of reply lines between
the lifecycle events a client watches for — the exact problem the two-seam architecture solved —
and would buy nothing, since ADR-0006 has already ruled out buying the compatibility that is the
shape's only value.

**A reply line never carries the snapshot.** (Corrected 2026-09-04, before implementation: this ADR
first claimed pi-go accumulates no snapshot at this seam. It does — `ai.StreamEvent.Partial` is a
per-event copy of the reply so far, kept so a renderer never has to accumulate deltas itself. The
premise was wrong; the decision is unchanged.) The wire serialisation omits `Partial` and keeps
`Final` on the two terminal events — the same strip Pi's `toJsonEvent` performs, done at the one
boundary where size on a pipe is the concern. What pi-go declines is Pi's *merge*, not its strip:
the lifecycle stream here never carried the deltas in the first place.

### Every line carries `seq`, from one counter across both families

⚠ **Pi's stream has no sequence number.** Order is delivery order; nothing on the wire recovers it,
so a consumer that buffers, or any hop that reorders, cannot tell what happened first.

`events.Event.Seq` already exists and is documented as the ordering authority — "consumers must not
infer order from Time", because wall clocks tie and go backwards. The wire carries a number with
the same meaning, and reply lines are numbered from the same counter, so a client can interleave
the two families correctly rather than guessing that a delta belongs to the turn currently open.

*(Amended 2026-09-04, when the RPC channel went concurrent.)* The wire's number is **allocated by
the stream writer under its own write lock**, not copied from the runtime's `Seq`. Once two
goroutines write — a prompt's events and the command channel's responses — a number taken before
the lock can reach the wire after a higher one taken by whoever got to the lock first, and "one
order" would be a lie exactly when it mattered. So the runtime's `Seq` orders in-process observers
and the writer's orders the wire; each is real because each is allocated and delivered under one
lock, and they are not the same numbers. `TestTheOrderIsTheWriteOrderUnderConcurrency` is the
falsifier: with allocation moved before the lock it fails every run.

This is what ADR-0006's capability checklist calls correlation, for a one-way stream.

### The stream names its version on its first line

⚠ Pi's JSON stream emits no header — `print-mode.ts:108-112` subscribes and starts writing events, so
a consumer cannot tell which shape it is about to read and has no way to fail cleanly on a shape it
does not know.

pi-go writes one line first, naming the protocol version and nothing else. A consumer that does not
recognise the version can stop before misreading a single event. Version *negotiation* is not part of
this: there is no partner to negotiate with on a one-way stdout stream. That question belongs to the
command channel.

### `--mode json` is the stream; `--mode rpc` is the stream plus a command channel

Pi's own structure — its JSON mode is print mode with `mode === "json"` and no input path at all,
while RPC adds commands on stdin. The two already resolve separately in `internal/cli/mode.go`, so
this settles what each writes rather than how either is selected.

**Scope: this ADR settles the event stream and its framing, which is everything `--mode json`
needs. It does not design the command channel.** What that still requires is named at the end.

### Every one of Pi's events gets a disposition, and most are `incomplete`

ADR-0006 requires each remote capability to end as `native-equivalent`, `accepted-deviation` or
`incomplete`. Read against what pi-go emits today (`internal/runtime/`, `internal/events/`):

| Pi event | pi-go | Disposition |
| --- | --- | --- |
| `agent_start` · `turn_start` | `agent_start` · `turn_start` | native-equivalent |
| `turn_end` | `turn_end` — carries a reason; Pi's carries the message and its tool results | native-equivalent, shape differs |
| `agent_end` | `agent_end` — without `willRetry`, because pi-go has no session-level auto-retry to report | **incomplete** (#39) |
| `message_start` · `message_end` | `model_request` · `model_response` | native-equivalent, **decomposed differently**: Pi's are per message (user, assistant, tool result), pi-go's are per model call |
| `message_update` | the `ReplyObserver` stream, twelve variants | native-equivalent, and unmerged |
| `tool_execution_start` · `tool_execution_end` | `tool_start` · `tool_end` | native-equivalent |
| `tool_execution_update` | — | **incomplete** — pi-go's tools report no partial result |
| — | `tool_result` | pi-go's own: an end follows completion, a result follows the order the model asked for them, and folding the two makes parallel and sequential execution inexpressible |
| — | `model_changed` | pi-go's own; Pi's nearest equivalent is `entry_appended` carrying a `ModelChangeEntry` |
| `agent_settled` | — | **incomplete** |
| `queue_update` | — | **incomplete** — pi-go steers but publishes no queue state |
| `compaction_start` · `compaction_end` | — | **incomplete** — `internal/compaction` runs and emits nothing |
| `entry_appended` · `session_info_changed` | — | **incomplete** — the session store appends without publishing |
| `thinking_level_changed` | — | **incomplete** (#36) — nothing writes a thinking level at all |
| `auto_retry_start` · `auto_retry_end` | — | **incomplete** (#39) — no auto-retry |
| `summarization_retry_scheduled` · `_attempt_start` · `_finished` | — | **incomplete** — needs retry (#39) and branch summarization (#32) |
| `bash_execution_update` | — | **incomplete** (#28) — Pi's `!` execution is a full-screen-mode surface |
| `extension_error` | — | **incomplete** — no extension host; the parity matrix records that surface as architecture-risk |

Eight of Pi's capabilities have native equivalents today and fifteen do not. **That is a reason to
ship the stream, not to keep refusing.** ADR-0006's scheme exists precisely so a surface can be
published while its gaps are enumerated: an `incomplete` blocks the parity release, not the feature.
A stream that emits what pi-go actually observes, and a matrix row that says which of Pi's events it
does not, is more useful to a client than a mode that exits 2 — and it is honest in a way that
inventing the missing fifteen would not be.

## Consequences accepted

**`--mode json` becomes implementable and `--mode rpc` does not.** The rows stay `incomplete` for
different reasons afterwards: json for the fifteen unemitted capabilities, rpc for those plus a
command channel nobody has designed.

**A client cannot reconstruct Pi's `message_update`.** It receives lifecycle and reply lines
separately and correlates them by `seq`. That deviation from Pi's shape is registered as **D-16**.

**One counter across two families is a constraint on the emitters**, not just the writer: both seams
must draw from the same sequence, or the interleaving the wire promises is not real. That is a test.

**The absence of session-level auto-retry now has a name and an issue: #39.** Five of Pi's events
exist only because Pi retries a failed turn. pi-go does not, anywhere — the provider ports switch SDK
retries *off* by design, which is a different question. Whether pi-go should retry a turn is a
product decision nobody has made; this ADR's accounting is what surfaced it, and #39 is where it gets
made.

**pi-go's stream carries two events Pi has no counterpart for** — `tool_result` and `model_changed`.
Both are already justified where they are declared. They make the stream not a subset of Pi's in
either direction, which is what ADR-0006 means by a native protocol rather than a reduced one.

## Alternatives, and why not

**Merge the two streams to match Pi's shape.** Rejected. It would re-create inside one wire stream
the drowning problem `loop.go:1205` keeps out of the architecture, and it buys nothing: ADR-0006
rules out the compatibility that would be the only reason to want the shape. (An earlier version of
this section also claimed the merge would require building a snapshot nothing else needs — wrong,
see above; the snapshot exists for renderers. The rejection stands on the two real legs.)

**Emit lifecycle only and drop the reply stream.** Simpler, and rejected: watching a reply form is
the main reason to consume a JSON event stream. A mode that reports only that a turn started and
ended is a log, not a protocol.

**Keep refusing until every capability exists.** Rejected. Fifteen gaps span the TUI (#28), session
entry kinds (#32), thinking (#36), extensions and retry — years of surface, gating a mode that could
be useful now. ADR-0006's three-way disposition exists so that a partial surface can ship with its
gaps recorded, and using it is not the same as claiming parity.

**Adopt Pi's event names for the capabilities pi-go does have.** Tempting, and rejected for a reason
this repository has already been bitten by: a name that matches Pi's invites a reader to assume the
payload does too, and `agent_end` is exactly the case — same name, and Pi's carries a `willRetry`
that changes what a client should do. pi-go's names are its own, and the mapping is this table.

## What this needs before it is code

1. ~~A serialisation for both families~~ — `internal/jsonstream`, version line first.
2. ~~A test that one counter spans both seams~~ — `TestOneCounterSpansBothFamilies`, confirmed to
   fail when reply events are numbered apart.
3. ~~A golden-trace test for the stream~~ — `TestTheStreamCarriesTheReplyAndItsLifecycle`.
4. ~~The deviation registered~~ — done on acceptance: **D-16**.
5. ~~The parity matrix updated when the stream ships~~ — shipped 2026-09-04; the row reads `partial`
   with this ADR's table as its evidence.
6. ~~An issue for session-level auto-retry~~ — filed as #39.

The command channel — 32 commands, their request fields and response payloads all recorded in §21.1
and §21.2 — needs its own decision covering ownership and contention, cancellation, correlation of a
response to a request, and typed failures. It is the larger half of #31 and it is not decided here.
