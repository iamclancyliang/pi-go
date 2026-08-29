package ai_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// streamOf delivers a recorded set of events and closes, as a port's stream does.
func streamOf(events ...ai.StreamEvent) <-chan ai.StreamEvent {
	out := make(chan ai.StreamEvent, len(events))
	for _, ev := range events {
		out <- ev
	}
	close(out)
	return out
}

func terminal(msg ai.AssistantMessage) ai.StreamEvent {
	kind := ai.StreamDone
	if msg.Cause != nil {
		kind = ai.StreamError
	}
	return ai.StreamEvent{Kind: kind, Final: &msg}
}

// TestCollectingAReplyKeepsEveryPartOfIt: a caller that asked in one piece gets
// what a caller watching it arrive would have seen, in the order the model
// produced it.
func TestCollectingAReplyKeepsEveryPartOfIt(t *testing.T) {
	cached, reasoningTokens := 4, 0
	got, err := ai.Collect("p", streamOf(terminal(ai.AssistantMessage{
		Model:      "served-model",
		StopReason: ai.StopToolUse,
		Blocks: []ai.Block{
			{Kind: ai.BlockThinking, Text: "weighing "},
			{Kind: ai.BlockThinking, Text: "it up"},
			{Kind: ai.BlockText, Text: "the "},
			{Kind: ai.BlockText, Text: "answer"},
			{Kind: ai.BlockToolCall, Call: ai.ToolCall{ID: "call_a", Name: "alpha", Args: "{}"}},
			{Kind: ai.BlockToolCall, Call: ai.ToolCall{ID: "call_b", Name: "beta", Args: "{}"}},
		},
		Usage: ai.Usage{
			InputTokens: 7, OutputTokens: 3,
			CacheReadTokens: &cached, ReasoningTokens: &reasoningTokens, Reported: true,
		},
		EarlierAttempts: []ai.Usage{{InputTokens: 11, Reported: true}},
	})))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got.Content != "the answer" {
		t.Fatalf("content %q", got.Content)
	}
	if got.Reasoning != "weighing it up" {
		t.Fatalf("reasoning %q", got.Reasoning)
	}
	if len(got.ToolCalls) != 2 ||
		got.ToolCalls[0] != (ai.ToolCall{ID: "call_a", Name: "alpha", Args: "{}"}) ||
		got.ToolCalls[1] != (ai.ToolCall{ID: "call_b", Name: "beta", Args: "{}"}) {
		t.Fatalf("calls %+v", got.ToolCalls)
	}
	if got.Model != "served-model" {
		t.Fatalf("served model %q", got.Model)
	}
	if got.Truncated {
		t.Fatal("a reply that asked for tools was reported as cut short")
	}
	// Everything a bill is built from, field by field: a total would agree
	// while input and output were swapped.
	if got.Usage.InputTokens != 7 || got.Usage.OutputTokens != 3 {
		t.Fatalf("usage %+v", got.Usage)
	}
	if got.Usage.CacheReadTokens == nil || *got.Usage.CacheReadTokens != 4 {
		t.Fatalf("cache read %v", got.Usage.CacheReadTokens)
	}
	if got.Usage.ReasoningTokens == nil || *got.Usage.ReasoningTokens != 0 {
		t.Fatalf("a reported zero became %v", got.Usage.ReasoningTokens)
	}
	// The attempts before this one are part of what the call cost. A provider
	// that makes one attempt has none; one that retried has spent on each.
	if len(got.EarlierAttempts) != 1 || got.EarlierAttempts[0].InputTokens != 11 {
		t.Fatalf("earlier attempts %+v; a call that paid twice reported once", got.EarlierAttempts)
	}
}

// TestATruncatedReplySaysSo: the ending is what a caller acts on, and a reply
// the provider cut short must not be handed back as a complete answer.
func TestATruncatedReplySaysSo(t *testing.T) {
	got, err := ai.Collect("p", streamOf(terminal(ai.AssistantMessage{
		StopReason: ai.StopLength,
		Blocks:     []ai.Block{{Kind: ai.BlockText, Text: "half an"}},
	})))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !got.Truncated {
		t.Fatal("a reply the provider cut short was collected as complete")
	}
}

// TestAFailedReplyCarriesWhatEveryAttemptSpent: there is no response on this
// path to hold the counts, and a call that read the request is not free.
func TestAFailedReplyCarriesWhatEveryAttemptSpent(t *testing.T) {
	cause := &ai.ProviderError{Provider: "p", Failure: ai.FailureThrottled, Detail: "slow"}
	_, err := ai.Collect("p", streamOf(terminal(ai.AssistantMessage{
		StopReason:      ai.StopError,
		Cause:           cause,
		Usage:           ai.Usage{InputTokens: 5, Reported: true},
		EarlierAttempts: []ai.Usage{{InputTokens: 11, Reported: true}, {InputTokens: 13, Reported: true}},
	})))
	if !errors.Is(err, cause) {
		t.Fatalf("the cause was lost: %v", err)
	}
	var carrier interface{ Consumed() []ai.Usage }
	if !errors.As(err, &carrier) {
		t.Fatalf("a failed call reported no usage at all: %v", err)
	}
	spent := carrier.Consumed()
	if len(spent) != 3 {
		t.Fatalf("three attempts ledgered %d entries: %+v", len(spent), spent)
	}
	// In the order they happened: the attempts that came first come first.
	for at, want := range []int{11, 13, 5} {
		if spent[at].InputTokens != want {
			t.Fatalf("entry %d is %d, want %d: %+v", at, spent[at].InputTokens, want, spent)
		}
	}
}

// TestAStreamThatNeverEndedIsATypedFailure: a caller branches on a
// classification, and a stream that stopped without an ending is not something
// it should have to recognise from prose.
func TestAStreamThatNeverEndedIsATypedFailure(t *testing.T) {
	_, err := ai.Collect("some-provider", streamOf(
		ai.StreamEvent{Kind: ai.StreamStart},
		ai.StreamEvent{Kind: ai.StreamTextDelta, Delta: "half"},
	))
	if err == nil {
		t.Fatal("a stream with no ending was collected as an answer")
	}
	failure, ok := ai.FailureOf(err)
	if !ok {
		t.Fatalf("a caller cannot branch on %v", err)
	}
	if failure != ai.FailureUnknown {
		t.Fatalf("classified %s", failure)
	}
	if !strings.Contains(err.Error(), "some-provider") {
		t.Fatalf("the failure does not say who could not finish: %v", err)
	}
}

// TestACollectedReplyOwnsItsCounts: the reply is handed to a caller that may
// keep it. Sharing the ledger with the stream's own message would let either
// one edit what the other reports.
func TestACollectedReplyOwnsItsCounts(t *testing.T) {
	cached := 4
	final := ai.AssistantMessage{
		StopReason:      ai.StopEnd,
		Usage:           ai.Usage{InputTokens: 7, CacheReadTokens: &cached, Reported: true},
		EarlierAttempts: []ai.Usage{{InputTokens: 11, Reported: true}},
	}
	got, err := ai.Collect("p", streamOf(terminal(final)))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	got.Usage.InputTokens = 99999
	*got.Usage.CacheReadTokens = 99999
	got.EarlierAttempts[0].InputTokens = 99999
	if final.Usage.InputTokens != 7 || cached != 4 || final.EarlierAttempts[0].InputTokens != 11 {
		t.Fatalf("editing the collected reply changed what the stream reported: %+v, cached %d",
			final, cached)
	}
}

// TestCollectingStopsWhenTheStreamDoes guards against a collector that waits
// for an ending a closed stream will never send.
func TestCollectingStopsWhenTheStreamDoes(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = ai.Collect("p", streamOf())
	}()
	select {
	case <-done:
	case <-context.Background().Done():
	}
}

// TestAFailedAttemptThatReportedNothingIsStillAnAttempt pins the entry a
// silent final attempt leaves behind.
//
// The runtime ledgers one entry per attempt and deliberately does not filter on
// the reported flag, because counts filled in without it are still counts.
// Dropping a silent attempt here would hand that ledger a shorter list than the
// call has attempts — and when it is the only entry, an empty one, which sends
// the runtime to a fallback that never saw the earlier spend.
func TestAFailedAttemptThatReportedNothingIsStillAnAttempt(t *testing.T) {
	cause := &ai.ProviderError{Provider: "p", Failure: ai.FailureTransient, Detail: "cut"}
	_, err := ai.Collect("p", streamOf(terminal(ai.AssistantMessage{
		StopReason:      ai.StopError,
		Cause:           cause,
		Usage:           ai.Usage{},
		EarlierAttempts: []ai.Usage{{InputTokens: 11, Reported: true}},
	})))
	var carrier interface{ Consumed() []ai.Usage }
	if !errors.As(err, &carrier) {
		t.Fatalf("a failed call reported no usage at all: %v", err)
	}
	spent := carrier.Consumed()
	if len(spent) != 2 {
		t.Fatalf("two attempts ledgered %d entries: %+v", len(spent), spent)
	}
	if spent[0].InputTokens != 11 || !spent[0].Reported {
		t.Fatalf("the attempt that did report was lost: %+v", spent)
	}
	// Silence is carried as silence. Recording it as a measured zero would bill
	// the caller for a number this provider never produced.
	if spent[1].Reported {
		t.Fatalf("an attempt that said nothing was ledgered as having reported: %+v", spent)
	}
}

// TestAnUnknownThinkingLevelIsRefusedNamingTheOnes. A caller who asked for more
// reasoning and silently got the default would read the answer as what the
// model produces when it thinks hard, which is the one conclusion they must not
// draw.
func TestAnUnknownThinkingLevelIsRefusedNamingTheOnes(t *testing.T) {
	_, err := ai.ParseThinkingLevel("very hard")
	if err == nil {
		t.Fatal("an unknown thinking level was accepted")
	}
	for _, level := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
		if !strings.Contains(err.Error(), level) {
			t.Fatalf("the failure does not name %q: %v", level, err)
		}
	}
	for _, accepted := range []string{"off", "HIGH", " max "} {
		if _, err := ai.ParseThinkingLevel(accepted); err != nil {
			t.Fatalf("%q was refused: %v", accepted, err)
		}
	}
}
