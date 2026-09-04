# ADR-0010: The command channel — required ids, one ordered stdout, typed failures

**Status:** accepted — @qy-liang, 2026-09-04
**Date:** 2026-09-04
**Decision owner:** @qy-liang
**Related:** ADR-0006, ADR-0009 · `docs/product/pi-feature-inventory.md` §6, §21 · issues #31, #27, #28, #30, #32, #36, #39

## Context

ADR-0009 settles what `--mode rpc` writes when nobody asks it anything: the event stream. This ADR
is the other half — commands arriving on stdin, and what goes back.

The census closed the schema questions first (§21.1 request fields, §21.2–21.3 response payloads),
so this is a design decision over recorded facts rather than a reading exercise. Three of those
facts shape the design:

1. **Pi's correlation is optional.** Every request MAY carry an `id`; the response echoes `id?` and
   the `command` name. A client that pipelines two commands of the same type without ids gets two
   responses it cannot tell apart, and nothing in the protocol prevents that client from existing.
2. **Pi's failure path is untyped prose.** One shared variant — `{ command, success: false, error:
   string }`, commented "any command can fail" — beside 32 typed successes (`rpc-types.ts:230-231`).
   A client cannot distinguish "bad argument" from "provider quota exhausted" without parsing English.
3. **Pi's acknowledgements are load-bearing.** `prompt`, `steer` and `follow_up` answer with receipt
   only; outcomes arrive as events (§21.4). Two commands return `null` for "nothing to cycle to" and
   four return `{ cancelled }` for "completed by doing nothing" — ordinary cases, not errors.

pi-go is a single process reading its own stdin. There is no connection, no second client, and no
contention — those exist only for the network protocol (`packages/protocol`, target v3), and this
ADR does not borrow their problems.

## Decision

### Every request carries an id, and every response echoes it

Required, not optional. The ambiguity in fact 1 exists only for requests without ids, so the native
protocol does not have requests without ids. A request missing one is answered by a failure that
says so — correlated by position, the one place position is still needed.

This deviation from Pi's optional `id` is registered as **D-17**.

### Responses join the event stream's single ordered stdout

One stream, one `seq` counter — the same counter ADR-0009 puts on lifecycle and reply lines, now
spanning responses too. A client reads stdout in order and that order is the truth: the response to
`compact` and the `compaction`-related events it caused carry sequence numbers that say which came
first, instead of the client guessing from adjacency.

### Failures are typed, with the taxonomy this repository already has

A failure response carries a stable kind plus human detail, not prose alone. Where the failure came
from a provider, the kind is the provider-failure classification `internal/ai` already maintains —
the one every port is held to, where quota is not a throttle and a moderation refusal is not an auth
failure. Where it is the protocol's own (unknown command, malformed request, missing id), the kinds
are pi-go's, few, and closed.

Pi's single prose variant is the alternative, and rejecting it is the point of having classified
failures at all: this repository has repeatedly found that the difference between "wait", "pay",
and "fix your request" is the thing a caller most needs and prose most obscures.

### Pi's three load-bearing semantics are kept

- **Ack-then-events** for turn-starting commands: the response confirms receipt, the outcome is the
  event stream. A client that treats the ack as completion is wrong in Pi and stays wrong here.
- **Null-not-error** for "nothing to cycle to".
- **Cancelled-success** for operations a user dismissed: completing by doing nothing is a success
  with `cancelled: true`, not a failure.

These are semantics, not shapes; keeping them does not copy a byte of Pi's wire.

### Command names are pi-go's own, and the accounting is the table below

The same decision ADR-0009 made for events, for the same reason: a matching name invites the
assumption of a matching payload, and `agent_end` already showed where that leads.

### Every one of Pi's 32 commands gets a disposition

Read against what pi-go has (the slash-command layer in `internal/cli`, the session store, the
runtime seams), with the parity matrix as the capability authority:

| Pi command | pi-go capability today | Disposition |
| --- | --- | --- |
| `prompt` · `steer` · `follow_up` | the runtime's turn loop and steering/follow-up delivery (C2/C3) | native-equivalent |
| `abort` | termination via context, every call agreeing to terminate (A7) | native-equivalent |
| `new_session` | `/new` | native-equivalent |
| `get_state` | model, session id and name, usage — but no steering/follow-up *mode*, no `isCompacting` flag | **partial** — the state object exists only as big as the state does |
| `set_model` | `/model` | native-equivalent |
| `cycle_model` | needs scoped models and a listing | **incomplete** (#30, ADR-0008) |
| `get_available_models` | needs the listing | **incomplete** (#30, ADR-0008) |
| `set_thinking_level` · `cycle_thinking_level` · `get_available_thinking_levels` | nothing writes a thinking level | **incomplete** (#36) |
| `set_steering_mode` · `set_follow_up_mode` | steering and follow-up exist as delivery timing; the all / one-at-a-time selection does not | **incomplete** (#40) — mechanism without the mode switch |
| `compact` | `/compact` | native-equivalent |
| `set_auto_compaction` | overflow recovery is always on and has no toggle | **incomplete** (#41) — a toggle is a decision about recovery, not a flag to add casually |
| `set_auto_retry` · `abort_retry` | no session-level retry | **incomplete** (#39) |
| `bash` · `abort_bash` | the bash *tool* exists; running a command outside the turn with `excludeFromContext` does not | **incomplete** — closest to #28's `!` surface |
| `get_session_stats` | counts and token ledger; **no currency** — pi-go computes no cost (§7.2) | **partial** |
| `export_html` | `/export`, Markdown | deviation D-5 (#27) |
| `switch_session` | `/resume` | native-equivalent |
| `fork` · `clone` · `get_fork_messages` | `/fork`, `/clone`, and the entries they select over | native-equivalent |
| `get_entries` · `get_tree` | the session store's snapshot and `/tree` | native-equivalent — minus the entry kinds pi-go lacks (#32) |
| `get_last_assistant_text` | `/copy`'s source | native-equivalent |
| `set_session_name` | `/name` | native-equivalent |
| `get_messages` | the session snapshot | native-equivalent |
| `get_commands` | the command table `/help` prints | native-equivalent |

Of the thirty-two: seventeen have native equivalents today, two are partial, one is an already
registered deviation (D-5), and twelve are incomplete — and every incomplete one was already
incomplete somewhere else (#28, #30, #32, #36, #39, and the two this table itself surfaced, filed
as #40 and #41: queue-mode selection and the auto-compaction toggle). The channel adds no new gaps; it inherits the
ones the features have.

## Consequences accepted

**`--mode rpc` becomes implementable at the size pi-go actually is.** Twenty commands answerable —
seventeen natively, two partially, one in Markdown — and honest responses for the twelve that are
not: an unimplemented command fails with a typed kind saying so, which is the channel's own
vocabulary doing the job `--mode rpc`'s exit-2 refusal does today, one command at a time instead of
all at once.

**A required id is stricter than Pi.** A hypothetical client written for Pi's loosest case — fire
and match by order — would need ids added. That client also could not tell Pi's own responses apart;
the strictness is the protocol declining to reproduce a latent bug.

**Two new unowned gaps get names.** Queue-mode selection and the auto-compaction toggle are
mechanisms whose switches don't exist. Filed on acceptance: **#40** and **#41**.

**The state object will grow field by field**, and `get_state`'s response is versioned by the
protocol version line (ADR-0009), not by field presence guessing.

## Alternatives, and why not

**Optional ids, like Pi.** Rejected — fact 1. Keeping the option keeps the ambiguity, and no client
is made better by being allowed to omit the one thing that makes its responses attributable.

**Prose failures, like Pi.** Rejected — the taxonomy exists, every provider port already feeds it,
and flattening it back to English at the last boundary would undo the classification work at the
moment a machine finally consumes it.

**A second stdout stream, or interleaving frames.** Rejected: stdio gives one ordered pipe, and one
`seq` counter over everything on it is strictly more information than any framing that splits it.

**Refuse the mode until every command exists.** Rejected for ADR-0009's reason: the three-way
disposition exists so a surface ships with its gaps enumerated. A channel that answers twenty
commands and names why it cannot answer twelve is more useful than exit 2.

## What this needs before it is code

1. ~~The request and response serialisation~~ — `internal/rpc`, `id` required, `seq` on every
   response from the shared counter, failure kinds closed and tested.
2. ~~A golden transcript test~~ — `TestAPromptsResponseAndItsEventsShareOneOrder` (the one order),
   plus the channel's refusal and dispatch tests.
3. ~~The deviation registered~~ — done on acceptance: **D-17**.
4. ~~Issues for the two gaps this table surfaced~~ — filed on acceptance: #40, #41.
5. ~~The parity matrix updated~~ — shipped 2026-09-04; the row reads `partial`, six commands
   answered natively, each unbuilt one pointing at its issue.

**Shipped, then made concurrent the same day.** The first slice answered six commands from a
synchronous loop. The loop now runs a prompt on its own goroutine while stdin keeps being read,
which is what `abort`, `steer` and `follow_up` need — each is a command that only means anything
DURING a run — so nine commands answer natively. Two rules came with the concurrency: one prompt at
a time, a second refused as `busy` rather than queued (a queue the client cannot see into is a
prompt it believes is running; steer and follow_up are how work in flight is added to), and on EOF a
running prompt is allowed to finish, because a client that stopped sending has not asked for the
work to be thrown away. The one-order guarantee moved with it: it no longer rests on the loop being
synchronous, but on the stream writer allocating every line's number under its own write lock —
see ADR-0009's amendment. Every other command still fails with a typed kind that separates unknown
from unbuilt, and those are the feature gaps this table already named.
