# AGENTS.md

Project rules for both humans and agents working in this repo.

`pi-go` is a Go reimplementation of [pi](https://github.com/earendil-works/pi), built on the
[cloudwego/eino](https://github.com/cloudwego/eino) framework.

> Development rules are added as conventions are actually established and verified — not copied
> from another project up front.

## Toolchain

**Declared minimum: Go 1.25.** This is a deliberate decision, **not** whatever is installed
locally: it keeps us on two currently-supported toolchain generations without inheriting eino's
historical `go 1.18` floor. CI should run **1.25.x and current stable 1.26.x** — otherwise the
declared minimum has no evidence behind it. Raise it only when the tracer bullet demonstrably needs
a newer language or standard-library feature.

Verified locally on go1.26.6 darwin/arm64 against declared minimum 1.25.

`.github/workflows/ci.yml` runs the same gates on Go 1.25.x and 1.26.x. The workflow file existing is
not itself proof that the minimum is supported: the toolchain gate closes only after both hosted
matrix jobs pass on the committed revision.

**Gate CLOSED 2026-08-15.** Both matrix jobs (`Go 1.25.x`, `Go 1.26.x`) succeeded on `75cc9d3`
(run `31884705936`). The declared minimum of 1.25 now has hosted evidence behind it, not just a
local run. This closure is tied to that revision — it is not a standing guarantee for later commits,
which is what CI on every push is for.

## Commands

Every command below was run in this repository and passed before being documented here. Do not add
a command to this list that has not actually been run.

```bash
gofmt -l .          # format check — empty output means clean
go vet ./...        # static checks
go build ./...      # build
go test ./...       # tests
go test -race ./... # tests under the race detector
go mod verify       # verify dependency checksums
go mod tidy         # must produce no diff to go.mod / go.sum
```

`go mod tidy` producing a diff is a failure, not a fix: it means the committed module files did not
match the source.

## Commit identity

All commits use the repository owner's GitHub-linked identity:
`clancyliang <37497641+iamclancyliang@users.noreply.github.com>`. Configure this identity in the
repository-local Git config and verify both `git var GIT_AUTHOR_IDENT` and
`git var GIT_COMMITTER_IDENT` before committing. Agent identities — including Claude Code, Codex,
and other automation names or emails — must not appear as the author or committer.

Do not rewrite already-pushed history solely to change old identities unless the repository owner
explicitly requests it.

## Public code and commit language

Code comments and docstrings must explain behavior and rationale in place. Do not make readers
decode internal collaboration context such as agent names, task/review narratives, acceptance IDs,
ADR/PRD labels, or document section markers. Replace those references with the actual constraint or
tradeoff the code must preserve.

Commit subjects and bodies follow the same rule: describe the change, its user/engineering reason,
and verification without agent attribution, task routing, review dialogue, or internal planning
document references. Formal documents under `docs/` may cite and cross-reference ADRs, the PRD and
their sections because those references are part of the documents' purpose.

## Implementation hold

**Product implementation is paused by the repository owner as of 2026-08-17.** The approved Pi
baseline has a top-level parity denominator, but not yet a complete feature-level inventory. Work
may continue on the pinned-source census, feature schema, parity/traceability documents, omission
audits, read-only review, and repository maintenance required for that inventory. Preserve existing
product-code WIP without extending or publishing it.

Before inventory work or any request to resume implementation, read
`docs/product/feature-inventory-schema.md`. Implementation resumes only after its C0-C8 checks pass
for the whole baseline and the repository owner explicitly lifts this hold.

The current `main` includes pre-hold A1-A3 tracer work with open NO-GO review findings. Its presence
is not evidence that a contract, module boundary or release gate is accepted.

## Repository layout

```
internal/     product code — every module from architecture §1
  events/       observable event contract; zero dependencies
  tools/        tool registration seam + v0 fixture tools
  ai/           model port; eino-free. The framework is HIDDEN BEHIND it (edge E1),
                so no signature here may name an eino type -- a constructor that
                returns one moves the dependency into every caller instead
  session/      conversational truth vs projection
  runtime/      agent loop on eino adk.TurnLoop (edge E2); owns events + seams
cmd/          composition roots — assemble modules, no behaviour of their own
conformance/  acceptance-scenario tests (A1…); inside the module by necessity
spikes/       isolated capability experiments only
```

Rules that are enforced, not aspirational:

- **No product module may import `spikes/`.** Spike code must not become the architecture by
  default.
- **eino types must not escape.** `internal/ai` hides eino's model types; `internal/runtime` hides
  eino's loop types. Neither re-exports them (ADR-0001), which is what keeps ADR-0002 reversible.
- **There is still no public SDK package.** ADR-0001 defers its name and contents until after the
  v0 tracer bullet, so exports are justified by a real consumer rather than guessed.
- **`conformance/` lives inside this module** because `internal/` is unimportable from outside it.

### Handler order is a correctness constraint

eino composes `WrapModel` handlers **lazily, outermost-first in registration order**. A handler that
substitutes the model never calls through, so **every handler registered after it is never invoked —
no error, no trace** (`spikes/einoprobe`, `TestWrapModelCompositionOrder`). pi's per-turn model
selection is exactly such a substitution. Register model selection **last (innermost)**, and keep
the `Handlers` slice in `internal/runtime/loop.go` the single place that order is decided.

## Agent skills

### Issue tracker

GitHub issues in `iamclancyliang/pi-go`, driven by the `gh` CLI. Write access is available here
(unlike the read-only upstream `pi` study checkout). See `docs/agents/issue-tracker.md`.

### Triage labels

The five canonical triage labels (`needs-triage`, `needs-info`, `ready-for-agent`,
`ready-for-human`, `wontfix`). GitHub's stock `question` label is kept separate, for ordinary
questions rather than triage state. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context — one root `CONTEXT.md` plus `docs/adr/`, created lazily by `/domain-modeling`.
Expect ADRs to carry more weight than usual here: most decisions take the form "pi does X — keep
it, use an eino mechanism, or build our own?". See `docs/agents/domain.md`.
