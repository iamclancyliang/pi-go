// Package qwen reaches Qwen's OpenAI-compatible endpoint.
//
// The wire, the SSE decoding and the tool-schema conversion come from an
// eino-ext adapter. Everything this repository promises about a provider does
// not: credentials, the typed failure set, the usage ledger, block identity,
// served-model truth, retry ownership and overflow recovery stay here.
//
// Two things in particular have to be taken before the adapter's conversion
// runs. It reports the model it was configured with rather than the one that
// answered, and it flattens a cached-token count the provider may never have
// sent into a zero. Both are read here from the provider's own bytes.
package qwen

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// terminal is what the provider itself said, before anything reinterpreted it.
//
// Every field is optional on purpose: absent means the provider did not say,
// and that is not the same as zero. Filling a gap from the request — the model
// we ASKED for, or a zero standing in for silence — would manufacture evidence
// for the two claims this capture exists to support.
type terminal struct {
	// Model is the model the provider reports as having served the reply.
	Model string

	// InputTokens and the rest are set only when the reply carried them.
	InputTokens     *int
	OutputTokens    *int
	CachedTokens    *int
	ReasoningTokens *int

	// FinishReason is how the provider says the reply ended.
	FinishReason string

	// ErrorCode is the provider's own code when a reply failed inside a 200. A
	// stream can fail for a reason the status cannot express — an exhausted
	// balance among them — and calling every such ending the same thing loses
	// the difference between "try later" and "this cannot succeed".
	ErrorCode string
}

// capture holds what one logical call observed.
//
// Owned by the call that created it and reachable from nowhere else: there is
// no shared table keyed by anything, so no failure or cancellation inside the
// adapter can attach one call's terminal to another request.
type capture struct {
	mu   sync.Mutex
	seen terminal

	// failure is the classified reason a request was refused, when one was.
	failure error

	// announced is every tool-call position the provider sent, in order, and
	// whether that fragment named the call it belongs to.
	announced []announcement

	// anonymous describes every fragment the provider sent without the index
	// that identifies it. Held separately because there is no index to hold it
	// under, which is exactly the condition being recorded.
	anonymous []string
}

// announcement is one tool-call fragment as the provider addressed it.
type announcement struct {
	index int
	// named is true when the fragment carried the call's id or name, which the
	// provider sends once, on the fragment that opens the call.
	named bool
	id    string
}

// observe records what the reply reported, keeping the last value of each
// field the provider actually sent. A later chunk that omits a field has not
// retracted it: usage and the served model arrive once, on different chunks.
func (c *capture) observe(t terminal) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.Model != "" {
		c.seen.Model = t.Model
	}
	if t.FinishReason != "" {
		c.seen.FinishReason = t.FinishReason
	}
	if t.ErrorCode != "" {
		c.seen.ErrorCode = t.ErrorCode
	}
	if t.InputTokens != nil {
		c.seen.InputTokens = t.InputTokens
	}
	if t.OutputTokens != nil {
		c.seen.OutputTokens = t.OutputTokens
	}
	if t.CachedTokens != nil {
		c.seen.CachedTokens = t.CachedTokens
	}
	if t.ReasoningTokens != nil {
		c.seen.ReasoningTokens = t.ReasoningTokens
	}
}

func (c *capture) last() terminal {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen
}

// observeFailure records why a request was refused.
func (c *capture) observeFailure(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failure = err
}

// refusal is the classified failure, if the provider refused.
func (c *capture) refusal() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failure
}

// observeCall records a tool-call fragment as the provider addressed it.
func (c *capture) observeCall(a announcement) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.announced = append(c.announced, a)
}

// observeAnonymous records a fragment sent with no identity.
func (c *capture) observeAnonymous(what string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.anonymous = append(c.anonymous, what)
}

func (c *capture) announcements() []announcement {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]announcement(nil), c.announced...)
}

func (c *capture) anonymousFragments() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.anonymous...)
}

// captureTransport reads the provider's own metadata as it goes past.
//
// It does not count requests: the injected transport already answers that from
// the outside, and a second counter nobody reads is a claim with no reader.
type captureTransport struct {
	inner   http.RoundTripper
	capture *capture
	key     string
}

func (t *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// A non-streamed refusal is small enough to read and hand back intact.
		raw, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		if readErr != nil {
			return resp, nil
		}
		// Classified here, where the status and the provider's own error code
		// both exist. After this the adapter turns it into prose, and a
		// classification rebuilt from prose is one a change of wording breaks.
		refused := failureFrom(resp.StatusCode, raw, t.key)
		var classified *ai.ProviderError
		if errors.As(refused, &classified) {
			// The provider's own instruction about retrying travels with the
			// failure. Nothing here retries, so an instruction that is not
			// carried out is one the caller who does decide never learns of.
			classified.Advise(retryAdvice(resp.Header))
		}
		// A refused request may still report what it read. Recording it keeps
		// a failed call from being accounted for as free.
		if used, ok := usageFromBody(raw); ok {
			if classified != nil {
				classified.Record(used)
			} else {
				refused = ai.WithUsage(refused, used)
			}
		}
		t.capture.observeFailure(refused)
		return resp, nil
	}

	// A streamed body is observed as it flows. Reading it here would consume
	// the stream the caller is about to read, and buffering it whole would turn
	// a stream into a wait.
	resp.Body = &teeBody{
		inner:       resp.Body,
		onTerminal:  t.capture.observe,
		onCall:      t.capture.observeCall,
		onAnonymous: t.capture.observeAnonymous,
	}
	return resp, nil
}

// teeBody watches an event stream go past without changing or delaying it.
type teeBody struct {
	inner       io.ReadCloser
	onTerminal  func(terminal)
	onCall      func(announcement)
	onAnonymous func(string)
	pending     []byte
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.inner.Read(p)
	if n > 0 {
		t.pending = append(t.pending, p[:n]...)
		t.scan()
	}
	if err != nil {
		// The stream ended. A final event needs no blank line after it to be
		// valid, so whatever is still buffered is parsed now — otherwise the
		// metadata of a reply that ended at EOF is lost.
		t.flush()
	}
	return n, err
}

func (t *teeBody) Close() error { return t.inner.Close() }

func (t *teeBody) scan() {
	for {
		idx := bytes.IndexByte(t.pending, '\n')
		if idx < 0 {
			return
		}
		line := t.pending[:idx]
		t.pending = t.pending[idx+1:]
		t.handleLine(line)
	}
}

// flush parses whatever remains once no more bytes are coming.
func (t *teeBody) flush() {
	trailing := t.pending
	t.pending = nil
	for _, line := range bytes.Split(trailing, []byte("\n")) {
		t.handleLine(line)
	}
}

func (t *teeBody) handleLine(line []byte) {
	trimmed := bytes.TrimSpace(line)
	chunk, ok := bytes.CutPrefix(trimmed, []byte("data:"))
	if !ok {
		return
	}
	payload := bytes.TrimSpace(chunk)
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	t.handle(payload)
}

// handle reads one streamed chunk.
func (t *teeBody) handle(payload []byte) {
	var body wireChunk
	if err := json.Unmarshal(payload, &body); err != nil {
		return
	}
	if t.onTerminal != nil {
		t.onTerminal(body.toTerminal())
	}
	for _, choice := range body.Choices {
		if choice.Delta == nil {
			continue
		}
		for _, call := range choice.Delta.ToolCalls {
			switch {
			case call.Index == nil:
				// The position is what ties a fragment to the call it
				// continues. Without it there is nothing to tie, and inferring
				// one from arrival order is the renumbering this refuses to do.
				if t.onAnonymous != nil {
					t.onAnonymous("a tool call fragment with no index")
				}
			case t.onCall != nil:
				t.onCall(announcement{
					index: *call.Index,
					named: call.ID != "" || (call.Function != nil && call.Function.Name != ""),
					id:    call.ID,
				})
			}
		}
	}
}

// wireChunk is only the parts this capture is responsible for. Everything else
// about the reply comes from the adapter.
type wireChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Delta        *struct {
			ToolCalls []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Function *struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *wireUsage `json:"usage"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// wireUsage is the provider's own count, with presence preserved.
type wireUsage struct {
	PromptTokens     *int `json:"prompt_tokens"`
	CompletionTokens *int `json:"completion_tokens"`
	PromptDetails    *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionDetails *struct {
		ReasoningTokens *int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (w wireChunk) toTerminal() terminal {
	found := terminal{Model: w.Model}
	for _, choice := range w.Choices {
		if choice.FinishReason != "" {
			found.FinishReason = choice.FinishReason
		}
	}
	if w.Error != nil {
		found.ErrorCode = w.Error.Code
	}
	if w.Usage != nil {
		found.InputTokens = w.Usage.PromptTokens
		found.OutputTokens = w.Usage.CompletionTokens
		if w.Usage.PromptDetails != nil {
			found.CachedTokens = w.Usage.PromptDetails.CachedTokens
		}
		if w.Usage.CompletionDetails != nil {
			found.ReasoningTokens = w.Usage.CompletionDetails.ReasoningTokens
		}
	}
	return found
}

// usageFromBody reads usage a refused request reported, when it reported any.
func usageFromBody(raw []byte) (ai.Usage, bool) {
	var body wireChunk
	if err := json.Unmarshal(raw, &body); err != nil || body.Usage == nil {
		return ai.Usage{}, false
	}
	return usageFrom(body.toTerminal()), true
}
