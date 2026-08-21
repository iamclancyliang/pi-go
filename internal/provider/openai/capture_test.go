package openai

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type stubRoundTripper struct {
	responses []*http.Response
	sent      int
	err       error
}

func (s *stubRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	s.sent++
	if s.err != nil {
		return nil, s.err
	}
	if s.sent > len(s.responses) {
		return nil, io.ErrUnexpectedEOF
	}
	return s.responses[s.sent-1], nil
}

func sseResponse(events ...string) *http.Response {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: " + e + "\n\n")
	}
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(b.String())),
	}
}

func drain(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return string(body)
}

// TestTheCapturedTerminalComesFromTheProvider covers the metadata half of the
// transport's job. The count is proven separately, because a right count says
// nothing about whether anything was read.
func TestTheCapturedTerminalComesFromTheProvider(t *testing.T) {
	held := &capture{}
	tr := &captureTransport{
		capture: held,
		inner: &stubRoundTripper{responses: []*http.Response{sseResponse(
			`{"type":"response.output_text.delta","delta":"hi"}`,
			`{"type":"response.completed","response":{"model":"gpt-served-something","status":"completed","usage":{"input_tokens":11,"output_tokens":2,"input_tokens_details":{"cached_tokens":4}}}}`,
		)}},
	}

	resp, err := tr.RoundTrip(&http.Request{})
	if err != nil {
		t.Fatal(err)
	}
	body := drain(t, resp)

	// The stream is handed on untouched.
	if !strings.Contains(body, `"delta":"hi"`) || !strings.Contains(body, "response.completed") {
		t.Fatalf("the body was altered or truncated:\n%s", body)
	}

	got := held.last()
	if got.Model != "gpt-served-something" {
		t.Fatalf("served model captured as %q", got.Model)
	}
	if got.InputTokens == nil || *got.InputTokens != 11 {
		t.Fatalf("input tokens %v", got.InputTokens)
	}
	if got.CachedTokens == nil || *got.CachedTokens != 4 {
		t.Fatalf("cached tokens %v", got.CachedTokens)
	}
	// Never reported, so it must stay absent rather than become zero.
	if got.ReasoningTokens != nil {
		t.Fatalf("a count the provider never sent became %d", *got.ReasoningTokens)
	}
}

// TestAbsentIsNotZero is the property the adapter's own conversion destroys.
func TestAbsentIsNotZero(t *testing.T) {
	for _, tc := range []struct {
		name   string
		usage  string
		absent bool
		want   int
	}{
		{name: "reported zero", usage: `,"usage":{"input_tokens":5,"output_tokens":0,"output_tokens_details":{"reasoning_tokens":0}}`, want: 0},
		{name: "never reported", usage: `,"usage":{"input_tokens":5,"output_tokens":0}`, absent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held := &capture{}
			tr := &captureTransport{
				capture: held,
				inner: &stubRoundTripper{responses: []*http.Response{sseResponse(
					`{"type":"response.completed","response":{"model":"m","status":"completed"` + tc.usage + `}}`)}},
			}
			resp, err := tr.RoundTrip(&http.Request{})
			if err != nil {
				t.Fatal(err)
			}
			drain(t, resp)

			got := held.last().ReasoningTokens
			if tc.absent {
				if got != nil {
					t.Fatalf("silence became %d", *got)
				}
				return
			}
			if got == nil || *got != tc.want {
				t.Fatalf("a reported %d became %v", tc.want, got)
			}
		})
	}
}

// TestNothingIsBackfilledWhenTheProviderSaysNothing: an unreadable or silent
// terminal leaves the fields unknown. Filling them from the request would
// manufacture the very evidence this capture exists to provide.
func TestNothingIsBackfilledWhenTheProviderSaysNothing(t *testing.T) {
	held := &capture{}
	tr := &captureTransport{
		capture: held,
		inner: &stubRoundTripper{responses: []*http.Response{sseResponse(
			`{"type":"response.output_text.delta","delta":"only content, no terminal"}`)}},
	}
	resp, err := tr.RoundTrip(&http.Request{})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, resp)

	got := held.last()
	if got.Model != "" || got.InputTokens != nil || got.CachedTokens != nil {
		t.Fatalf("a stream with no terminal produced %+v", got)
	}
}

// TestEachAttemptKeepsItsOwnTerminal: one attempt's terminal must never be
// attached to another request. The pairing is positional within a call this
// wrapper owns, so there is no shared table to cross.
func TestEachAttemptKeepsItsOwnTerminal(t *testing.T) {
	held := &capture{}
	tr := &captureTransport{
		capture: held,
		inner: &stubRoundTripper{responses: []*http.Response{
			sseResponse(`{"type":"response.failed","response":{"model":"first-model","status":"failed","usage":{"input_tokens":70}}}`),
			sseResponse(`{"type":"response.completed","response":{"model":"second-model","status":"completed","usage":{"input_tokens":30}}}`),
		}},
	}

	for i := 0; i < 2; i++ {
		resp, err := tr.RoundTrip(&http.Request{})
		if err != nil {
			t.Fatal(err)
		}
		drain(t, resp)
	}

	if n := held.requestCount(); n != 2 {
		t.Fatalf("counted %d requests", n)
	}
	earlier := held.earlier()
	if len(earlier) != 1 || earlier[0].Model != "first-model" {
		t.Fatalf("the first attempt is recorded as %+v", earlier)
	}
	if last := held.last(); last.Model != "second-model" {
		t.Fatalf("the last attempt is recorded as %+v", last)
	}
}

// TestTheCountIsTheCountOfRequests covers the counting half on its own,
// including a request that produced no response at all.
func TestTheCountIsTheCountOfRequests(t *testing.T) {
	held := &capture{}
	tr := &captureTransport{capture: held, inner: &stubRoundTripper{err: io.ErrUnexpectedEOF}}

	if _, err := tr.RoundTrip(&http.Request{}); err == nil {
		t.Fatal("expected the transport failure")
	}
	if n := held.requestCount(); n != 1 {
		t.Fatalf("a request that failed before answering counted %d", n)
	}
	if got := held.last(); got.Model != "" {
		t.Fatalf("a failed request invented a terminal: %+v", got)
	}
}
