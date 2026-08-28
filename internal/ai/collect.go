package ai

import "strings"

// Collect drains a reply's events into the single answer a caller asked for.
//
// The collected path is the streamed one drained, never a second reading of the
// wire: two paths that build a reply independently drift, and only one of them
// ends up covered by the tests that matter. Every provider that offers both
// therefore answers Generate from here.
//
// The provider is named only to say who could not finish, which is the one
// thing this cannot know for itself.
func Collect(provider string, events <-chan StreamEvent) (Response, error) {
	var final *AssistantMessage
	for ev := range events {
		if ev.Final != nil {
			final = ev.Final
		}
	}
	if final == nil {
		// Typed, like every other way a call can fail: a caller branches on the
		// classification, and a stream that ended with no ending is not
		// something it should have to recognise from prose.
		return Response{}, &ProviderError{
			Provider: provider,
			Failure:  FailureUnknown,
			Detail:   "the stream ended without a terminal event",
		}
	}
	if final.Cause != nil {
		// The counts travel with the failure: on this path there is no response
		// to carry them, and a call that read the request is not free. Earlier
		// attempts come first because they happened first; a provider that
		// makes one attempt per call simply has none.
		consumed := append(CloneUsages(final.EarlierAttempts), final.Usage.Clone())
		return Response{}, WithUsage(final.Cause, consumed...)
	}

	var text, reasoning strings.Builder
	var calls []ToolCall
	for _, b := range final.Blocks {
		switch b.Kind {
		case BlockText:
			text.WriteString(b.Text)
		case BlockThinking:
			reasoning.WriteString(b.Text)
		case BlockToolCall:
			calls = append(calls, b.Call)
		}
	}
	return Response{
		EarlierAttempts: CloneUsages(final.EarlierAttempts),
		Content:         text.String(),
		Reasoning:       reasoning.String(),
		ToolCalls:       calls,
		Model:           final.Model,
		Usage:           final.Usage.Clone(),
		Truncated:       final.StopReason == StopLength,
	}, nil
}
