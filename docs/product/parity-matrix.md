# pi-go parity matrix

**Status:** implementation active · this matrix is the parity audit, not a precondition for it
**Last reconciled against the code:** 2026-09-04 (json/rpc mode and model-listing rows, deviations D-15–D-17, `--mode json` and the `--mode rpc` channel shipping); 2026-09-02 (provider evidence rows); 2026-08-29 (everything else)

**Approved source baseline:** `earendil-works/pi@086c32e74530564922d011ade23ff582c9d63116` (approved 2026-08-15; re-pin requires explicit review)
**Product requirement:** complete Pi feature accounting with no silent omissions

**Inventory authority:** @qy-liang decided on 2026-08-17 that the top-level denominator was not
enough to continue implementation, and lifted that hold on 2026-08-28. Implementation now proceeds
against axes whose semantics are already recorded, while the raw census, this matrix and the
source-coverage ledger continue toward every check in
`docs/product/feature-inventory-schema.md`. Satisfying those checks is still what lets pi-go claim
parity; it is no longer what lets it write feature code.

## How to use this matrix

`docs/product/pi-feature-inventory.md` is the raw primary-source census. This file normalizes that
census into product accounting; it must not invent missing Pi semantics or treat a package-level row
as feature-level coverage. Stable IDs, required fields and completeness checks are defined in
`docs/product/feature-inventory-schema.md`.

Every Pi feature or engineering surface must have:

- a pinned source/document reference;
- an implementation class;
- a target release;
- an owner and issue;
- reproducible acceptance evidence;
- a final status of `compatible`, `compatible-via-adapter`, or `accepted-deviation`.

`unknown`, `unowned`, and `blocked` prevent the parity release.

### Implementation classes

| Code | Meaning |
| --- | --- |
| B | Behaviour-compatible Go reimplementation; internal source shape may differ |
| W | Wire/data compatibility or a lossless migration adapter is required |
| R | Capability-equivalent redesign is expected because the runtime/language mechanism differs |
| I | Engineering equivalent for internal generation, testing, packaging, or evaluation infrastructure |
| N | Net-new pi-go requirement with no released Pi counterpart; requires its own PRD/ADR rationale and acceptance evidence |

An item can have multiple classes. `R` does not waive parity: it says the mechanism changes while
the user capability still needs acceptance evidence. `N` is an additional product gate, not parity
evidence; it cannot replace `B`, `W`, `R`, or `I` for a Pi surface.

## Initial inventory

This is the approved top-level denominator, not the feature-by-feature completion claim. It remains
as an index while the raw census is normalized into stable feature IDs. No row below is evidence
that its area is completely inventoried unless the source-coverage ledger and the corresponding
feature records are complete.

Statuses were reconciled against the code on 2026-08-29; the feature-level rows below carry the
evidence. `implemented` means the surface exists with acceptance evidence and an owner, and is not
by itself a parity claim — the open issues named beside each area are what stands between it and a
final disposition.

| Area | Pi source surface | Class | Target | Initial acceptance boundary | Status |
| --- | --- | --- | --- | --- | --- |
| Agent loop and messages | `packages/agent` | B | v0 | C1-C8 conformance traces, streaming and cancellation | in-spec |
| Tool execution | `packages/agent`, coding-agent tools | B | v0/v1 | validation, batch modes, ordering, abort, side-effect policy | implemented · #25 open |
| Model/provider layer | `packages/ai` | B/I | v1-v2 | provider calls, message conversion, streaming, auth, usage/cost, generated catalogue | partial · #30, #33 open · denominator set by ADR-0007 |
| Coding-agent assembly | `packages/coding-agent` | B | v1-v2 | documented commands, settings, modes, resources, tools and workflows | partial · #27, #28, #30 open |
| Terminal UI library | `packages/tui` | B/R | v2 | rendering, input, overlays, width handling, streaming UX | partial · #28 open |
| Sessions and compaction | coding-agent session/compaction; AgentHarness | B/W/N | v1-v2 | resume, tree, fork/clone, summaries, context projection, migration; net-new crash-safe replay policy | implemented · #26 open |
| Session storage port | session abstractions and harness storage contract | B/R | v1 | narrow persistence interface, atomicity/recovery needs, in-memory conformance implementation | implemented |
| Concrete session backends | `packages/session-backends/*` | B/R/W | v3 | file/sqlite backends, migrations, leases and recovery | inventory-needed |
| Extensions | coding-agent extensions + examples | R/W | v0 seam / v2 product | v0 core seams for tools/events/policy/state/capabilities; v2 discovery, UI, lifecycle, degradation and TS migration; evaluate pigo's Node-host bridge without treating its no-op capabilities as parity | architecture-risk |
| Skills and prompt resources | coding-agent docs/resource loader | B/W | v2 | user/project discovery, precedence, invocation and compatible formats | inventory-needed |
| Themes | coding-agent themes | B/W | v2 | discovery, selection, rendering semantics and migration | inventory-needed |
| Pi packages / package manager | coding-agent package surface | B/R/W | v2 | install/update/remove, resource contribution, versioning and migration | inventory-needed |
| RPC mode | coding-agent RPC docs/implementation | B/W | v3 | bidirectional JSONL commands/events: prompt/steer/follow-up; state/model/thinking/queue modes; compaction/retry/bash; session switch/fork/clone/tree; extension UI; errors, ordering and degradation | inventory-needed |
| Protocol | `packages/protocol` | B/R | v3 | pi-go-native framing/schema/errors/events with semantic coverage of Pi's remote capabilities; direct Pi-wire interoperability and a live adapter are explicit non-goals under decision C; pigo provides no implementation shortcut | decision-set/inventory-needed |
| Client | `packages/client` | B/R | v3 | native remote-session control and error semantics; C4/C4.0 event ordering and C6 unmatched calls; unchanged Pi client connection is explicitly unsupported and tested/documented | decision-set/inventory-needed |
| Server | `packages/server` | B/R | v3 | native session hosting/lifecycle and compatibility with the pi-go client/protocol; Pi-wire migration/incompatibility boundary is explicit | decision-set/inventory-needed |
| Telemetry | `packages/telemetry` | B/R | v1-v3 | event/callback meaning, usage attribution and observability | inventory-needed |
| Behavioural evaluations | `packages/evals` | I | Phase 0-parity | equivalent regression/evaluation coverage for compatibility claims | inventory-needed |
| Installation and update | coding-agent installer/update paths | I/R/W | v2-parity | distribution, update, rollback and compatible user data handling | inventory-needed |
| Examples and dogfooding | examples, `.pi/` | I | rolling | representative examples run or have documented Go equivalents | inventory-needed |
| Documentation | package docs and product docs | B/I | rolling | every shipped compatible surface is documented for Go users | inventory-needed |

## Implemented surface, reconciled 2026-08-29

Feature-level rows for what pi-go actually has. Written by reading the code and
running its tests, not from the commit log — a matrix derived from what was
claimed at merge time drifts the first time something is changed afterwards.

**Owner: @qy-liang** for every row — this repository has one. Assigned
2026-08-29, which clears the `unowned` condition of §C7.

**These rows are still not a parity claim.** What each says is that the surface
exists and its evidence runs. Reaching `compatible` needs the incomplete
deviations closed (#25, #26, #27, #28). The provider rows' open question, #29,
was closed on 2026-08-29 by an authorized probe. Every `incomplete` row below names the issue that tracks it —
`docs/product/feature-inventory-schema.md` §C7 refuses a row that is blocked
without saying by what.

Evidence is reproducible: each row names a test that fails when the behaviour it
describes is removed. Every offline test runs under `go test -race ./...`; a
live row needs that provider's own `PI_GO_LIVE_*` gate and a credential, and CI
never runs them. Every one of the nine ports now carries such a test —
`TestEveryProviderPortHasALiveTest` fails if one does not — but **having the
test is not having the evidence**: which ports have actually reached their
provider is what the rows below say.

### Built-in tools — `coding-agent.tool.*`

Pi source: `packages/coding-agent/src/core/tools/`. Class B. Target v1.

| Feature ID | pi-go | Acceptance evidence | Proposed |
| --- | --- | --- | --- |
| `coding-agent.tool.read` | `internal/tools/read.go` | `TestContinuingFromTheOffsetGivenReassemblesTheFile` — following the offsets reassembles the file exactly | compatible |
| `coding-agent.tool.ls` | `internal/tools/ls.go` | `TestListingIsSortedTheWayAPersonReads`, `TestTheEntryLimitSaysHowToSeeMore` | compatible |
| `coding-agent.tool.find` | `internal/tools/find.go` | `TestIgnoredFilesStayOut`, `TestAPatternWithoutASlashMatchesAtAnyDepth` | compatible-via-adapter |
| `coding-agent.tool.grep` | `internal/tools/grep.go` | `TestTheSeparatorSaysWhichLineMatched`, `TestSearchingIgnoresWhatGitIgnores` | compatible-via-adapter |
| `coding-agent.tool.write` | `internal/tools/write.go` | `TestWritingReplacesTheWholeFile`, `TestOneFileIsMutatedByOneCallAtATime` | compatible |
| `coding-agent.tool.edit` | `internal/tools/edit.go` | `TestEveryEditIsMatchedAgainstTheOriginalFile`, `TestOverlappingEditsAreRefused` | compatible, less D-2 |
| `coding-agent.tool.bash` | `internal/tools/bash.go` | `TestOutputIsTruncatedFromTheEnd`, `TestKillingACommandKillsWhatItStarted` | compatible |
| tool argument schemas (§15) | `internal/tools/schema.go` | `TestAToolsArgumentSchemaReachesTheWire` asserts the bytes sent; `TestLiveDeepSeekCallsTheReadToolFromItsDeclaredSchema` asserts a real model uses them | compatible |

`find` and `grep` are `compatible-via-adapter` rather than `compatible` because
Pi shells out to `fd` and `ripgrep`, downloading them when absent. pi-go has no
binary-fetching machinery, so the behaviour is implemented in Go: glob matching,
`.gitignore`, hidden files, base-name versus full-path patterns. What a caller
observes is meant to match; how the files are reached does not. See D-1.

### Modes and the command line — `coding-agent.mode.*`, `coding-agent.flag.*`

Pi source: `src/main.ts:118-133`, `src/cli/args.ts`. Class B. Target v1.

| Feature ID | pi-go | Acceptance evidence | Proposed |
| --- | --- | --- | --- |
| `coding-agent.mode.interactive` | `internal/cli/run.go` `RunInteractive` | `TestInteractiveAnswersEachLineAndEndsAtEOF` | compatible, less D-7 |
| `coding-agent.mode.print` | `internal/cli/run.go` `RunPrint` | `TestPrintWritesTheAnswerToStdoutAndNothingElse`, `TestPrintReportsAFailureOnStderrAndInTheExitCode` | compatible |
| `coding-agent.mode.json` | `internal/cli/run.go` `RunJSON`, `internal/jsonstream/` | `TestOneCounterSpansBothFamilies` (confirmed to fail with a second counter), `TestTheStreamCarriesTheReplyAndItsLifecycle`, `TestAReplyLineNeverCarriesTheSnapshot`, `TestJSONModeWritesOnlyTheStreamToStdout` | partial — the stream ships per ADR-0009 (D-16): version line, both families, one `seq`. **Not Pi's shape and not its coverage**: 8 of Pi's 24 event capabilities have native equivalents; the missing 15 are the features' own gaps (#28, #32, #36, #39, extensions), enumerated in ADR-0009's table |
| `coding-agent.mode.rpc` | `internal/cli/run.go` `RunRPC`, `internal/rpc/` | `TestACommandWithoutAnIdIsRefused`, `TestAPromptsResponseAndItsEventsShareOneOrder`, `TestAbortEndsARunningPromptAndTheStreamSaysAborted`, `TestSteerAndFollowUpReachTheRunInFlightAndNothingElse`, `TestASecondPromptWhileOneRunsIsBusy`, `TestTheOrderIsTheWriteOrderUnderConcurrency` (confirmed to fail 5/5 with `seq` allocated outside the write lock), `TestAnUnknownVerbAndAnUnbuiltOneAreDifferentAnswers`, `TestAProviderFailureAtStartKeepsItsClassification` | partial — the channel ships per ADR-0010 (D-17): required ids, responses on the stream's one `seq`, typed failures. A prompt runs on its own goroutine while stdin is read, so nine commands answer natively — `prompt`, `abort`, `steer`, `follow_up`, `get_state`, `get_messages`, `get_session_stats`, `get_last_assistant_text`, `set_session_name`; a second prompt during a run is `busy`, not queued. The rest fail with a typed kind that separates unknown from unbuilt, each unbuilt one tracked (#28, #30, #32, #36, #39, #40, #41) |
| mode resolution (§2.1) | `internal/cli/mode.go` | `TestTheTerminalIsHalfTheDecision`, `TestModeTextMeansLetTheEnvironmentDecide` | compatible |
| CLI flags | `internal/cli/args.go` | `TestTheFlagsThisBuildActsOn`, `TestAPiFlagThisBuildLacksIsSaidAloud` | partial — 13 of 40 acted on; the rest warn rather than being silently ignored |

### Slash commands — `coding-agent.slash.*`

Pi source: `src/core/slash-commands.ts:20-41` and the handlers in
`src/modes/interactive/interactive-mode.ts`. Class B/R. Target v1-v2.

21 of Pi's 22 are implemented. Acceptance for the set: `internal/cli/commands_test.go`.

| Feature ID | pi-go | Proposed |
| --- | --- | --- |
| `session`, `new`, `resume`, `quit`, `help` | `internal/cli/commands.go` | compatible |
| `tree`, `fork`, `clone` | `internal/cli/commands.go` + `internal/session/filestore.go` | compatible-via-adapter — selectors are line-based, not Pi's TUI pickers |
| `compact` | `internal/compaction/` | compatible, less D-3 |
| `name`, `export`, `import`, `copy` | `internal/cli/commands.go` | compatible, less D-4 (import), D-5 (export/share format) |
| `share` | `internal/cli/commands.go` | compatible, less D-5, D-6 |
| `model` | `internal/cli/commands.go` | partial — the `<provider/model>` argument form; the selector needs the provider-reported listing (ADR-0008, accepted, unimplemented) and the full-screen picker (#28). Accepting a name no listing contains is D-15, not a gap |
| `login`, `logout` | `internal/auth/` | partial — API keys; Pi's OAuth flows are not ported |
| `settings` | `internal/settings/` | partial — 9 keys, not Pi's 49; see D-8 |
| `trust` | `internal/trust/` | compatible-via-adapter — the decision and its nearest-ancestor rule are Pi's; what it gates is pi-go's own resource set |
| `reload` | `internal/cli/settings_cmd.go` | partial — settings only; Pi also reloads extensions, skills, prompts, themes, context files |
| `hotkeys` | `internal/tui/bindings.go` | partial — the editor's keys; Pi's app-level chords need the full interface |
| `changelog` | `CHANGELOG.md`, embedded | compatible |
| `scoped-models` | — | **incomplete** (#30) — the data source is decided: ADR-0008, a provider-reported listing asked for on demand. What still blocks it: the listing is unimplemented, the selector is a full-screen surface (#28), and Pi's selector semantics were never read |

### Sessions — `coding-agent.session.*`

Pi source: `src/core/session-manager.ts`, `agent-session-runtime.ts`. Class B/W/N. Target v1-v2.

| Feature ID | pi-go | Acceptance evidence | Proposed |
| --- | --- | --- | --- |
| durable session record | `internal/session/filestore.go` | `TestAConversationOutlivesTheProcessThatWroteIt`, `TestAppendingIsAllOrNone`, `TestEveryKindOfEntrySurvives` | compatible-via-adapter — format is pi-go's own under ADR-0006 (D-9) |
| discovery, `--continue`, `--resume` | `internal/session/discovery.go`, `internal/cli/session.go` | `TestSessionsAreGroupedByWhereTheyRan`, `TestAnEmptySessionIsNotWhatContinueResumes`, live `TestLiveDeepSeekRemembersAcrossTwoRuns` | compatible |
| session tree, branch, fork | `internal/session/filestore.go` | `TestBranchingLeavesTheOldLineWhereItWas`, `TestForkingCopiesIntoANewFileAndLeavesTheOldOne`, live `TestLiveDeepSeekDoesNotSeeAnAbandonedBranch` | compatible |
| compaction | `internal/compaction/` | `TestACutNeverSeparatesAToolCallFromItsResult`, live `TestLiveDeepSeekSummarisesAConversation` | compatible, less D-3 |
| session name | `internal/session/store.go` | `TestNamingAConversationOutlivesTheRun` | compatible |
| labels, branch summaries, `session_info` beyond name | — | — | **incomplete** (#32) — entry kinds not ported |

### Providers — `ai.provider.*`

Pi source: `packages/ai`. Class B. Target v1-v2.

| Feature ID | pi-go | Acceptance evidence | Proposed |
| --- | --- | --- | --- |
| DeepSeek port | `internal/provider/deepseek/` | 115 tests in `internal/provider`; live `TestLiveDeepSeekAnswersAndReportsWhatItSpent`, `TestLiveDeepSeekCallsTheReadToolFromItsDeclaredSchema` and the rest of the live suite have run against the provider | compatible |
| OpenAI and Qwen ports | `internal/provider/{openai,qwen}/` | offline: 115 tests in `internal/provider`, plus `TestQwenToolsReachTheProvider` and the e2e runs through the real agent. Live: `TestLiveOpenAIAnswersAndReportsWhatItSpent`, `TestLiveQwenAnswersAndReportsWhatItSpent` and their tool-schema pair, **written and skipping until a credential exists** | partial — **unverified-against-provider**. These two rows read `compatible` from 2026-08-29 while carrying no live test at all, which was a weaker evidential position than the rows that say plainly they are unverified. The gated tests were added on 2026-09-02 (`TestEveryProviderPortHasALiveTest` now refuses a port without one); the disposition returns to `compatible` when one of them has run |
| one call, one billed request | `internal/provider/*` | `TestOneCallSendsOneRequest`, counted at the transport in every live test; two vendor SDKs retry by default and are switched off — `internal/provider/claude/retry.go` and Ark's `RetryTimes` — each of which would otherwise make one call three | compatible |
| usage accounting, absent vs zero | `internal/ai/counts.go` | `TestAFailedAttemptThatReportedNothingIsStillAnAttempt`; live run shows `cache_read=0` as a measured zero | compatible |
| stored credentials | `internal/auth/` | `TestACredentialRefusesToFormatItself`, `TestTheFileIsNotReadableByOthers` | partial — API keys only; Pi's OAuth flows are `semantics-needed` |
| context-overflow recovery | `internal/provider/deepseek/overflow.go`, `internal/compaction/` | `TestTheRecordedRejectionIsRecognisedAsAnOverflow` against the recorded rejection; `TestAnOrdinaryBadRequestIsNotAnOverflow` | compatible |
| model facts (window, output cap, reasoning) | `internal/ai/catalogue.go` | `TestTheMeasuredDeepSeekFactsAreWhatWasMeasured`, `TestAnUnrecordedModelSaysSoRatherThanAnsweringZero` | accepted-deviation — ADR-0007: owned, sourced per entry, one model recorded. Pi's generated catalogue is a `source-gap` (§7.2) and is not reproduced |
| full per-provider model list | — | — | **incomplete** (#30) — split by ADR-0008: identifiers come from the provider when a person asks (unimplemented), and the facts stay owned per ADR-0007, so a "full list with facts" is deliberately never reproduced |
| OpenRouter, Ollama, Claude, Ark, Gemini, Qianfan ports | `internal/provider/{openrouter,ollama,claude,ark,gemini,qianfan}/` | `TestAModerationRefusalIsNotAnAuthenticationFailure`, `TestAModelThatWasNeverPulledSaysHowToFixIt`, `TestTheStatusesAndTypesThisProviderDocuments`, `TestTheStatusesThisPortClassifiesOn`, `TestTheCanonicalStatusNamesThisProviderSends`, `TestTheBackendIsNotChosenByTheEnvironment`, `TestTheCodesThisProvidersOwnSDKNames`, `TestOneCallSendsOneRequest` | partial — **unverified-against-provider** except Qianfan, which answered a live call on 2026-08-31: no credential exists for the others and no server for Ollama, so the wire semantics are this repository's reading of each vendor SDK's own types and of wordings pi recorded at the pin. A live test per provider is written and skips until a credential exists |
| Qianfan reached without its component | `internal/provider/qianfan/` | `TestTheCompatibleEndpointIsWhatIsReached`, `TestAStreamThatRenumbersItsToolCallsIsRefused`, `TestAModelThisAccountCannotUseIsNotReportedAsABadCredential`, `TestAnAccountInArrearsIsNotReportedAsABadCredential`; **live** `TestLiveQianfanAnswersAndReportsWhatItSpent` ran against the provider on 2026-08-31 | accepted-deviation (#38) — eino-ext's Qianfan component builds its own HTTP client and keeps credentials in a process-wide singleton, so a port on it could not count its requests, classify a refusal from the body, or be tested without a credential. This port speaks the provider's OpenAI-compatible v2 endpoint through the shared dialect instead. ADR-0007 settles which providers are reached, not which component reaches them. **Partially verified**: one streamed completion answered with usage read off the wire; tool calling is untested because the account went into arrears |

### Terminal interface — `tui.*`

Pi source: `packages/tui`, `src/modes/interactive/`. Class B/R. Target v2.

| Feature ID | pi-go | Acceptance evidence | Proposed |
| --- | --- | --- | --- |
| key decoding | `internal/tui/keys.go` | `TestDecodingTheKeysTheBindingsName`, `TestAReadEndingMidSequenceWaitsForTheRest` | compatible |
| line editor, kill ring, undo, history | `internal/tui/editor.go` | `TestConsecutiveKillsYankBackAsOne`, `TestHistoryBrowsingKeepsTheLineBeingWritten` | compatible |
| key assignments | `internal/tui/bindings.go` | table is Pi's verbatim for the actions implemented | compatible |
| full-screen interface, chat rendering, selectors, overlays | — | — | **incomplete** (#28) — the bulk of `packages/tui` (16.7k lines) and `interactive-mode.ts` |

## Deviation register — decided 2026-08-29

Each is a place pi-go behaves differently from Pi. **@qy-liang decided these on
2026-08-29**, splitting them the way §C7 requires: an `accepted-deviation` is
permanent and its row may reach a final disposition; an `incomplete` is a gap
that will be closed and blocks its row until it is.

The split is not about how defensible each difference is — each had a reason at
the point it was made. It is about whether the difference is meant to last.
D-15–D-17 were added on 2026-09-04, decided with ADR-0008, ADR-0009 and
ADR-0010.

### Accepted deviations — permanent

| # | Where | Pi | pi-go | Why it stands |
| --- | --- | --- | --- | --- |
| D-1 | `find`, `grep` | shells out to `fd`/`ripgrep`, downloading them if absent | implements the behaviour in Go | no binary-fetching machinery; adding one is a distribution feature, not a search feature |
| D-4 | `import` | confirms before replacing the running session | does not confirm | the conversation being left is already on disk and resumable; nothing is lost |
| D-6 | `share` | uploads without asking | asks first, only an explicit yes proceeds | a coding conversation carries source and tool output; a secret gist is unlisted, not private, and cannot be recalled |
| D-8 | settings | 49 keys | 9, each read by something | a key that parses and does nothing is a setting the user believes is on |
| D-9 | session files, agent directory | `~/.pi/agent`, Pi's JSONL shape | `~/.pi-go/agent`, pi-go's own shape | ADR-0006 gives pi-go a native wire and rules out interoperability; a shared directory would offer conversations the other program cannot read |
| D-10 | `read` | tries macOS filename fallbacks (NFD, curly quotes, narrow no-break space) | fails as a missing file | a path that quietly resolves to a different file is worse than one that fails |
| D-11 | `--mode <invalid>` | ignored silently | ignored, with a warning | `--mode interactive` names the one mode the flag rejects and would otherwise look like it worked |
| D-12 | unimplemented Pi flags and commands | — | warn, naming the reason | a flag a user believes took effect is the failure a parser must not have |
| D-15 | `/model <name>` | an exact match against the catalogue sets the model; no match opens the selector filtered by the term | an exact name switches whether or not any listing contains it; the listing is discovery, not an allowlist | ADR-0008 — being unable to enumerate is not evidence a name is invalid, and a provider whose listing call fails must not take its working models with it |
| D-16 | JSON event stream | one stream; content deltas arrive inside `message_update`, whose cumulative snapshot `toJsonEvent` strips on the way out | two families on one stdout — lifecycle and reply lines — correlated by a shared `seq`; the reply snapshot (`ai.StreamEvent.Partial`, kept for renderers) is likewise not serialised | ADR-0009 as corrected — merging would drown the lifecycle events in deltas (`loop.go:1205`) and buys only a compatibility ADR-0006 rules out. Takes effect with `--mode json` |
| D-17 | RPC requests | `id` optional; a response without one is attributable only by arrival order | `id` required, echoed on every response | ADR-0010 — correlation by position is a latent bug, not a convenience worth keeping. Takes effect with `--mode rpc` |



### Incomplete — a gap that will be closed

These block their rows from a final disposition. Each has an issue; none is a
statement that Pi's behaviour is wrong.

| # | Issue | Where | Pi | pi-go | What closing it needs |
| --- | --- | --- | --- | --- | --- |
| D-2 | #25 | `edit` | falls back to fuzzy matching (trailing whitespace, smart quotes) and overlays the change to keep unchanged bytes | exact matching only; a miss is reported | a partly correct overlay silently rewrites regions nobody touched, which for an editing tool is worse than not matching |
| D-3 | #26 | `compact` | may cut inside a turn and summarise the dropped prefix separately | cuts only at turn starts, keeping slightly more | a tool result must follow its call; cutting between them produces a request the provider refuses |
| D-5 | #27 | `export`, `share` | HTML by default | Markdown | there is no HTML renderer; sharing something this build cannot produce would be worse |
| D-7 | #28 | interactive mode | full-screen interface | line-based loop with an editing prompt | the interface is a separate workstream; a half-drawn one is worse than an honest prompt |
| D-14 | #37 | context-overflow detection | one shared matcher over six recorded wordings, applied to every provider | per-port detection: two-number comparison (deepseek), error code (qwen, openai), pi's phrase (ollama), up-front counts where a window is recorded | a provider whose wording nobody recorded reports an overflow as an ordinary refusal, losing the one failure this repository can recover from. Pi's six positives and four negatives are recorded in its own tests and can be adopted on that basis |

`D-13` was reserved for an up-front context-overflow rejection pi-go could not
recognise. **It is no longer needed** (#29 closed 2026-08-29): an
owner-authorized probe recorded a real rejection, and
`internal/provider/deepseek/overflow.go` recognises it by comparing the two
token counts the message carries. The provider-contract rows are unblocked. See
`docs/research/provider-contract-source-audit.md`, "Probe result".

## Native remote-capability checklist (wire decision C)

ADR-0006 rejects live Pi wire interoperability. Therefore “a remote client can send input” is not
enough evidence for RPC/protocol/client/server parity. Each capability family below must end as
`native-equivalent`, `accepted-deviation`, or `incomplete`; `incomplete` blocks the relevant parity
release. The eventual feature inventory expands these families into individual commands, events,
errors, and state fields.

| Capability family | Pinned Pi evidence | pi-go acceptance boundary |
| --- | --- | --- |
| Connection and envelopes | `packages/protocol/src/schemas.ts`: protocol version, client/server hello, request id, success/error response, event envelope | native version negotiation, correlation, typed errors, and event delivery are specified and tested; no claim that Pi framing/schema is accepted |
| Server/session inventory | protocol `list`, `create`, `server_snapshot`, `session_removed`; server session registry | list/create/removal and metadata revisions have native equivalents with deterministic lifecycle semantics |
| Attachment and ownership | protocol `attach`, `detach`; session `attached`/`locked` state | native client ownership, contention, detach, and reconnect behavior are explicit; deviations from Pi locking are itemized |
| Live session control | protocol `prompt`, `steer`, `abort`, `set_model`, `set_thinking` | equivalent control points exist and preserve steering timing, cancellation, per-turn model/thinking changes, and error categories |
| Snapshot and transcript projection | server/session snapshots; transcript item/progress schemas | authoritative snapshots plus started/delta/updated/finished progress are reconstructable; C4/C4.0 ordering and C6 unmatched tool calls are preserved |
| RPC prompt lifecycle | `rpc-types.ts`: `prompt`, `steer`, `follow_up`, `abort`, `new_session`, `get_state`, `get_messages` | native headless surface covers input queues, state inspection, and terminal/error behavior without collapsing steer and follow-up semantics |
| RPC model and queue policy | `set_model`, `cycle_model`, model inventory; thinking-level commands; steering/follow-up modes | every setting has a native equivalent or explicit deviation, including supported-value discovery and mode-specific queue behavior |
| RPC compaction, retry, and shell | `compact`, auto-compaction, auto-retry/abort-retry, `bash`/`abort_bash`, session stats | native operations expose lifecycle, cancellation, typed failure, and usage evidence; compaction obeys S1–S6 |
| RPC session navigation and export | switch, fork, clone, fork messages, entries, tree, last assistant text, session name, HTML export | tree/navigation/export capabilities are individually accounted for; data-format differences require migration documentation rather than silent omission |
| Resource commands and extension UI | `get_commands`; extension UI request/response methods for select/confirm/input/editor/notify/status/widget/title/editor text | discovery and interactive methods are individually accounted for, with unsupported headless capabilities declared as degradation rather than no-op parity |

The source unions are the starting denominator, not the final specification. The G5 native-wire
contract owns framing, state machines, errors, and delivery rules; this checklist prevents that new
protocol from shrinking the Pi capability set unnoticed.

## Known parity risks

### Extensions are not a source-level port

Pi loads TypeScript extensions at runtime and gives them in-process access to hooks, tools, state,
and UI APIs. Go cannot natively reproduce that loading model. The architecture must choose among:

- embedding or launching a JavaScript/TypeScript runtime with a compatibility bridge;
- a new process-out protocol;
- a Go-native in-process API plus migration tooling;
- a hybrid.

The decision must track capability loss and migration impact per extension API. The presence of a
new Go plugin interface is not proof that existing Pi extensions remain usable.

### `pi-tui` is a product-sized subsystem

Eino does not provide Pi's differential terminal renderer. TUI parity is an independent workstream,
not a thin adapter around the agent loop.

### `pi-ai` is more than a chat-model interface

The parity inventory must include provider-specific message conversion, generated model metadata,
authentication flows, subscription/plan behaviour where present, streaming, retry, and usage/cost
accounting. Eino `ChatModel` coverage alone is insufficient.

### Evaluation parity is evidence infrastructure

`packages/evals` may not need matching internal APIs, but complete-replication claims require
equivalent behavioural evidence. It remains in the denominator as class `I`, not silently omitted.

### pigo is not a client/server wire precedent

Primary-source review at `smallnest/pigo@ef2c447b754b114b0eea87ff2ad1228bcb11dc84` found no Pi
CBOR codec, four-byte framing, hello/version schema, or standalone protocol/client/server package.
Its headless JSONL, subprocess JSON-RPC, and browser WebSocket control are pigo-owned surfaces and
cannot interoperate directly with Pi's client/server. They are partial analogues, not completed
class-R parity. See `docs/research/pigo-wire-compatibility.md`.

pigo is useful evidence for a different boundary: its embedded Node Pi-extension host loads the
real Pi SDK and adapts selected tools/commands onto pigo's JSON-RPC plugin protocol. That validates
the feasibility of a TS bridge candidate for ADR-0003, while its inert/no-op session, model, UI,
provider, and widget actions demonstrate why capability degradation must remain explicit.

## Inventory completion order

1. Pin the upstream baseline and inventory every documented coding-agent feature/command.
2. Expand `packages/agent` into the existing C1-C8 contract rows.
3. Inventory extension API events, UI methods, tools, state, and host-mode degradation.
4. Inventory session formats and both current session runtimes without conflating them.
5. Inventory `pi-ai` provider/auth/usage surfaces separately from Eino components.
6. Inventory TUI primitives and user-visible interaction behaviours.
7. Inventory RPC/protocol/client/server wire and event contracts.

This order organizes the work; it does not permit implementation to restart area by area. The hold
is lifted only after the whole inventory passes C0–C8 and the repository owner explicitly approves
reopening implementation.
