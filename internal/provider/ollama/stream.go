package ollama

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// addressedModel supplies what Ollama's reply leaves out of a tool call.
//
// The shared streaming loop refuses a call with no position, and is right to:
// a provider that streams a call in fragments needs one to keep them together,
// and inferring it from arrival order could merge two calls. Ollama is different
// because it sends each call whole, in one chunk, so arrival order cannot
// misattribute anything. That is true of this provider only, which is why the
// numbering is done here and the shared check stays as it is.
//
// Only streaming is covered, because it is the only path the shared port uses:
// its Generate collects the stream.
type addressedModel struct {
	model.ToolCallingChatModel
}

// WithTools keeps the binding addressed. The embedded method would return the
// adapter's own model, and the tool calls this exists for only happen on a model
// with tools bound.
func (m addressedModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	bound, err := m.ToolCallingChatModel.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return addressedModel{bound}, nil
}

// Stream answers with every tool call addressed.
func (m addressedModel) Stream(ctx context.Context, input []*schema.Message,
	opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {

	reader, err := m.ToolCallingChatModel.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	reply := replyAddress{prefix: newReplyPrefix()}
	return schema.StreamReaderWithConvert(reader, reply.address), nil
}

// replyAddress numbers the tool calls of one reply as they arrive.
type replyAddress struct {
	prefix string
	calls  int
}

// address fills in one chunk. The chunk is copied rather than edited, because
// the framework's callbacks may hold the same message.
func (r *replyAddress) address(chunk *schema.Message) (*schema.Message, error) {
	out := *chunk
	if len(chunk.ToolCalls) > 0 {
		out.ToolCalls = make([]schema.ToolCall, len(chunk.ToolCalls))
		for at, call := range chunk.ToolCalls {
			index := r.calls
			r.calls++
			call.Index = &index
			// The wire has no id, and the rest of this repository pairs a result
			// with its call by nothing else.
			call.ID = fmt.Sprintf("%s_%d", r.prefix, index)
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
		// Ollama says "stop" for a reply that called a tool. Its own
		// OpenAI-compatible endpoint reports the same reply as tool_calls, and any
		// other ending, such as length, still means what it says.
		if meta.FinishReason == "stop" && r.calls > 0 {
			meta.FinishReason = "tool_calls"
		}
		// The conversion attaches a usage object to every chunk, and only the
		// last one has counts in it. A stream cut off before then has reported
		// nothing, which is not the same as having consumed nothing. A terminal
		// report of zero is still a report and must survive.
		if used := meta.Usage; meta.FinishReason == "" && used != nil && used.PromptTokens == 0 && used.CompletionTokens == 0 {
			meta.Usage = nil
		}
		out.ResponseMeta = &meta
	}
	return &out, nil
}

// newReplyPrefix makes a reply's call ids distinct from every earlier reply's.
//
// They stay in the conversation, so numbering from zero alone would give the
// first call of every reply the same id, and a result could pair with a call
// from an earlier turn.
func newReplyPrefix() string {
	var raw [8]byte
	// crypto/rand.Read does not return an error: it crashes the program if the
	// system's source of randomness fails.
	_, _ = rand.Read(raw[:])
	return "call_" + hex.EncodeToString(raw[:])
}
