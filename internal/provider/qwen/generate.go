package qwen

import (
	"context"
	"errors"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// Generate answers in one piece.
//
// The stream collected, not a second implementation: two request-building paths
// drift, and only one of them ends up covered by the tests that matter.
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
		return ai.Response{}, fail(FailureUnknown, 0, "the stream ended without a terminal event")
	}
	if final.Cause != nil {
		// The counts travel with the failure: on this path there is no response
		// to carry them, and a call that read the request is not free.
		return ai.Response{}, ai.WithUsage(final.Cause, final.Usage.Clone())
	}

	var text, reasoning strings.Builder
	var calls []ai.ToolCall
	for _, b := range final.Blocks {
		switch b.Kind {
		case ai.BlockText:
			text.WriteString(b.Text)
		case ai.BlockThinking:
			reasoning.WriteString(b.Text)
		case ai.BlockToolCall:
			calls = append(calls, b.Call)
		}
	}
	return ai.Response{
		Content:   text.String(),
		Reasoning: reasoning.String(),
		ToolCalls: calls,
		Model:     final.Model,
		Usage:     final.Usage,
		Truncated: final.StopReason == ai.StopLength,
	}, nil
}

// Compile-time proof that this satisfies both boundaries.
var (
	_ ai.Port          = (*Port)(nil)
	_ ai.StreamingPort = (*Port)(nil)
	_ error            = (*Error)(nil)
	_                  = errors.Is
)
