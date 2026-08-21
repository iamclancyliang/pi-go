package openai

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/iamclancyliang/pi-go/internal/ai"
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

// TestUsageKeepsWhatTheProviderSaid: the adapter's own conversion flattens
// these into integers, so absent becomes zero and the uncached remainder is
// lost. This is what the capture exists to preserve.
func TestUsageKeepsWhatTheProviderSaid(t *testing.T) {
	n := func(v int) *int { return &v }

	t.Run("cached tokens are separated from the uncached remainder", func(t *testing.T) {
		used := usageFrom(terminal{InputTokens: n(1000), OutputTokens: n(5), CachedTokens: n(600)})
		if used.InputTokens != 400 {
			t.Fatalf("uncached input %d, want 400", used.InputTokens)
		}
		if used.CacheReadTokens == nil || *used.CacheReadTokens != 600 {
			t.Fatalf("cache read %v", used.CacheReadTokens)
		}
		if used.Total() != 1005 {
			t.Fatalf("total %d, want 1005", used.Total())
		}
	})

	t.Run("a field the provider never sent stays absent", func(t *testing.T) {
		used := usageFrom(terminal{InputTokens: n(10), OutputTokens: n(1)})
		if used.ReasoningTokens != nil {
			t.Fatalf("silence became %d", *used.ReasoningTokens)
		}
		if !used.Reported {
			t.Fatal("a reply that reported counts claims it reported nothing")
		}
	})

	t.Run("no usage at all is not a free call", func(t *testing.T) {
		if used := usageFrom(terminal{}); used.Reported {
			t.Fatal("a reply with no usage claims to have reported some")
		}
	})
}

// TestAnUnrecognisedStatusIsAFailure: a terminal state this code does not know
// cannot be assumed complete, or a truncated reply is handed back as the
// model's final answer.
func TestAnUnrecognisedStatusIsAFailure(t *testing.T) {
	for _, tc := range []struct {
		status, incomplete string
		wantErr            bool
	}{
		{status: "completed"},
		{status: "incomplete", incomplete: "max_output_tokens"},
		{status: "incomplete", incomplete: "content_filter", wantErr: true},
		{status: "failed", wantErr: true},
		{status: "a_status_from_the_future", wantErr: true},
		{status: "", wantErr: true},
	} {
		name := tc.status + "/" + tc.incomplete
		t.Run(name, func(t *testing.T) {
			_, err := failureFromStatus(tc.status, tc.incomplete, "")
			if (err != nil) != tc.wantErr {
				t.Fatalf("status %q incomplete %q produced err=%v, wantErr=%v",
					tc.status, tc.incomplete, err, tc.wantErr)
			}
		})
	}
}

// TestHistoryConvertsWithReasoningKept: this provider requires an assistant's
// reasoning back on the next request, and its position before the answer is
// part of what it means.
func TestHistoryConvertsWithReasoningKept(t *testing.T) {
	converted, err := toAgentic([]ai.Message{
		{Role: ai.RoleSystem, Content: "be brief"},
		{Role: ai.RoleUser, Content: "hello"},
		{Role: ai.RoleAssistant, Reasoning: "weighing", Content: "hi",
			ToolCalls: []ai.ToolCall{{ID: "tc1", Name: "list_files", Args: "{}"}}},
		{Role: ai.RoleTool, ToolCallID: "tc1", Content: "a.go"},
	})
	if err != nil {
		t.Fatalf("converting: %v", err)
	}
	if len(converted) != 4 {
		t.Fatalf("converted %d messages", len(converted))
	}

	assistant := converted[2]
	if len(assistant.ContentBlocks) != 3 {
		t.Fatalf("assistant carries %d blocks, want reasoning, text and the call",
			len(assistant.ContentBlocks))
	}
	if assistant.ContentBlocks[0].Type != schema.ContentBlockTypeReasoning {
		t.Fatalf("reasoning is not first: %s", assistant.ContentBlocks[0].Type)
	}
	if got := assistant.ContentBlocks[0].Reasoning.Text; got != "weighing" {
		t.Fatalf("reasoning converted as %q", got)
	}
	call := assistant.ContentBlocks[2].FunctionToolCall
	if call == nil || call.CallID != "tc1" || call.Name != "list_files" {
		t.Fatalf("tool call converted as %+v", call)
	}
	result := converted[3].ContentBlocks[0].FunctionToolResult
	if result == nil || result.CallID != "tc1" {
		t.Fatalf("tool result lost its pairing: %+v", result)
	}
}

// TestAnUnsupportedRoleIsRefused: silently dropping a message would leave a
// caller believing it was sent.
func TestAnUnsupportedRoleIsRefused(t *testing.T) {
	if _, err := toAgentic([]ai.Message{{Role: ai.Role("something_new")}}); err == nil {
		t.Fatal("an unconvertible message was accepted")
	}
}
