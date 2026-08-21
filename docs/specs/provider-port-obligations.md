# What any provider adapter must do

**Status:** derived from the controls in force on `main`. Every line names the test that fails if it
stops being true, so this cannot drift from the code without a red suite.

**Why this exists:** a framework adapter can look like a drop-in replacement and quietly not carry
some of these. Each one below was a real defect at some point in building the first provider, so the
list is a record of what actually goes wrong rather than a wish list.

**How to use it:** for a candidate adapter, mark each obligation *carried*, *partly carried*, or
*not carried*. Anything not carried has to stay in this repository's own layer, or be added on top
of the adapter. That answer, not the adapter's feature list, decides whether the provider port can
be replaced.

## Credentials

| Obligation | Enforced by |
| --- | --- |
| The key is reached only through injected configuration; a missing seam fails rather than falling back | `TestAPortWithoutASuppliedTransportIsRefused` |
| Resolution order: stored wins, else the first set variable; blank counts as unset | `TestCredentialPrecedence`, `TestAStoredCredentialWinsAndABlankVariableIsSkipped` |
| A provider with no other source is handed a resolved value rather than a resolver, so nothing varies per request | `TestAMissingCredentialIsATypedAbsence` |
| One logical call resolves at most once and uses that value for every attempt and for scrubbing | `TestOneCallResolvesItsCredentialOnce`, `TestARetriedCallKeepsTheCredentialItStartedWith` |
| Every error a call reports is scrubbed of that call's own key by identity, not left to its shape | `TestATransportErrorDoesNotCarryTheConfiguredKey`, `TestABodyReadFailureDoesNotCarryTheCallsKey` |
| A lookup that fails with the value in its own error does not carry it out | `TestALookupErrorDoesNotCarryTheValueOut` |
| Scrubbing removes both the exact value and anything key-shaped | `TestScrubbingRemovesBothWhatIsKnownAndWhatIsShaped` |
| Resolution stops when the caller does, between lookups and not only before them | `TestResolutionStopsWhenTheCallerDoes` |
| Absence is a typed failure, not an empty value, and is refused before anything is billed | `TestCredentialPrecedence`, `TestAnAbsentCredentialIsTyped`, `TestAMissingCredentialIsATypedAbsence` |
| The key appears in no formatted value — config, port, or the container holding it — and in no error | `TestCredentialPrecedence`, `TestACredentialDoesNotPrintItsKey`, `TestAConfigDoesNotPrintACallersSecret`, `TestListIsNonSecretAndSideEffectFree` |
| One credential per provider; storing again replaces | `TestOneCredentialPerProvider` |
| Removal is serialized against writing, so a logout racing a refresh is not undone by it | `TestTheStoreSerializesWritesAgainstDeletes` |
| Enumeration returns metadata only and performs no work | `TestListIsNonSecretAndSideEffectFree` |

## Failure classification

| Obligation | Enforced by |
| --- | --- |
| Failures are values, not text: nothing branches on an error's wording | `TestQuotaAndThrottleReachOppositeOutcomes` |
| The set is closed, including the two failures that arrive inside a 200 | `TestA200ThatReportsFailureIsNotASuccess` |
| An unrecognised stop reason is a failure, never a success | `TestA200ThatReportsFailureIsNotASuccess` |
| A failure from the wire leaves classified, and only what a repeat could survive is called transient | `TestATransportFailureLeavesTyped` |
| An overflow reported inside a 200 is the same recoverable condition as one reported by a status | `TestAnOverflowInsideA200IsRecoverable` |
| The provider's own retry instruction outranks the status, and reaches the caller that decides | `TestTheProvidersOwnRetryInstructionSurvives`, `TestAProvidersOwnRetryInstructionReachesOneCaller`, `TestTheProvidersInstructionOutranksTheStatusButNotAnExhaustedBalance` |
| An exhausted balance stays terminal however the provider or the status reads | `TestTheProvidersInstructionOutranksTheStatusButNotAnExhaustedBalance` |
| A reply that asked for tools says so rather than reporting a plain ending | `TestAReplyAskingForToolsSaysSo` |
| Cancellation and deadlines stay themselves rather than becoming transient provider failures | `TestCancellationStaysCancellation`, `TestCancellingABackoffStaysCancellation`, `TestSetupCancellationStaysCancellation`, `TestACancellationInsideATransportErrorStaysCancellation`, `TestALookupCancellationStaysCancellation` |

## Retry and cost

| Obligation | Enforced by |
| --- | --- |
| An exhausted balance is terminal **before** the retry question is asked | `TestAnExhaustedBalanceIsTerminalBeforeAnyRetry` |
| That holds even when exhaustion arrives inside a rate-limit status | `TestExhaustionInsideARateLimitIsStillTerminal` |
| The provider's own retry instruction outranks the status, but never that judgement | `TestTheProvidersOwnInstructionOutranksTheStatus` |
| A requested wait beyond the cap is refused rather than slept | `TestAServerRequestedWaitBeyondTheCapIsRefused` |
| The shipped budget sends one request per model call, proven by counting | `TestOneCallSendsOneRequest`, `TestTheShippedBudgetSendsOneRequest` |
| A request always carries an output cap; one cannot be built without | `TestAnUncappedRequestCannotBeBuilt` |
| The cap uses the field the provider actually reads | `TestTheRequestCarriesTheFieldsThisProviderReads` |
| A request naming no model sends nothing | `TestNoModelIsRefusedBeforeAnythingIsSent` |

## Usage

| Obligation | Enforced by |
| --- | --- |
| Every attempt is ledgered, including a call that never succeeded | `TestAttemptsSurviveACallThatNeverSucceeds`, `TestRetriedAttemptsEachReachTheLedger` |
| A failed call ledgers what it read, on both the streamed and collected paths | `TestAFailedCallStillLedgersWhatItUsed`, `TestAFailedCollectedCallLedgersToo` |
| An overflow that gets recovered still reports what the refused attempt used | `TestCollectedOverflowStillReportsWhatItUsed` |
| Unreported stays distinct from reported zero, in a call and in the total | `TestUsageKeepsUnreportedApartFromZero`, `TestAnUnreportedFieldStaysUnreportedInTheTotal` |
| Cached prompt tokens are counted once, and counted in the total | `TestCachedPromptTokensAreNotCountedTwice`, `TestTotalCountsEveryReportedToken` |
| The ledger owns its entries: neither the writer nor a reader can edit them afterwards | `TestTheLedgerOwnsWhatItRecords`, `TestASnapshotDoesNotChangeAfterItIsTaken` |
| A failed call's spend is owned the same way — copied in and copied out — before it ever reaches a ledger | `TestAFailuresSpendCannotBeRewritten` |
| A failure owns what the provider said about retrying, and a copy of one shares nothing with it | `TestAFailureOwnsWhatTheProviderSaidAboutRetrying`, `TestACopyAndItsOriginalDoNotRecordOverEachOther` |
| The model that served a reply is read from the reply | `TestTheServedModelIsReported` |

## Streaming

| Obligation | Enforced by |
| --- | --- |
| Fragments of one tool call reassemble into one call with its identity intact | `TestAToolCallSplitAcrossChunksStaysOneCall` |
| Interleaved calls stay apart | `TestInterleavedToolCallFragmentsStayApart`, `TestInterleavedToolCallsStayApart` |
| A tool-call position that is missing, opened twice, skipped, or continued before it was opened is refused | `TestAToolCallFragmentWithNoPositionIsRefused`, `TestAStreamWhoseCallPositionsDoNotHoldIsRefused` |
| Text after several open calls closes all of them | `TestTextAfterInterleavedCallsClosesEveryBlock` |
| A block ends before the next begins | `TestABlockEndsBeforeTheNextBegins` |
| A block the provider announced without a position fails before its content reaches a consumer, and still fails when it carries none | `TestAnAnnouncementWithNoIdentityFailsBeforeItsContent`, `TestAnAnnouncementWithNoIdentityAndNoContentStillFails`, `TestAWellFormedStreamStillPasses` |
| A cancelled stream still delivers exactly one terminal, carrying what had arrived | `TestACancelledStreamStillEnds` |
| Streaming and collecting agree on content, reasoning, calls, served model and usage presence | `TestStreamingAndCollectingAgree` |
| A call stopped mid-reply ends aborted rather than failed, whether the context or the error chain says so | `TestAStreamStoppedMidReplyEndsAborted` |
| Overflow is inferred from counts, only against a measured window, and never invented without one | `TestCountBasedOverflowDetection`, `TestConfigurationRefusesAWindowItWouldMisuse` |

## Conversation

| Obligation | Enforced by |
| --- | --- |
| A reply becomes history, and reasoning does not leak into the answer | `TestAProviderReplyReachesTheRuntime` |
| Reasoning returns on the next request, on both paths | `TestReasoningReturnsToTheProviderOnTheNextRound`, `TestReasoningReturnsOnTheCollectedPathToo`, `TestQwenReasoningReturnsToTheProviderOnTheNextRound` |
| Reasoning survives persisting and reopening | `TestReasoningSurvivesReopeningTheSession` |
| A tool call from a provider is refused by policy and recorded before it runs | `TestAProviderToolCallIsRefusedByPolicyAndRecordedFirst`, `TestAnOpenAIToolCallIsRefusedByPolicyAndRecordedFirst`, `TestAQwenToolCallIsRefusedByPolicyAndRecordedFirst` |
| The tools a caller registered reach the request, checked against the bytes sent | `TestQwenToolsReachTheProvider` |
| A tool result travels in a shape the provider accepts, proven by sending it | `TestAQwenToolResultTravelsInAShapeTheProviderAccepts` |
| Several calls keep the order the model asked for | `TestSeveralProviderToolCallsKeepTheOrderTheModelAsked` |
| A failure inside a 200 stops the run instead of arriving as an answer | `TestAProviderFailureStopsTheRun` |
