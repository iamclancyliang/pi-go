package ai

import (
	"context"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// This file is the ONLY place the framework's model types appear.
//
// Everything else in this package speaks pi-go's own model port; the framework
// and its provider adapters stay hidden behind it, and none of this reaches
// pi-go's public surface.

// NewEinoChatModel adapts a pi-go Port to the chat-model interface eino
// consumes.
//
// The Port stays in charge: this is translation only. If pi-go ever stops
// using this framework, this file is what gets deleted — not the port, and not
// any caller of it.
func NewEinoChatModel(p Port, defaultModel string) model.BaseChatModel {
	return &einoChatModel{port: p, defaultModel: defaultModel}
}

type einoChatModel struct {
	port         Port
	defaultModel string
}

func (m *einoChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	req := Request{
		Messages: fromEinoMessages(input),
		Tools:    toolSpecsFromOptions(opts),
		Model:    m.defaultModel,
	}

	resp, err := m.port.Generate(ctx, req)
	if err != nil {
		return nil, err
	}
	return toEinoMessage(resp), nil
}

// Stream satisfies eino's interface by delivering the non-streaming result as a
// single chunk.
//
// This is deliberately NOT presented as streaming support. pi's real path is
// streaming and it needs its own verification; emitting one chunk would make a
// streaming test pass without proving anything about incremental delivery. v0
// slice is deterministic, so the honest position is that streaming is
// unimplemented, not that it works.
func (m *einoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() {
		defer sw.Close()
		sw.Send(msg, nil)
	}()
	return sr, nil
}

var _ model.BaseChatModel = (*einoChatModel)(nil)

func fromEinoMessages(in []*schema.Message) []Message {
	out := make([]Message, 0, len(in))
	for _, m := range in {
		if m == nil {
			continue
		}
		msg := Message{
			Role:       fromEinoRole(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{
				ID:   tc.ID,
				Name: tc.Function.Name,
				Args: tc.Function.Arguments,
			})
		}
		out = append(out, msg)
	}
	return out
}

func toEinoMessage(r Response) *schema.Message {
	calls := make([]schema.ToolCall, 0, len(r.ToolCalls))
	for _, tc := range r.ToolCalls {
		calls = append(calls, schema.ToolCall{
			ID: tc.ID,
			Function: schema.FunctionCall{
				Name:      tc.Name,
				Arguments: tc.Args,
			},
		})
	}
	if len(calls) == 0 {
		return schema.AssistantMessage(r.Content, nil)
	}
	return schema.AssistantMessage(r.Content, calls)
}

func fromEinoRole(r schema.RoleType) Role {
	switch r {
	case schema.System:
		return RoleSystem
	case schema.User:
		return RoleUser
	case schema.Assistant:
		return RoleAssistant
	case schema.Tool:
		return RoleTool
	default:
		// Unknown roles are reported as-is rather than coerced to a
		// plausible one: silently relabelling a message we do not
		// understand would corrupt session truth.
		return Role(r)
	}
}

// toolSpecsFromOptions reports the tools eino bound to this call.
func toolSpecsFromOptions(opts []model.Option) []ToolSpec {
	common := model.GetCommonOptions(&model.Options{}, opts...)
	if common == nil || len(common.Tools) == 0 {
		return nil
	}
	out := make([]ToolSpec, 0, len(common.Tools))
	for _, t := range common.Tools {
		if t == nil {
			continue
		}
		out = append(out, ToolSpec{Name: t.Name, Description: t.Desc})
	}
	return out
}
