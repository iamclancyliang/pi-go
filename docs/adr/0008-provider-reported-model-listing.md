# ADR-0008: The model listing is asked of the provider; the model facts stay owned

**Status:** proposed — awaiting @qy-liang
**Date:** 2026-09-04
**Decision owner:** @qy-liang
**Related:** ADR-0007 · `docs/product/pi-feature-inventory.md` §4, §7.2, §27.1 · issues #30, #33, #36 · `docs/product/parity-matrix.md`

## Context

Issue #30 asks where pi-go's model metadata comes from, and names three candidates: a vendored
catalogue, a fetch at runtime, or provider-reported lists.

**Half of that question is already answered.** ADR-0007 decided that the model FACTS — context
window, output cap, whether the model reasons, which thinking levels it accepts — are pi-go's own:
hand-maintained in Go, one stated source per entry, absence a first-class answer. It rejected both a
vendored models.dev snapshot and a runtime fetch of one. That decision stands, and this one does not
reopen it.

What ADR-0007 left open, in as many words:

> **Provider-reported lists** (`GET /v1/models`). Cannot answer the question — ids only, and none of
> the four fields. Useful later as a source of identifiers for `/scoped-models`, alongside recorded
> metadata rather than instead of it.

That is the open half, and it is the half #30 actually blocks on. `/scoped-models` enables and
disables models for Ctrl+P cycling (§4, from Pi's own slash-command table at
`slash-commands.ts:22`), and `/model` with no argument opens a selector over a catalogue (§27.1,
`interactive-mode.ts:4633`). Both need an **enumeration** — a set of ids to put in front of a
person. Neither needs a context window or a thinking map to show a name.

pi-go has no enumeration of any kind. `ai.KnownModels` lists the models facts were recorded for —
one, `deepseek-chat` — and its own comment says what it is not: "NOT the models a provider offers —
this repository has no such list, and presenting these as one would show a user a fraction of what
they can call."

The two questions have different right answers, which is why one ADR could not settle both:

| | What is true of a model | Which models exist |
| --- | --- | --- |
| Changes | rarely | without notice |
| Size | four fields per entry | hundreds of ids per provider |
| Who knows it | measurable, citable, sometimes only by probing | only the provider |
| Cost of being wrong | a check acts on a wrong number | a name is missing from a picker |
| Answer | owned and sourced (ADR-0007) | **asked of the provider** (below) |

## Decision

### A listing is asked of the provider, at the moment a person asks for one

pi-go obtains model **identifiers** by asking the provider through a port method, when a user opens
a listing. Not at startup, not on a timer, not from a file in this repository.

The reasons, in order:

1. **It is the only first-party source of what currently exists.** A provider knows which ids its
   account can call today; no snapshot does, and models.dev is a third party's reading of the same
   thing.
2. **Staleness has nowhere to hide.** A listing is exactly as current as the call that fetched it.
   The failure mode of every alternative — a list that is quietly a few months old — cannot occur.
3. **ADR-0007's objections do not transfer.** They were about third-party data in the repository
   under unexamined terms, needing its own pin and refresh, and about a third-party service in the
   startup path. Identifiers the provider itself just sent are neither.

**This is not foreign to Pi.** Pi has its own runtime-refresh seam — `ModelsRefreshOptions` and
`RefreshModelsContext` (`models.ts:46-76`) — where a provider fetches its own catalogue with a
stored snapshot and a publish callback. §7.2 records that exactly one provider in the pinned tree
implements it, the generated catalogue serving the other forty-one. So this decision promotes Pi's
secondary mechanism to pi-go's primary one. It does not invent a mechanism Pi lacks.

### Never in the startup path, and never reached by `go test ./...`

A listing happens because someone asked for one. pi-go must start and run with no network, and a
tool that phones a provider to boot is a tool that fails in an aeroplane and on a locked-down
build host.

The same discipline the live tests already carry applies here: a test that reaches a provider is
gated, skips by default, and CI never runs it. A listing call is ordinarily unbilled, but it does
carry the user's credential to a network — cheap is not the same as free of consequence.

### A listing and the facts stay separate structures

They are never merged into one catalogue type. A listed id with no recorded facts is the **normal**
case, and is shown as a name with nothing claimed about it.

Merging them would produce entries half-sourced and half-blank, and ADR-0007's rule that absence is
a first-class answer would not survive the first UI that needed a number to render a row.

### A model does not have to be listed to be used

`/model <provider>/<name>` keeps working with no listing at all, exactly as it does today. **The
listing is discovery, not an allowlist.**

The alternative is worse in the case that matters: a provider whose listing call fails, or which
cannot list at all, would take with it models that answer perfectly. Being unable to enumerate is
not evidence that a name is invalid.

Pi's rule is different — §27.1 records that `/model` with an argument takes "an exact match against
the catalogue", and no match opens the selector filtered by the term. pi-go's is: an exact name
switches, and a listing helps a person find one. That is a deviation, and it should be registered as
such when this ADR is accepted.

### A port that cannot list says so, and that is an answer

A listing is not guaranteed to exist, and where it exists it is not guaranteed to be the set a user
can call. Ark takes either a published model name or an **endpoint id** — `ep-20240101120000-abcde`,
an account's own deployment rather than a name the vendor publishes
(`internal/provider/ark/port.go:43-50`). Whatever a vendor's catalogue of models says, it does not
describe what that account created.

So the port method may report that this provider does not list, or list something narrower than what
is callable. Both are rendered as what they are, not as failures, and neither takes away the ability
to name a model directly.

Which of the nine ports can list, and by what call, is a **per-port fact with a per-port source**,
recorded when each is implemented — the same standard ADR-0007 holds a model fact to.

## Consequences accepted

**#30 is not closed by this ADR, and this is what remains.** The data-source blocker is what it
removes. Three others are named rather than dissolved:

- the selector is a full-screen TUI surface, which is #28;
- **Pi's selector behaviour was never read.** §27.1 lists `/scoped-models` under "dispatch only —
  open selector UIs; their behaviour is the selector's, which is TUI and not read". So everything
  beyond "enable/disable models for Ctrl+P cycling" is `semantics-needed`, including where a scoped
  set is persisted and whether it is per-session or global;
- Ctrl+P cycling is an app-level keybinding, which is #28 again and #36.

Implementing a listing therefore gets `/model`'s argument form a way to discover names. It does not
by itself produce `/scoped-models`.

**#33 loses a gate it should not have had.** That issue records itself as "gated in part by #30,
since a provider without model metadata cannot be selected by name from a catalogue". Under this
decision a new port ships selectable by name immediately, lists if its provider can, and carries
recorded facts if and when a source for them exists. Three independent things instead of one
blocking chain.

**A listing costs a network call the user did not previously make.** On demand, once, when asked.

**Two machines running the same build can see different listings.** That is the provider's truth
changing between two calls, which is the thing being reported. It is not the failure ADR-0007
rejected under "two machines could then disagree about what a model is": there, a third party's
answer to *what a model is* varied by cache state. Here the identity of a model is untouched — only
the question of which ones exist right now, whose answer is genuinely time-varying.

**Ordering, filtering and grouping of a listing are pi-go's own.** Pi's selector was not read, so
there is nothing to be compatible with, and inventing an approximation of an unread UI would be a
claim this repository cannot support.

**pi-go still cannot say how many models it "supports".** Unchanged from ADR-0007. It can say which
providers it reaches, which models a provider reported when asked, and which models it has recorded
facts about. Three smaller true statements.

## Alternatives, and why not

**Vendor a models.dev snapshot.** Rejected for the reasons ADR-0007 gave, all unchanged: third-party
data under unexamined terms, a second baseline to keep honest beside `086c32e`, its own pin and
refresh process. For identifiers it is additionally the worst of both — stale *and* second-hand,
where the provider is one call away.

**Fetch models.dev at runtime.** Rejected: a third-party service in the path of a tool that must
work offline, and it answers a question its subject can answer directly.

**Hand-maintain an identifier list beside the facts table.** The natural extension of ADR-0007, and
rejected. What makes that table safe is that its entries are few, stable, and degrade to the status
quo when missing — a model with no facts behaves exactly as every model does today. An id list has
none of those properties: entries are many, change without notice, and a missing one is
**user-visible** as a model absent from the picker that the account can actually call. The rule
would look the same and the failure would not.

**Leave it as it is: no listing, type the name.** Where pi-go is today, and it is honest. But it
makes `/model` unusable to anyone who does not already know the id, leaves `/scoped-models`
impossible, and means #30 never closes.

## What this needs before it is code

Nothing here is implemented. Accepting this ADR authorizes the following, and each item is the
evidence its own row will name:

1. **A port method that reports a listing or reports that it cannot**, on the `ai` boundary, exposing
   no framework type (ADR-0001).
2. **A test that no listing is reached without a user asking** — the startup path and `go test ./...`
   both stay offline. This is the one that fails silently if it is not written.
3. **Per-port implementation with a per-port source**, recorded as ADR-0007 requires. The first
   should be one that costs nothing to call: Ollama runs on the developer's own machine.
4. **The deviation registered** — `/model` accepting a name the catalogue does not contain, against
   Pi's exact-match-then-filtered-selector rule.
5. **The parity matrix updated**: the `scoped-models` and `full per-provider model list` rows stay
   `incomplete`, with #28 and the unread selector semantics named as what now blocks them rather
   than the data source.
