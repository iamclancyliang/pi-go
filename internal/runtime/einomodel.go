package runtime

import (
	"context"
	"errors"

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
// The model name is asked for per call, not captured once.
//
// A run may change model between turns, and a name copied at construction keeps
// naming the model the run started with: the change would apply to nothing while
// still being announced, which is worse than not supporting it.
func newEinoChatModel(p ai.Port, defaultModel func() string) model.BaseChatModel {
	return &einoChatModel{port: p, defaultModel: defaultModel}
}

type einoChatModel struct {
	port         ai.Port
	defaultModel func() string
}

func (m *einoChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	req := ai.Request{
		Messages: fromEinoMessages(input),
		Tools:    toolSpecsFromOptions(opts),
		Model:    m.defaultModel(),
	}

	resp, err := m.port.Generate(ctx, req)
	if err != nil {
		// Tagged on the way out, because this is where a pi-go error crosses
		// into the framework and gets wrapped in the framework's own.
		return nil, ownError{err}
	}
	return toEinoMessage(resp), nil
}

// Stream delivers the reply to the framework as it arrives.
//
// THE FRAMEWORK GETS A LOSSY VIEW, and that is a decision rather than an
// oversight. Its message carries text and reasoning as two flat strings and tool
// calls as a third field, so block boundaries and their order do not survive the
// crossing: two adjacent text blocks arrive as one. The framework does not need
// them — it drives the loop — while pi-go's own event surface keeps the block
// structure for anything that renders the reply.
//
// A port that cannot stream falls back to one chunk. That is not presented as
// streaming: it is the whole answer, delivered once, which is what a
// non-streaming provider has to give.
func (m *einoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	streaming, ok := m.port.(ai.StreamingPort)
	if !ok {
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

	events, err := streaming.Stream(ctx, ai.Request{
		Messages: fromEinoMessages(input),
		Tools:    toolSpecsFromOptions(opts),
		Model:    m.defaultModel(),
	})
	if err != nil {
		return nil, err
	}

	sr, sw := schema.Pipe[*schema.Message](streamBuffer)
	go func() {
		defer sw.Close()
		for event := range events {
			chunk, send := einoChunk(event)
			if !send {
				continue
			}
			if closed := sw.Send(chunk, einoTerminalError(event)); closed {
				return
			}
		}
	}()
	return sr, nil
}

// streamBuffer is how many chunks may sit between the provider and the framework.
//
// Small on purpose: a large buffer would let the provider run far ahead of the
// consumer, which turns delivery-as-it-arrives back into delivery-in-a-burst
// without anything reporting that it had.
const streamBuffer = 1

// einoChunk converts one event, and reports whether the framework should see it.
//
// Only the increments cross. The start, the block boundaries and the terminal
// carry no new content, and forwarding them would make the framework's own
// concatenation count the same text twice.
func einoChunk(e ai.StreamEvent) (*schema.Message, bool) {
	switch e.Kind {
	case ai.StreamTextDelta:
		return &schema.Message{Role: schema.Assistant, Content: e.Delta}, true
	case ai.StreamThinkingDelta:
		return &schema.Message{Role: schema.Assistant, ReasoningContent: e.Delta}, true
	case ai.StreamToolCallEnd:
		return &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:       e.Call.ID,
				Function: schema.FunctionCall{Name: e.Call.Name, Arguments: e.Call.Args},
			}},
		}, true
	case ai.StreamError:
		return &schema.Message{Role: schema.Assistant}, true
	default:
		return nil, false
	}
}

// einoTerminalError is the error the framework is given, if any.
//
// A failed reply must fail the stream. Sending its text and stopping quietly
// would present a cut-off answer as a complete one.
func einoTerminalError(e ai.StreamEvent) error {
	if e.Kind != ai.StreamError || e.Final == nil {
		return nil
	}
	if e.Final.StopReason == ai.StopAborted {
		return context.Canceled
	}
	return errors.New("runtime: " + e.Final.ErrorMessage)
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
