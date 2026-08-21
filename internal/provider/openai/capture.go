// Package openai reaches OpenAI's Responses API.
//
// The wire, the SSE decoding and the tool-schema conversion come from an
// eino-ext adapter. Everything this repository promises about a provider does
// not: credentials, the typed failure set, the usage ledger, block identity,
// served-model truth, retry ownership and overflow recovery stay here, because
// the adapter does not carry them.
//
// One thing in particular has to be taken before the adapter runs. Its
// conversion turns usage into plain integers, so a count the provider never
// reported becomes a reported zero, and the model that actually served the
// reply is dropped entirely. Both are read here from the provider's own bytes,
// upstream of any conversion.
package openai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
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

	// InputTokens and the rest are set only when the response carried them.
	InputTokens     *int
	OutputTokens    *int
	CachedTokens    *int
	ReasoningTokens *int

	// Status and IncompleteReason describe how the provider ended the reply.
	Status           string
	IncompleteReason string
}

// capture holds what one logical call observed.
//
// Owned by the call that created it and reachable from nowhere else: there is
// no shared table keyed by anything, so no failure, cancellation or retry
// inside the SDK can attach one attempt's terminal to another request.
type capture struct {
	mu       sync.Mutex
	requests int
	attempts []terminal
}

func (c *capture) startAttempt() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests++
	c.attempts = append(c.attempts, terminal{})
}

// observe records what an attempt reported. The attempt is identified by
// position in this call's own list, not by matching content.
func (c *capture) observe(t terminal) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.attempts) == 0 {
		return
	}
	c.attempts[len(c.attempts)-1] = t
}

func (c *capture) requestCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests
}

// last is the terminal of the most recent attempt.
func (c *capture) last() terminal {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.attempts) == 0 {
		return terminal{}
	}
	return c.attempts[len(c.attempts)-1]
}

// earlier is every attempt before the most recent one.
func (c *capture) earlier() []terminal {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.attempts) <= 1 {
		return nil
	}
	out := make([]terminal, len(c.attempts)-1)
	copy(out, c.attempts[:len(c.attempts)-1])
	return out
}

// captureTransport counts requests and reads the provider's terminal metadata.
//
// It does both because both need the same vantage point, but they are proven
// separately: a count that is right says nothing about whether the metadata was
// read, and vice versa.
type captureTransport struct {
	inner   http.RoundTripper
	capture *capture
}

func (t *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.capture.startAttempt()
	resp, err := t.inner.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}

	// A streamed body is observed as it flows. Reading it here would consume
	// the stream the caller is about to read, and buffering it whole would turn
	// a stream into a wait.
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		resp.Body = &teeBody{inner: resp.Body, onTerminal: t.capture.observe}
		return resp, nil
	}

	// A non-streamed body is small enough to read and hand back intact.
	raw, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	if readErr == nil {
		if found, ok := terminalFromResponse(raw); ok {
			t.capture.observe(found)
		}
	}
	return resp, nil
}

// teeBody watches an event stream go past without changing or delaying it.
type teeBody struct {
	inner      io.ReadCloser
	onTerminal func(terminal)
	pending    []byte
	done       bool
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.inner.Read(p)
	if n > 0 && !t.done {
		t.pending = append(t.pending, p[:n]...)
		t.scan()
	}
	return n, err
}

func (t *teeBody) Close() error { return t.inner.Close() }

// scan looks for a completed response in whatever has arrived so far.
func (t *teeBody) scan() {
	// Only whole lines can be parsed; a partial one is kept for the next read.
	for {
		idx := bytes.IndexByte(t.pending, '\n')
		if idx < 0 {
			return
		}
		line := t.pending[:idx]
		t.pending = t.pending[idx+1:]

		payload, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
		if !ok {
			continue
		}
		if found, ok := terminalFromEvent(bytes.TrimSpace(payload)); ok {
			t.done = true
			t.onTerminal(found)
			return
		}
	}
}

// wireResponse is only the parts this capture is responsible for. Everything
// else about the reply comes from the adapter.
type wireResponse struct {
	Model            string `json:"model"`
	Status           string `json:"status"`
	IncompleteDetail *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage *struct {
		InputTokens        *int `json:"input_tokens"`
		OutputTokens       *int `json:"output_tokens"`
		InputTokensDetails *struct {
			CachedTokens *int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		OutputTokensDetails *struct {
			ReasoningTokens *int `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

func (w wireResponse) toTerminal() terminal {
	found := terminal{Model: w.Model, Status: w.Status}
	if w.IncompleteDetail != nil {
		found.IncompleteReason = w.IncompleteDetail.Reason
	}
	if w.Usage != nil {
		found.InputTokens = w.Usage.InputTokens
		found.OutputTokens = w.Usage.OutputTokens
		if w.Usage.InputTokensDetails != nil {
			found.CachedTokens = w.Usage.InputTokensDetails.CachedTokens
		}
		if w.Usage.OutputTokensDetails != nil {
			found.ReasoningTokens = w.Usage.OutputTokensDetails.ReasoningTokens
		}
	}
	return found
}

// terminalFromEvent reads a streamed event, keeping only the one that carries
// the finished response.
func terminalFromEvent(payload []byte) (terminal, bool) {
	var event struct {
		Type     string        `json:"type"`
		Response *wireResponse `json:"response"`
	}
	if err := json.Unmarshal(payload, &event); err != nil || event.Response == nil {
		return terminal{}, false
	}
	switch event.Type {
	case "response.completed", "response.incomplete", "response.failed":
		return event.Response.toTerminal(), true
	}
	return terminal{}, false
}

// terminalFromResponse reads a non-streamed reply.
func terminalFromResponse(raw []byte) (terminal, bool) {
	var body wireResponse
	if err := json.Unmarshal(raw, &body); err != nil {
		return terminal{}, false
	}
	if body.Model == "" && body.Usage == nil {
		return terminal{}, false
	}
	return body.toTerminal(), true
}
