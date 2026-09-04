# Corrections to earlier commit messages

Five commit messages in this repository describe their own change inaccurately. The commits are not
being rewritten, so the record of what was actually true lives here.

Each entry names the claim, what the code did at that commit, and where the behaviour stands now. A
later fix does not make an earlier message true, which is the reason this file exists rather than a
note that the code is fine today.

## `d1f5716` — "a request cannot be built without a cap"

That commit added configuration validation and terminal mapping only; it contained no code that
built a request. The output cap was required at construction and reached nothing. It first travelled
with a request in `aa5e189`.

Requiring a value at construction says nothing about what was sent. The two are separate claims, and
only the second is worth anything to someone reading a bill.

## `b2347d9` — "Block identity comes from the provider's own index rather than being renumbered"

That commit renumbered. `stream.go` assigned each new block the next position in its own counter,
and nothing compared the result against what the provider had announced.

The adapter underneath renumbers as well, so after conversion a stream that skipped an index is
indistinguishable from one that did not. Reading the provider's own content positions, and refusing
a stream that skips them, arrived in `42b6902`; `b71b4fb` added the same refusal for item positions
and took over both checks. An announcement carrying no position at all was accepted until `aa41438`,
and one carrying no content until `affd2db`.

## `ff48704` — "A reply that never names the model that served it leaves that unknown"

It echoed the requested one. The accumulator was seeded with the configured model, so a reply whose
terminal named no model reported the model that had been asked for — the substitution the sentence
claims to rule out. Seeded empty in `aa5e189`.

## `8081115` — "nothing that already imports a provider package has to change to keep compiling"

Type and constant aliases were kept, but a field was renamed in the same change: `Error.Usage`
became `ProviderError.Used`. Code reading that field, or building the struct with it, does not
compile. The claim is wrong rather than imprecise — aliases cannot preserve a field name that no
longer exists.

## `aa41438` — two claims

**"A failed call's spend is owned like a ledger entry, copied in and copied out."** Copy-out held;
copy-in did not. The field was assignable, so a caller kept its own reference to whatever it had
stored. Closed in `affd2db`, where the field became unreachable except through a method that copies.

**"Which source wins is one question for the process, not one per request."** True of a provider
whose credential can only come from configuration, and false as a general rule: a provider that also
reads a store must sample per call, or a replaced credential never takes effect. The guarantee that
does hold everywhere is that one call resolves at most once and uses that value for every attempt
and for reporting. Stated correctly in `69f31dc`.

## `df327fd` — a message about the mistake instead of the change

It described how a file came to be committed rather than what the change did. What it should have
said: the file removed from tracking is `docs/research/provider-contract-source-audit.md`, a working
note made while reading this repository's provider contract against its sources. It records one
reader's route through the code on one day; it is not something this repository documents, and it
has no reader once that reading is finished.

What `df327fd` did, precisely: it removed the file from the tracked tree. It is still reachable in
the history at `651f0ab`, where it was added, and a fresh checkout of any later commit does not
contain it. Whether it exists in a given working copy is a fact about that copy, not about this
repository — an earlier version of this entry said it was left "on disk and out of the history",
and both halves were wrong.

## Wording that does not stand on its own

Four messages use terms that mean nothing to a reader who was not present when they were written:
"this tranche" in `b2347d9` and `aa7e137`, "obligations list" in `9acdd46`, and both "only temporary
checks had covered" and "hung for ninety seconds" in `b8e5156`.

What they referred to: "this tranche" meant the set of content-block kinds the adapter handled at
that point, which was text, reasoning and tool calls — anything else ended the reply with a failure
naming it. "obligations list" meant `docs/specs/provider-port-obligations.md`, the table of
behaviours every provider adapter has to carry. The two in `b8e5156` describe how the work was
checked before it was committed, which is not a fact about the change: what that commit did was add
regression tests for behaviours that had none.

A commit message is read years later by someone with only the diff for context. A word that requires
knowing what else was happening at the time is a word that will not survive that reading.

## `3eeaf84` and `39bce22` — "a cumulative snapshot pi-go does not otherwise keep"

Both messages (and the ADR they describe, corrected in place on 2026-09-04) claim pi-go never builds
a cumulative reply snapshot, so refusing Pi's merged `message_update` avoids a build-then-strip round
trip. `39bce22` repeats it as D-16's rationale: "pi-go never builds the cumulative snapshot, so there
is nothing to strip."

pi-go builds exactly that snapshot. `ai.StreamEvent.Partial` is a per-event copy of the reply so far,
on all ten non-terminal events, kept so a renderer never accumulates deltas itself
(`internal/ai/stream.go`). The wire simply does not serialise it — the same strip Pi's `toJsonEvent`
performs, at the same kind of boundary.

The decision the commits record is unchanged; the argument was wrong. Not merging stands on the two
legs that were true throughout: a merged stream drowns lifecycle events in deltas (the reason
`loop.go:1205` gives for the two-seam architecture), and the merged shape's only value is a
compatibility ADR-0006 rules out. Found while implementing the stream, before any code was written
against the false premise.
