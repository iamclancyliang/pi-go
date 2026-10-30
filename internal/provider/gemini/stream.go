package gemini

import (
	"context"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// translatedModel turns the adapter's stream into what the shared streaming loop
// reads. The adapter passes this provider's own words straight through and gives a
// tool call no position, and the loop speaks the chat-completions dialect and
// refuses a call it cannot place.
//
// The position is safe to supply here because this provider sends each call whole,
// in one part, so arrival order cannot misattribute a fragment. That is true of
// this provider only, which is why the shared check stays as it is. The call's id
// is the adapter's: it generates one, because the provider sends none.
//
// Only streaming is covered, because it is the only path the shared port uses:
// its Generate collects the stream.
type translatedModel struct {
	model.ToolCallingChatModel
}

// WithTools keeps the binding translated. The embedded method would return the
// adapter's own model, and the tool calls this exists for only happen on a model
// with tools bound.
func (m translatedModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	bound, err := m.ToolCallingChatModel.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return translatedModel{bound}, nil
}

// Stream answers with every chunk translated.
func (m translatedModel) Stream(ctx context.Context, input []*schema.Message,
	opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {

	reader, err := m.ToolCallingChatModel.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	reply := replyState{}
	return schema.StreamReaderWithConvert(reader, reply.translate), nil
}

// dialectEnding says one of this provider's finish reasons in the shared loop's
// words. Anything else passes through unchanged, and the loop fails on it by name:
// a malformed call, an unsupported language or a value nobody here has seen is a
// failure, but not a filter's decision, and none of them says the reply is whole.
func dialectEnding(finish string, calledTool bool) string {
	switch finish {
	case "STOP":
		// This provider has no ending of its own for a reply that called a tool.
		if calledTool {
			return "tool_calls"
		}
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII",
		"IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION":
		return "content_filter"
	default:
		return finish
	}
}

// replyState numbers the tool calls of one reply as they arrive.
type replyState struct {
	calls int
}

// translate rewrites one chunk. The chunk is copied rather than edited, because
// the framework's callbacks may hold the same message.
func (r *replyState) translate(chunk *schema.Message) (*schema.Message, error) {
	out := *chunk
	if len(chunk.ToolCalls) > 0 {
		out.ToolCalls = make([]schema.ToolCall, len(chunk.ToolCalls))
		for at, call := range chunk.ToolCalls {
			index := r.calls
			r.calls++
			call.Index = &index
			if call.Function.Arguments == "null" {
				// A call with no arguments omits the field, and the conversion
				// marshals that absence as null. A tool's arguments are an object.
				call.Function.Arguments = "{}"
			}
			out.ToolCalls[at] = call
		}
	}
	if chunk.ResponseMeta != nil {
		meta := *chunk.ResponseMeta
		meta.FinishReason = dialectEnding(meta.FinishReason, r.calls > 0)
		out.ResponseMeta = &meta
	}
	return &out, nil
}
