package openai

import (
	"fmt"
	"net/http"

	"github.com/cloudwego/eino/schema"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// toAgentic converts this repository's messages into the adapter's.
//
// Only what this package handles: text, an assistant's reasoning, tool calls
// and tool results. Anything else would be a block the rest of this repository
// has no contract for, and quietly dropping it would leave a caller believing
// it was sent.
func toAgentic(msgs []ai.Message) ([]*schema.AgenticMessage, error) {
	out := make([]*schema.AgenticMessage, 0, len(msgs))
	for _, m := range msgs {
		converted, err := oneAgentic(m)
		if err != nil {
			return nil, err
		}
		out = append(out, converted)
	}
	return out, nil
}

func oneAgentic(m ai.Message) (*schema.AgenticMessage, error) {
	switch m.Role {
	case ai.RoleSystem:
		// A system message carries INPUT text, not generated text. The adapter
		// refuses a generated-text block here, and rightly: the two mean
		// different things even though both hold a string.
		return &schema.AgenticMessage{
			Role:          schema.AgenticRoleType(schema.System),
			ContentBlocks: []*schema.ContentBlock{inputTextBlock(m.Content)},
		}, nil

	case ai.RoleUser:
		return &schema.AgenticMessage{
			Role:          schema.AgenticRoleType(schema.User),
			ContentBlocks: []*schema.ContentBlock{inputTextBlock(m.Content)},
		}, nil

	case ai.RoleAssistant:
		msg := &schema.AgenticMessage{Role: schema.AgenticRoleType(schema.Assistant)}
		// Reasoning comes first, as it did when the model produced it: this
		// provider requires an assistant's reasoning back with the next
		// request, and its position is part of what it means.
		if m.Reasoning != "" {
			msg.ContentBlocks = append(msg.ContentBlocks, &schema.ContentBlock{
				Type:      schema.ContentBlockTypeReasoning,
				Reasoning: &schema.Reasoning{Text: m.Reasoning},
			})
		}
		if m.Content != "" {
			msg.ContentBlocks = append(msg.ContentBlocks, textBlock(m.Content))
		}
		for _, call := range m.ToolCalls {
			msg.ContentBlocks = append(msg.ContentBlocks, &schema.ContentBlock{
				Type: schema.ContentBlockTypeFunctionToolCall,
				FunctionToolCall: &schema.FunctionToolCall{
					CallID:    call.ID,
					Name:      call.Name,
					Arguments: call.Args,
				},
			})
		}
		return msg, nil

	case ai.RoleTool:
		return &schema.AgenticMessage{
			Role: schema.AgenticRoleType(schema.Tool),
			ContentBlocks: []*schema.ContentBlock{{
				Type: schema.ContentBlockTypeFunctionToolResult,
				FunctionToolResult: &schema.FunctionToolResult{
					CallID: m.ToolCallID,
					Content: []*schema.FunctionToolResultContentBlock{{
						Type: schema.FunctionToolResultContentBlockTypeText,
						Text: &schema.UserInputText{Text: m.Content},
					}},
				},
			}},
		}, nil

	default:
		return nil, fmt.Errorf("openai: no conversion for role %q", m.Role)
	}
}

// inputTextBlock is text going TO the model.
func inputTextBlock(text string) *schema.ContentBlock {
	return &schema.ContentBlock{
		Type:          schema.ContentBlockTypeUserInputText,
		UserInputText: &schema.UserInputText{Text: text},
	}
}

// textBlock is text the model generated.
func textBlock(text string) *schema.ContentBlock {
	return &schema.ContentBlock{
		Type:             schema.ContentBlockTypeAssistantGenText,
		AssistantGenText: &schema.AssistantGenText{Text: text},
	}
}

// httpClient builds the client this call will use.
//
// One client per logical call, holding this call's capture. The record is
// reachable from nowhere else, so nothing inside the SDK can attach one
// attempt's terminal to another request.
func (p *Port) httpClient(held *capture, key string) *http.Client {
	return &http.Client{Transport: &captureTransport{
		inner: p.cfg.Transport, capture: held, key: key,
	}}
}
