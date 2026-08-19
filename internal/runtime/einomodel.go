package runtime

import (
	"context"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// The adapter lives HERE, not in the model port's package.
//
// The port describes what pi-go needs from a model. A constructor that returns a
// framework type puts that framework in the signature every caller compiles
// against, so the dependency is re-exported rather than hidden — the port then
// carries the framework in its own API while claiming to abstract it. This
// package already depends on the framework; the port does not, and must not.
//
// If pi-go ever stops using this framework, this file is what gets deleted —
// not the port, and not any caller of it.
func newEinoChatModel(p ai.Port, defaultModel string) model.BaseChatModel {
	return &einoChatModel{port: p, defaultModel: defaultModel}
}

type einoChatModel struct {
	port         ai.Port
	defaultModel string
}

func (m *einoChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	req := ai.Request{
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

func fromEinoMessages(in []*schema.Message) []ai.Message {
	out := make([]ai.Message, 0, len(in))
	for _, m := range in {
		if m == nil {
			continue
		}
		msg := ai.Message{
			Role:       fromEinoRole(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, ai.ToolCall{
				ID:   tc.ID,
				Name: tc.Function.Name,
				Args: tc.Function.Arguments,
			})
		}
		out = append(out, msg)
	}
	return out
}

func toEinoMessage(r ai.Response) *schema.Message {
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

func fromEinoRole(r schema.RoleType) ai.Role {
	switch r {
	case schema.System:
		return ai.RoleSystem
	case schema.User:
		return ai.RoleUser
	case schema.Assistant:
		return ai.RoleAssistant
	case schema.Tool:
		return ai.RoleTool
	default:
		// Unknown roles are reported as-is rather than coerced to a
		// plausible one: silently relabelling a message we do not
		// understand would corrupt session truth.
		return ai.Role(r)
	}
}

// toolSpecsFromOptions reports the tools eino bound to this call.
func toolSpecsFromOptions(opts []model.Option) []ai.ToolSpec {
	common := model.GetCommonOptions(&model.Options{}, opts...)
	if common == nil || len(common.Tools) == 0 {
		return nil
	}
	out := make([]ai.ToolSpec, 0, len(common.Tools))
	for _, t := range common.Tools {
		if t == nil {
			continue
		}
		out = append(out, ai.ToolSpec{Name: t.Name, Description: t.Desc})
	}
	return out
}
