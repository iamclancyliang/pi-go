# ADR-0007: the provider set follows eino-ext; the model facts are pi-go's own

**Status:** accepted
**Date:** 2026-08-29
**Decision owner:** @qy-liang
**Related:** `docs/product/pi-feature-inventory.md` §7.2 · issues #30, #33, #36 · ADR-0002, ADR-0006

## Context

Two questions look like one and are not.

**Which providers does pi-go support?** Pi's registry holds 42 ids.

**What is true of a given model?** Four things pi-go needs are properties of a
model rather than of a provider: the context window, the output cap, whether it
reasons at all, and which thinking levels it accepts. Without them, count-based
overflow detection is disabled, the output cap is one constant for every model,
and `--thinking` sends a field to models that may not support one.

Pi answers the second with a generated catalogue: `scripts/generate-models.ts`
fetches `https://models.dev/api.json` — a third-party service — and writes one
JSON file per provider into `packages/ai/src/providers/data/`, which is
gitignored. The tracked wrappers import and flatten that JSON and carry none of
it. So the data **cannot be evidenced from the approved baseline** (§7.2), and
no amount of further reading would close that: the entries were never in the
tree.

## Decision

### The provider set follows eino-ext's model components

pi-go is built on eino (ADR-0002), and eino-ext ships a model component per
provider. That set is the denominator pi-go works toward, rather than Pi's 42:
it is the set the framework this repository already depends on can actually
reach, each one an independently versioned module rather than a name in a list.

At the version in use, that is **nine**: `ark`, `claude`, `deepseek`, `gemini`,
`ollama`, `openai`, `openrouter`, `qianfan`, `qwen` — several with an `agentic`
variant alongside.

pi-go has three of them. `openai` and `qwen` go through eino-ext adapters;
`deepseek` is hand-written HTTP, although `deepseek@v0.1.7` and
`agenticdeepseek@v0.1.0` both exist. That inconsistency predates this decision
and is not settled by it — the hand-written port carries behaviour the adapters
did not give us, and replacing it is its own question with its own evidence.

A provider Pi supports and eino-ext does not is **not** thereby out of scope; it
is out of the near-term denominator, and reaching it means writing a port the way
DeepSeek's was written.

### The model facts are pi-go's own, and every one has a source

**eino-ext carries no model metadata.** This was checked rather than assumed:
its components expose `MaxTokens` as a REQUEST field — what a caller asks for —
and no context window, no reasoning flag, no thinking-level mapping. Model names
appear only in tests. There is no registry package; each component is its own
module. Aligning the provider set with eino-ext therefore answers which
providers, and nothing about what is true of their models.

So pi-go maintains its own catalogue, in Go, carrying only the fields it reads,
and admitting only entries with a **stated source**: a provider's official
documentation, or a measurement this repository made and committed. An entry
whose value nobody can point at does not go in.

**Absence is a first-class answer.** A model with no entry behaves exactly as
every model does today: no window, so count-based overflow stays off; the
default output cap; and no thinking gating. Adding the catalogue therefore
cannot make any current behaviour worse, and a missing entry degrades to the
status quo rather than to a guess.

## Consequences accepted

**Each provider is a port and an entry.** Issue #33 becomes six ports rather
than thirty-nine, which is the practical effect of following eino-ext; each
still needs its own recorded model facts.

**pi-go cannot say how many models it "supports".** It can say which providers
it can reach and which models it has recorded facts about. Both are smaller and
truer claims than a count taken from a catalogue this build cannot verify.

**`/scoped-models` stays incomplete** (#30). It is a UI over everything a
provider offers; this catalogue is not that, and a listing missing most of what
a user can actually call is worse than none.

**The alignment cannot be enforced by a test.** eino-ext has no registry to
enumerate, so the nine are a documented list checked by hand against the module
cache. A component added upstream will not announce itself.

**Revisit when the count makes it silly.** At three providers a hand-maintained
table is a file anyone can check. At nine it is still small; well past that it
becomes a chore that will rot, and the tradeoff should be taken again — probably
toward a vendored snapshot with a dated, independently pinned source.

## Alternatives, and why not

**Take Pi's 42 as the denominator.** Rejected as the near-term target: 39 of
them would be ports written from scratch against providers this repository has
no adapter for, and the catalogue backing them cannot be evidenced from the pin
anyway.

**Vendor a models.dev snapshot.** Covers every provider's metadata at once and
matches Pi's data exactly. Rejected for now: third-party data in the repository
under unexamined terms, going stale silently, needing its own pin and refresh
process — a second baseline to keep honest alongside `086c32e`.

**Fetch models.dev at runtime, cached.** Always fresh, and rejected: a
third-party service in the startup path of a tool that must work offline, and
two machines could then disagree about what a model is while running the same
build.

**Provider-reported lists** (`GET /v1/models`). Cannot answer the question — ids
only, and none of the four fields. Useful later as a source of identifiers for
`/scoped-models`, alongside recorded metadata rather than instead of it.

## Evidence

The first entry is DeepSeek, and both of its facts were measured rather than
read from anywhere:

- Context window **1,048,576 tokens**, from the owner-authorized probe on
  2026-08-29 — `conformance/testdata/deepseek-large-request-rejected.json`, the
  provider's own refusal naming the limit.
- Reasoning **supported**, from `TestLiveDeepSeekAcceptsAThinkingRequest`:
  asking for `off` produced no reasoning and asking for `high` produced some, so
  the field is read rather than tolerated.

OpenAI and Qwen have ports but no entries, because no source for their values
has been recorded here yet. Under this decision that is the correct state, not
an omission to be filled in with plausible numbers.
