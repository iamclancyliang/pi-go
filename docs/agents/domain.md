# Domain Docs

How the engineering skills should consume this repo's domain documentation when exploring the
codebase.

**Layout: single-context.** One `CONTEXT.md` + `docs/adr/` at the repo root. The repo is new and
has no multi-module split yet; revisit if it later grows genuinely separate bounded contexts.

## Before exploring, read these

- **`CONTEXT.md`** at the repo root, or
- **`CONTEXT-MAP.md`** at the repo root if it exists — it points at one `CONTEXT.md` per context.
  Read each one relevant to the topic.
- **`docs/adr/`** — read ADRs that touch the area you're about to work in.

If any of these files don't exist, **proceed silently**. Don't flag their absence; don't suggest
creating them upfront. The `/domain-modeling` skill (reached via `/grill-with-docs` and
`/improve-codebase-architecture`) creates them lazily when terms or decisions actually get
resolved.

As of this file's creation the repo is empty, so none of them exist yet. That is expected.

## File structure

```
/
├── CONTEXT.md          ← project glossary (created lazily)
├── docs/
│   ├── adr/            ← architectural decisions (created lazily)
│   └── agents/         ← this directory (skill configuration)
└── ...                 ← Go source layout, as it emerges
```

## Use the glossary's vocabulary

When your output names a domain concept (in an issue title, a refactor proposal, a hypothesis, a
test name), use the term as defined in `CONTEXT.md`. Don't drift to synonyms the glossary
explicitly avoids.

If the concept you need isn't in the glossary yet, that's a signal — either you're inventing
language the project doesn't use (reconsider) or there's a real gap (note it for
`/domain-modeling`).

## Flag ADR conflicts

If your output contradicts an existing ADR, surface it explicitly rather than silently overriding:

> _Contradicts ADR-0007 (event-sourced orders) — but worth reopening because…_

## Project-specific note

This repo is a **Go/eino reimplementation of [pi](https://github.com/earendil-works/pi)**. Two
consequences for domain docs:

1. **The glossary should be shared with pi where the concept is genuinely the same** (agent loop,
   steering vs follow-up, tool batch, compaction, session). Where this project deliberately
   diverges, an ADR should say so — those divergences are the interesting content, and the
   accompanying talk is built on them.
2. **ADRs are unusually valuable here.** Most decisions are "pi does X; do we keep X, replace it
   with an eino mechanism, or build our own?" That question and its answer are exactly what an ADR
   is for. Expect more ADRs than a greenfield project of this size would normally have.
