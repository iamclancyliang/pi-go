// Package chatcompletions watches an OpenAI-compatible chat-completions stream
// go past.
//
// Extracted rather than copied a third time. Of the 375 lines this began as,
// two mentioned the provider they were written for: everything else — the
// Terminal frame, the usage counts with their presence preserved, the
// tool-call positions a renumbering would hide, the tee that observes a body
// without delaying it — is true of any provider speaking this dialect.
//
// What is NOT shared is how a refusal is classified. Statuses and error codes
// mean different things per provider — OpenRouter's 403 is a moderation
// refusal where another's is an authentication failure — so that stays behind
// the Classifier seam, which is the only thing a caller must supply.
//
// This is the chat-completions dialect specifically. The Responses API is a
// different event shape, and the port speaking it keeps its own Capture.
package chatcompletions

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// Terminal is what the provider itself said, before anything reinterpreted it.
//
// Every field is optional on purpose: absent means the provider did not say,
// and that is not the same as zero. Filling a gap from the request — the model
// we ASKED for, or a zero standing in for silence — would manufacture evidence
// for the two claims this Capture exists to support.
type Terminal struct {
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

// Capture holds what one logical call observed.
//
// Owned by the call that created it and reachable from nowhere else: there is
// no shared table keyed by anything, so no failure or cancellation inside the
// adapter can attach one call's Terminal to another request.
type Capture struct {
	mu   sync.Mutex
	seen Terminal

	// failure is the classified reason a request was refused, when one was.
	failure error

	// announced is every tool-call position the provider sent, in order, and
	// whether that fragment named the call it belongs to.
	announced []Announcement

	// anonymous describes every fragment the provider sent without the index
	// that identifies it. Held separately because there is no index to hold it
	// under, which is exactly the condition being recorded.
	anonymous []string
}

// Announcement is one tool-call fragment as the provider addressed it.
type Announcement struct {
	Index int

	// Named is true when the fragment carried the call's id or name, which the
	// provider sends once, on the fragment that opens the call.
	Named bool

	ID string
}

// observe records what the reply reported, keeping the last value of each
// field the provider actually sent. A later chunk that omits a field has not
// retracted it: usage and the served model arrive once, on different chunks.
func (c *Capture) observe(t Terminal) {
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

// Last is the most recent terminal frame this exchange carried.
func (c *Capture) Last() Terminal {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen
}

// observeFailure records why a request was refused.
func (c *Capture) observeFailure(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failure = err
}

// refusal is the classified failure, if the provider refused.
// Refusal is the classified reason a request was refused, when one was.
func (c *Capture) Refusal() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failure
}

// observeCall records a tool-call fragment as the provider addressed it.
func (c *Capture) observeCall(a Announcement) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.announced = append(c.announced, a)
}

// observeAnonymous records a fragment sent with no identity.
func (c *Capture) observeAnonymous(what string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.anonymous = append(c.anonymous, what)
}

// Announcements are the tool-call positions the provider sent, in order.
func (c *Capture) Announcements() []Announcement {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Announcement(nil), c.announced...)
}

// AnonymousFragments are the fragments sent with no position to identify them.
func (c *Capture) AnonymousFragments() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.anonymous...)
}

// Transport reads the provider's own metadata as it goes past.
//
// It does not count requests: the injected transport already answers that from
// the outside, and a second counter nobody reads is a claim with no reader.
// Transport observes a chat-completions exchange without changing it.
type Transport struct {
	inner      http.RoundTripper
	classifier Classifier
	key        string

	// Capture holds what this exchange revealed, and is read after it.
	Capture *Capture
}

// Classifier is the only thing a provider must supply.
//
// Statuses and error codes mean different things per provider — OpenRouter's
// 403 is a moderation refusal where another's is an authentication failure — so
// the mapping cannot be shared even though everything around it can.
type Classifier interface {
	// Refusal classifies a non-2xx response, with the credential to scrub from
	// anything it quotes.
	Refusal(status int, body []byte, key string) error

	// RetryAdvice reads the provider's own instruction about trying again, or
	// nil when it gave none.
	RetryAdvice(h http.Header) *bool
}

// NewTransport wraps a transport so one exchange can be observed.
//
// One per call, holding that call's record: a transport shared across calls
// would attribute one call's terminal frame to another.
func NewTransport(inner http.RoundTripper, classifier Classifier, key string) *Transport {
	return &Transport{inner: inner, classifier: classifier, key: key, Capture: &Capture{}}
}

// UsageOf turns a captured terminal into this repository's usage.
//
// Here rather than in each port: what presence means, and that a cached prompt
// is subtracted rather than counted beside the whole one, is the shared rule.
func UsageOf(t Terminal) ai.Usage {
	return ai.ReportedCounts{
		InputTokens:     t.InputTokens,
		OutputTokens:    t.OutputTokens,
		CachedTokens:    t.CachedTokens,
		ReasoningTokens: t.ReasoningTokens,
	}.Usage()
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
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
		refused := t.classifier.Refusal(resp.StatusCode, raw, t.key)
		var classified *ai.ProviderError
		if errors.As(refused, &classified) {
			// The provider's own instruction about retrying travels with the
			// failure. Nothing here retries, so an instruction that is not
			// carried out is one the caller who does decide never learns of.
			classified.Advise(t.classifier.RetryAdvice(resp.Header))
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
		t.Capture.observeFailure(refused)
		return resp, nil
	}

	// A streamed body is observed as it flows. Reading it here would consume
	// the stream the caller is about to read, and buffering it whole would turn
	// a stream into a wait.
	resp.Body = &teeBody{
		inner:       resp.Body,
		onTerminal:  t.Capture.observe,
		onCall:      t.Capture.observeCall,
		onAnonymous: t.Capture.observeAnonymous,
	}
	return resp, nil
}

// teeBody watches an event stream go past without changing or delaying it.
type teeBody struct {
	inner       io.ReadCloser
	onTerminal  func(Terminal)
	onCall      func(Announcement)
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
	var body chunk
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
				t.onCall(Announcement{
					Index: *call.Index,
					Named: call.ID != "" || (call.Function != nil && call.Function.Name != ""),
					ID:    call.ID,
				})
			}
		}
	}
}

// chunk is only the parts this Capture is responsible for. Everything else
// about the reply comes from the adapter.
type chunk struct {
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
	Usage *usage `json:"usage"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// usage is the provider's own count, with presence preserved.
type usage struct {
	PromptTokens     *int `json:"prompt_tokens"`
	CompletionTokens *int `json:"completion_tokens"`
	PromptDetails    *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionDetails *struct {
		ReasoningTokens *int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (w chunk) toTerminal() Terminal {
	found := Terminal{Model: w.Model}
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
	var body chunk
	if err := json.Unmarshal(raw, &body); err != nil || body.Usage == nil {
		return ai.Usage{}, false
	}
	return UsageOf(body.toTerminal()), true
}
