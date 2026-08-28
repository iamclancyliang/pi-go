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
go run ./cmd/pi -p "read README.md and summarise it"   # one-shot
go run ./cmd/pi                                        # a session, in a terminal
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

### Live provider tests

```bash
# Off by default. Reaches a real provider, spends a real credential.
PI_GO_LIVE_DEEPSEEK=1 DEEPSEEK_API_KEY=... go test ./conformance/ -run TestLive -count=1
```

The gate is a **separate variable from the credential**: having a key configured is not consent to
spend it, so `go test ./...` and CI skip these whether or not `DEEPSEEK_API_KEY` is set. The
credential enters only through the injected environment seam — never a literal, a file or a flag —
and the types that hold it refuse to format it.

Retry budgets are zero in these tests, so one model call is one billed request, and a counting
transport asserts that rather than trusting the configuration. A live failure is reported, not
retried: rerunning one in a loop is how a smoke test becomes a bill.

They exist because some facts cannot be established offline. Whether a real model, given a tool's
declared schema and nothing else, produces a call this repository can execute is a fact about the
provider — and it was false until `2026-08-28`, when a live run found the argument schema being
dropped between the framework and the model port. Every offline test passed throughout.

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

## Implementation priority

**The implementation hold of 2026-08-17 was lifted by the repository owner on 2026-08-28.** Feature
parity with the approved Pi baseline is the current priority, and implementation no longer waits on
the feature-level inventory.

What changed is the inventory's job, not its standard. It was a precondition: no feature code until
the C0-C8 checks passed for the whole baseline. It is now the parity *audit* — it records what Pi
has and what pi-go has yet to answer for, and it runs alongside implementation instead of ahead of
it. The reason is measured rather than impatient: closing C0-C8 is tens of hours of census work,
including a source-coverage ledger that does not yet exist, and none of it puts a working feature in
front of a user. Counting the denominator and building against it are separable, and only the first
was ever the blocking one.

Two obligations survive the lift, because they are what the hold was actually protecting:

- **Build against recorded evidence, not against a memory of Pi.** A feature whose semantics are
  `semantics-needed`, `schema-needed` or `source-gap` in the coverage ledger of
  `docs/product/pi-feature-inventory.md` is not ready to implement. Close that axis first, from the
  pinned tree.
- **Do not claim parity from an implementation.** A `compatible` disposition in
  `docs/product/parity-matrix.md` still requires the acceptance evidence that file defines. Shipping
  a tool is not the same as accounting for it.

Work order is by user-visible value, coarsest first: the built-in tool set, then a real CLI and its
turn loop, then session persistence and slash commands, then the TUI, then the RPC/server/client and
extension surfaces.

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
  cli/          how a command line becomes a run: flags, mode resolution,
                provider selection, and the modes themselves. Here rather than
                in cmd/ so it is testable without building a binary
cmd/          composition roots — assemble modules, no behaviour of their own
  pi/            the agent
  pi-tracer/     the v0 contract tracer bullet
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
