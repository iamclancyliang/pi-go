package deepseek

import (
	"context"
	"errors"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// Generate answers in one piece.
//
// It is the stream collected, not a second implementation. Pi has exactly one
// production path — its non-streaming call is its streaming call awaited — and
// the same holds here. Two request-building paths drift, and only one of them
// ends up covered by the tests that matter.
func (p *Port) Generate(ctx context.Context, req ai.Request) (ai.Response, error) {
	events, err := p.Stream(ctx, req)
	if err != nil {
		return ai.Response{}, err
	}

	var final *ai.AssistantMessage
	for ev := range events {
		if ev.Final != nil {
			final = ev.Final
		}
	}
	if final == nil {
		// The channel closed without a terminal event. Nothing here may report
		// a reply it never received.
		return ai.Response{}, &Error{
			Failure: FailureUnknown,
			Detail:  "the stream ended without a terminal event",
		}
	}
	if final.Cause != nil {
		// The counts travel with the failure: on this path there is no response
		// to carry them, and a failed call that read the request is not free.
		consumed := append([]ai.Usage(nil), final.EarlierAttempts...)
		if final.Usage.Reported {
			consumed = append(consumed, final.Usage)
		}
		var classified *Error
		if errors.As(final.Cause, &classified) && len(consumed) > 0 {
			withUsage := *classified
			withUsage.Usage = consumed
			return ai.Response{}, &withUsage
		}
		return ai.Response{}, final.Cause
	}

	var text, reasoning strings.Builder
	var calls []ai.ToolCall
	for _, b := range final.Blocks {
		switch b.Kind {
		case ai.BlockText:
			text.WriteString(b.Text)
		case ai.BlockThinking:
			// Kept apart from Content — it is what the model worked through,
			// not what it said — but kept, because this provider requires it
			// back on the next request.
			reasoning.WriteString(b.Text)
		case ai.BlockToolCall:
			calls = append(calls, b.Call)
		}
	}
	return ai.Response{
		EarlierAttempts: final.EarlierAttempts,
		Content:         text.String(),
		Reasoning:       reasoning.String(),
		ToolCalls:       calls,
		Model:           final.Model,
		Usage:           final.Usage,
		Truncated:       final.StopReason == ai.StopLength,
	}, nil
}

// Compile-time proof that this satisfies both boundaries.
var (
	_ ai.Port          = (*Port)(nil)
	_ ai.StreamingPort = (*Port)(nil)
)
