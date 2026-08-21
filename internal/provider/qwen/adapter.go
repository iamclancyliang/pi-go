package qwen

import (
	"fmt"
	"net/http"

	"github.com/cloudwego/eino/schema"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// toMessages converts this repository's messages into the adapter's.
//
// Only what this package handles: text, an assistant's reasoning, tool calls
// and tool results. Anything else would be a block the rest of this repository
// has no contract for, and quietly dropping it would leave a caller believing
// it was sent.
func toMessages(msgs []ai.Message) ([]*schema.Message, error) {
	out := make([]*schema.Message, 0, len(msgs))
	for _, m := range msgs {
		converted, err := oneMessage(m)
		if err != nil {
			return nil, err
		}
		out = append(out, converted)
	}
	return out, nil
}

func oneMessage(m ai.Message) (*schema.Message, error) {
	switch m.Role {
	case ai.RoleSystem:
		return schema.SystemMessage(m.Content), nil

	case ai.RoleUser:
		return schema.UserMessage(m.Content), nil

	case ai.RoleAssistant:
		calls := make([]schema.ToolCall, 0, len(m.ToolCalls))
		for at, call := range m.ToolCalls {
			// The position is sent back as the provider addressed it. This is
			// the field that survives its own streaming, and returning history
			// without it would describe an order the model never produced.
			index := at
			calls = append(calls, schema.ToolCall{
				Index: &index,
				ID:    call.ID,
				Type:  "function",
				Function: schema.FunctionCall{
					Name:      call.Name,
					Arguments: call.Args,
				},
			})
		}
		msg := schema.AssistantMessage(m.Content, calls)
		// Reasoning travels in its own field rather than folded into the
		// answer: the two are different things to a model reading its own
		// history, and merging them makes the reply look like something the
		// model said out loud.
		msg.ReasoningContent = m.Reasoning
		return msg, nil

	case ai.RoleTool:
		// A tool result is addressed to the call it answers. Without that id
		// the provider cannot tell which of several calls was answered.
		return schema.ToolMessage(m.Content, m.ToolCallID), nil

	default:
		return nil, fmt.Errorf("qwen: no conversion for role %q", m.Role)
	}
}

// toolSpecs converts this repository's tool descriptions for the adapter.
func toolSpecs(specs []ai.ToolSpec) []*schema.ToolInfo {
	out := make([]*schema.ToolInfo, 0, len(specs))
	for _, spec := range specs {
		out = append(out, &schema.ToolInfo{Name: spec.Name, Desc: spec.Description})
	}
	return out
}

// httpClient wraps the injected transport for the adapter.
//
// Redirects are refused rather than followed. A redirect is another request:
// the default client would make it, and a call budgeted for one request would
// quietly make several — each billable, none counted, and the second carrying
// the credential to wherever the first was pointed.
func httpClient(tr http.RoundTripper) *http.Client {
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
