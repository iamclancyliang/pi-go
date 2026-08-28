# Pi feature inventory schema and completeness gate

**Authority:** @qy-liang, 2026-08-17, amended 2026-08-28. The 2026-08-17 hold on product
implementation was lifted on 2026-08-28. This gate now governs when pi-go may *claim* parity with
`earendil-works/pi@086c32e74530564922d011ade23ff582c9d63116`, not when implementation may begin.

This document defines what “complete” means. A long list is not proof of completeness: the
inventory must show which authoritative source surfaces were examined, which feature records they
produced, and why every remaining file or symbol is non-feature-bearing.

## 1. Canonical artifacts

The inventory uses four artifacts with different jobs:

1. `pi-feature-inventory.md` — raw primary-source census. It records what Pi has, without pi-go
   implementation status.
2. `feature-inventory-schema.md` — this schema and the mechanical completeness rules.
3. `parity-matrix.md` — normalized feature records plus pi-go class, target, owner, issue,
   acceptance and disposition.
4. A source-coverage ledger — every tracked source/document/example/build file in the approved Pi
   baseline is classified and either mapped to feature IDs or justified as non-feature-bearing.

The raw census may be reorganized without changing feature IDs. The parity matrix is the product
source of truth after normalization; the ledger is the evidence that nothing was silently skipped.

## 2. Stable feature IDs

Use lowercase dot-separated IDs:

```text
<area>.<surface>.<feature>
```

Add a fourth segment when a family contains independently observable members:

```text
<area>.<surface>.<family>.<member>
```

Examples:

- `wire.protocol.command.attach`
- `coding-agent.rpc.command.compact`
- `coding-agent.rpc.event.agent-settled`
- `coding-agent.tool.find`
- `agent-harness.tool.image`
- `extension.hook.tool-call`
- `ai.provider.openai-codex`
- `tui.component.editor`

Rules:

- IDs describe Pi capability, not a TypeScript filename or a pi-go implementation.
- Distinct commands, flags, tools, events, hooks, UI methods, providers, public APIs and data-format
  variants receive distinct IDs.
- The CBOR multi-session protocol and coding-agent JSON RPC mode are separate surfaces.
- Coding-agent built-in tools and Agent Harness tools are separate surfaces.
- Renames retain an `Aliases` entry; IDs do not churn merely because files move.
- One feature may cite several source files. Several independently observable features may not be
  collapsed into one row merely because they share an implementation.

## 3. Raw census record

Every enumerated Pi feature has these fields before normalization:

| Field | Requirement |
| --- | --- |
| `Feature ID` | Stable ID following §2 |
| `Package/area` | One owning package or root surface |
| `Kind` | `command`, `flag`, `mode`, `tool`, `event`, `hook`, `ui-api`, `public-api`, `workflow`, `data-format`, `provider`, `auth`, `usage`, `component`, or `engineering-evidence` |
| `Name` | Pi's user-facing or exported name |
| `Semantics` | One sentence describing observable behaviour, not merely the symbol name |
| `Pi evidence` | Pinned `path:line` and symbol where useful; multiple citations are allowed |
| `Docs evidence` | Pinned documentation citation, or `none` to expose source-only features |
| `Variants/constraints` | Modes, platforms, providers, inputs/outputs, lifecycle, errors, degradation and prerequisites |
| `Coverage state` | `enumerated`, `semantics-needed`, `schema-needed`, or `source-gap` |
| `Aliases` | Previous/user-facing names, or empty |

`enumerated` means the semantics and evidence are sufficient to normalize. It does not mean the
feature is implemented in pi-go.

## 4. Normalized parity record

Each raw feature becomes one parity record with these additional fields:

| Field | Requirement |
| --- | --- |
| `Class` | One or more of B/W/R/I; N may only add a pi-go requirement and never replace Pi parity |
| `Target` | The first release that must satisfy the feature |
| `Disposition` | `unknown`, `planned`, `compatible`, `compatible-via-adapter`, `accepted-deviation`, or `not-applicable` |
| `Owner` | Accountable human/agent or `unowned` |
| `Issue` | Executable issue, or an explicit reason it is not ticketed yet |
| `Acceptance` | Test/scenario/evidence that can prove the disposition |
| `Dependencies` | Other feature IDs or ADRs required first |
| `Migration/degradation` | Required for W/R items and every unsupported host mode |

`unknown`, `unowned`, `source-gap`, `semantics-needed`, `schema-needed`, and missing acceptance all
block the target release. They also block reopening implementation while the Phase 0 inventory hold
is active.

## 5. Source-coverage ledger

Every tracked file in the approved baseline must receive exactly one primary classification:

| Classification | Meaning |
| --- | --- |
| `feature-source` | Defines or implements one or more feature IDs |
| `public-contract` | Export barrel, schema, type union, protocol or persisted format |
| `documentation` | First-party documented behaviour; maps to feature IDs or a documented-only gap |
| `generated-surface` | Generated catalogue/data plus the generator that owns it |
| `test-or-eval` | Behavioural evidence mapped to feature IDs |
| `example-or-dogfood` | Demonstrates required capability or degradation |
| `build-install-release` | Packaging, installer, updater, migration or release behaviour |
| `supporting-internal` | No independent feature; must name the feature IDs it supports |
| `non-feature` | Administrative/static asset with a written reason |

Directories are not evidence. A package marked “enumerated” while tracked files inside it remain
unclassified is incomplete.

## 6. Completeness checks

The Phase 0 inventory is complete only when all checks pass.

### C0 — Baseline integrity

- Every citation resolves in `086c32e`.
- Census scripts/readers use `git show`, `git grep` or `git ls-tree` against that commit, not the
  local Pi working tree.
- Re-pinning requires explicit review and regenerates the ledger.

### C1 — Tracked-file closure

- Every tracked file under `packages/`, `.pi/`, `scripts/`, `.github/`, root install/test scripts
  and first-party docs appears in the source-coverage ledger.
- Every ledger row maps to feature IDs or contains a specific non-feature justification.
- File counts by top-level area reconcile with `git ls-tree` counts.

### C2 — Registry and union closure

All authoritative registries/unions are exhaustively expanded, including:

- CLI modes, flags and slash commands;
- both protocol command/event/error/state unions;
- both built-in tool registries and each tool's input/result/error schema;
- extension hooks, context methods, UI methods and host-mode degradation;
- provider registries, generated model catalogues, auth flows, request/stream conversion and
  usage/cost accounting;
- TUI public exports, components, input/key handling and rendering behaviours;
- session entry formats, lifecycle workflows and concrete backends;
- telemetry event/export surfaces, eval suites, installers/updaters and package scripts.

Counts are recorded beside their owning registry so a future addition makes the inventory stale
instead of silently disappearing.

### C3 — Source/docs bidirectional closure

- Every documented command, option, workflow and limitation maps to at least one feature ID.
- Every public source/API feature maps to documentation or is explicitly marked source-only.
- Docs-only claims without source evidence remain `source-gap`; source-only behaviours without
  semantics remain `semantics-needed`.

### C4 — Variant closure

Platform, provider, host mode, streaming/non-streaming, interactive/headless, local/remote and
session-backend variants are recorded when behaviour differs. A generic row may not hide a variant
with different inputs, lifecycle, error semantics or degradation.

### C5 — State/data-format closure

Persisted session formats, wire envelopes, RPC payloads, event payloads, content kinds, stop/error
reasons, model metadata and migration/version rules are inventoried as contracts, not implementation
details.

### C6 — Examples and dogfooding closure

Every example and `.pi/` dogfood artifact maps to the capabilities it proves. An example may share
feature IDs with product code, but it may not be omitted merely because it is not shipped as a
library API.

### C7 — Product-accounting closure

Every normalized feature has class, target, owner, issue strategy and acceptance. W/R items state
migration/degradation. Accepted deviations require the repository owner's explicit decision.

### C8 — Independent omission audit

An independent reviewer reruns the counts and registry extraction, samples citations, verifies zero
unclassified files and searches for exported unions/registries/docs headings not represented by a
feature ID. The audit records its exact baseline and result.

## 7. What an incomplete gate now blocks

An incomplete coverage axis or a failing C0–C8 check no longer pauses implementation. It blocks two
narrower things, which is what the checks were always measuring:

- **A parity claim.** No feature may reach a `compatible`, `compatible-via-adapter` or
  `accepted-deviation` disposition in `parity-matrix.md` while the axis it belongs to is
  incomplete — an unaudited feature is unaccounted for, however well it works.
- **Building that feature at all, if its own semantics are missing.** An axis marked
  `semantics-needed`, `schema-needed` or `source-gap` has not recorded the behaviour a port would
  have to reproduce, so implementing from it means implementing from a guess. Close the axis from
  the pinned tree first; this is per-axis, not repository-wide.

Completing every check remains the condition for declaring baseline parity, and that declaration is
still the owner's, reviewed against the finished artifacts.
