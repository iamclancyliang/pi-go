package claude

import (
	"context"
	"io"
	"net/http"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// translatedModel turns the adapter's stream into what the shared streaming loop
// reads. The adapter passes this provider's own words and counts straight through,
// and the loop speaks the chat-completions dialect.
//
// Only streaming is covered, because it is the only path the shared port uses:
// its Generate collects the stream.
type translatedModel struct {
	model.ToolCallingChatModel
	gate *bodyGate
}

// WithTools keeps the binding translated. The embedded method would return the
// adapter's own model, and most of the conversations this port serves have tools.
func (m translatedModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	bound, err := m.ToolCallingChatModel.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return translatedModel{bound, m.gate}, nil
}

// Stream answers with every chunk translated.
func (m translatedModel) Stream(ctx context.Context, input []*schema.Message,
	opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {

	reader, err := m.ToolCallingChatModel.Stream(ctx, input, opts...)
	m.gate.open()
	if err != nil {
		return nil, err
	}
	reply := replyState{hasArgs: map[int]bool{}}
	return schema.StreamReaderWithConvert(reader, reply.translate), nil
}

// dialectEnding says one of this provider's stop reasons in the shared loop's
// words. Anything else passes through unchanged, and the loop fails on it by name.
//
// That includes pause_turn, which asks for the turn to be sent back so a server
// tool can carry on. This port binds no server tools, so it should not occur, and
// if it does the reply is not finished: reporting it as an ordinary end would hand
// over a partial answer as the model's last word.
func dialectEnding(stop string) string {
	switch stop {
	case "end_turn", "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	case "refusal", "sensitive":
		return "content_filter"
	default:
		return stop
	}
}

// replyState is what translating one reply has to remember between chunks.
type replyState struct {
	used    *schema.TokenUsage
	calls   []int
	hasArgs map[int]bool
}

// translate rewrites one chunk. The chunk is copied rather than edited, because
// the framework's callbacks may hold the same message.
func (r *replyState) translate(chunk *schema.Message) (*schema.Message, error) {
	out := *chunk
	for _, call := range chunk.ToolCalls {
		if call.Index == nil {
			continue
		}
		if _, seen := r.hasArgs[*call.Index]; !seen {
			r.calls = append(r.calls, *call.Index)
			r.hasArgs[*call.Index] = false
		}
		if call.Function.Arguments != "" {
			r.hasArgs[*call.Index] = true
		}
	}
	if chunk.ResponseMeta == nil {
		return &out, nil
	}
	meta := *chunk.ResponseMeta
	if meta.Usage != nil {
		r.used = mergedUsage(r.used, meta.Usage)
		meta.Usage = r.used
	}
	if meta.FinishReason == "tool_use" {
		out.ToolCalls = append(chunk.ToolCalls[:len(chunk.ToolCalls):len(chunk.ToolCalls)], r.emptyArguments()...)
	}
	meta.FinishReason = dialectEnding(meta.FinishReason)
	out.ResponseMeta = &meta
	return &out, nil
}

// emptyArguments completes the calls that never received any arguments.
//
// The adapter blanks the empty input this provider opens every call with, so a
// call to a tool taking no arguments ends up with an empty string, which no tool
// can parse. It can only be told apart once the reply has ended: until then more
// fragments may come, and a reply cut off by a limit may have been about to send
// them, which is why this waits for a reply that says it finished by calling tools.
func (r *replyState) emptyArguments() []schema.ToolCall {
	var calls []schema.ToolCall
	for _, at := range r.calls {
		if r.hasArgs[at] {
			continue
		}
		index := at
		calls = append(calls, schema.ToolCall{
			Index:    &index,
			Function: schema.FunctionCall{Arguments: "{}"},
		})
	}
	return calls
}

// mergedUsage combines the two reports one reply makes.
//
// The opening event carries the prompt's counts and the closing one the output's,
// usually leaving the prompt's out, which the adapter reads as zero. The shared
// loop keeps the last report it sees, so without this every reply would claim a
// prompt of nothing and the overflow check would never fire. The provider's counts
// are cumulative, so the larger of two values is always the later truth.
func mergedUsage(earlier, later *schema.TokenUsage) *schema.TokenUsage {
	if earlier == nil {
		return later
	}
	merged := *later
	merged.PromptTokens = max(earlier.PromptTokens, later.PromptTokens)
	merged.PromptTokenDetails.CachedTokens = max(
		earlier.PromptTokenDetails.CachedTokens, later.PromptTokenDetails.CachedTokens)
	merged.CompletionTokens = max(earlier.CompletionTokens, later.CompletionTokens)
	merged.CompletionTokensDetails.ReasoningTokens = max(
		earlier.CompletionTokensDetails.ReasoningTokens, later.CompletionTokensDetails.ReasoningTokens)
	merged.TotalTokens = merged.PromptTokens + merged.CompletionTokens
	return &merged
}

// bodyGate holds a reply's body shut until the adapter's Stream has returned.
//
// The goroutine the adapter reads the reply on assigns to Stream's own named error
// result, the first time it joins the empty events that open every reply to the
// first one with content. Stream is writing that same variable as it returns, so
// the two race on every reply, and the newest release of the adapter still does.
// The goroutine reaches that line only by reading the body, so a body that cannot
// be read until Stream has returned puts the two writes in order. Nothing is lost
// by waiting: the wait ends before anyone has been handed the stream.
//
// One gate serves one call, as one adapter does: the shared port builds both anew
// for each request.
type bodyGate struct {
	opened chan struct{}
	once   sync.Once
}

func newBodyGate() *bodyGate { return &bodyGate{opened: make(chan struct{})} }

func (g *bodyGate) open() { g.once.Do(func() { close(g.opened) }) }

// client is the given client with its replies held behind this gate.
func (g *bodyGate) client(base *http.Client) *http.Client {
	gated := *base
	gated.Transport = gatedTransport{inner: base.Transport, gate: g}
	return &gated
}

type gatedTransport struct {
	inner http.RoundTripper
	gate  *bodyGate
}

func (t gatedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	// A refusal is left alone. The SDK reads it inside Stream, before there is any
	// reading goroutine to race with, and holding it would wait on itself.
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		resp.Body = gatedBody{ReadCloser: resp.Body, ctx: req.Context(), gate: t.gate}
	}
	return resp, nil
}

type gatedBody struct {
	io.ReadCloser
	ctx  context.Context
	gate *bodyGate
}

func (b gatedBody) Read(p []byte) (int, error) {
	select {
	case <-b.gate.opened:
		return b.ReadCloser.Read(p)
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	}
}
