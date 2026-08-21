package qwen_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/qwen"
)

// recordedTransport replays a recorded exchange and counts requests.
type recordedTransport struct {
	requests  int
	responses []*http.Response
	sent      []string
}

func (r *recordedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		r.sent = append(r.sent, string(body))
	}
	r.requests++
	if r.requests > len(r.responses) {
		return nil, io.ErrUnexpectedEOF
	}
	return r.responses[r.requests-1], nil
}

var fixedKey = ai.StoredCredential("test-key", "a test")

// streamed is a recorded reply in the provider's own event format.
func streamed(chunks ...string) *http.Response {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString("data: " + c + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(b.String())),
	}
}

func refused(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newPort(t *testing.T, tr http.RoundTripper) *qwen.Port {
	t.Helper()
	p, err := qwen.New(qwen.Config{
		Model: "qwen-test", Transport: tr, Credential: fixedKey, MaxOutputTokens: 64,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func ask(t *testing.T, p *qwen.Port) (ai.Response, error) {
	t.Helper()
	return p.Generate(context.Background(), ai.Request{
		Model:    "qwen-test",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hello"}},
	})
}

// TestAReplyArrivesThroughTheAdapter drives the real adapter over a recorded
// exchange: no network, and the request count proves the transport was used.
func TestAReplyArrivesThroughTheAdapter(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{streamed(
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"role":"assistant","content":"the "}}]}`,
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"content":"answer"}}]}`,
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":4}}}`,
	)}}
	resp, err := ask(t, newPort(t, tr))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if tr.requests != 1 {
		t.Fatalf("sent %d requests, want exactly 1", tr.requests)
	}
	if resp.Content != "the answer" {
		t.Fatalf("content %q", resp.Content)
	}
	// From the capture, not the adapter's conversion, which drops it entirely.
	if resp.Model != "qwen-served" {
		t.Fatalf("served model %q; a substitution would be invisible", resp.Model)
	}
	if resp.Usage.InputTokens != 7 {
		t.Fatalf("uncached input %d, want 7 (11 reported, 4 cached)", resp.Usage.InputTokens)
	}
	if resp.Usage.CacheReadTokens == nil || *resp.Usage.CacheReadTokens != 4 {
		t.Fatalf("cache read %v", resp.Usage.CacheReadTokens)
	}
	if resp.Usage.ReasoningTokens != nil {
		t.Fatalf("a count the provider never sent became %d", *resp.Usage.ReasoningTokens)
	}
	if resp.Usage.Total() != 13 {
		t.Fatalf("total %d, want 13", resp.Usage.Total())
	}
	// The cap has to reach the request; requiring it at construction says
	// nothing about what was sent.
	if !strings.Contains(tr.sent[0], `"max_tokens":64`) {
		t.Fatalf("the output cap did not reach the request: %s", tr.sent[0])
	}
}

// TestInterleavedToolCallsStayApart is the case that disqualified this
// provider's other adapter: two calls whose fragments alternate must arrive as
// two calls with whole arguments.
func TestInterleavedToolCallsStayApart(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{streamed(
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"alpha","arguments":"{\"x\""}}]}}]}`,
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"beta","arguments":"{\"y\""}}]}}]}`,
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":1}"}}]}}]}`,
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":":2}"}}]}}]}`,
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":4}}`,
	)}}
	resp, err := ask(t, newPort(t, tr))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("two announced calls became %d: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	for at, want := range []ai.ToolCall{
		{ID: "call_a", Name: "alpha", Args: `{"x":1}`},
		{ID: "call_b", Name: "beta", Args: `{"y":2}`},
	} {
		got := resp.ToolCalls[at]
		if got.ID != want.ID || got.Name != want.Name || got.Args != want.Args {
			t.Errorf("call %d is %+v, want %+v", at, got, want)
		}
	}
}

// TestAToolCallFragmentWithNoPositionIsRefused: the position is what ties a
// fragment to the call it continues. Inferring one from arrival order is the
// renumbering this repository refuses to do everywhere else.
func TestAToolCallFragmentWithNoPositionIsRefused(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{streamed(
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"alpha","arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)}}
	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("a fragment with no position was accepted")
	}
	if !strings.Contains(err.Error(), "no index") {
		t.Fatalf("the failure did not say what was missing: %v", err)
	}
}

// TestAStreamWhoseCallPositionsDoNotHoldIsRefused covers the other two ways a
// position can be wrong: opening one twice describes two calls in one place,
// and continuing one that was never opened has nothing to continue.
func TestAStreamWhoseCallPositionsDoNotHoldIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		chunks  []string
		wantErr string
	}{
		{
			name: "a position opened twice",
			chunks: []string{
				`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"alpha","arguments":"{}"}}]}}]}`,
				`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_b","function":{"name":"beta","arguments":"{}"}}]}}]}`,
				`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			},
			wantErr: "twice",
		},
		{
			name: "a position continued but never opened",
			chunks: []string{
				`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":3,"function":{"arguments":"{}"}}]}}]}`,
				`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			},
			wantErr: "never opened",
		},
		{
			name: "a position that skips",
			chunks: []string{
				`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"alpha","arguments":"{}"}}]}}]}`,
				`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":2,"id":"call_c","function":{"name":"gamma","arguments":"{}"}}]}}]}`,
				`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			},
			wantErr: "skips",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &recordedTransport{responses: []*http.Response{streamed(tc.chunks...)}}
			_, err := ask(t, newPort(t, tr))
			if err == nil {
				t.Fatal("a stream whose positions do not hold was accepted")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("the failure did not explain itself: %v", err)
			}
		})
	}
}

// TestAWellFormedStreamStillPasses guards the checks above from being satisfied
// by refusing everything.
func TestAWellFormedStreamStillPasses(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{streamed(
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"alpha","arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","function":{"name":"beta","arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)}}
	resp, err := ask(t, newPort(t, tr))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Identities too, not just the count: a check that only counts would be
	// satisfied by two calls nobody can dispatch, which is not what "still
	// passes" should mean.
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("got %d calls", len(resp.ToolCalls))
	}
	for at, want := range []ai.ToolCall{
		{ID: "call_a", Name: "alpha", Args: "{}"},
		{ID: "call_b", Name: "beta", Args: "{}"},
	} {
		if resp.ToolCalls[at] != want {
			t.Errorf("call %d is %+v, want %+v", at, resp.ToolCalls[at], want)
		}
	}
}

// TestReasoningDoesNotLeakIntoTheAnswer: the two are different things to a
// caller rendering a reply, and merging them shows a model's private working as
// something it said.
func TestReasoningDoesNotLeakIntoTheAnswer(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{streamed(
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"weighing it up"}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"the answer"}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)}}
	resp, err := ask(t, newPort(t, tr))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Content != "the answer" {
		t.Fatalf("content %q", resp.Content)
	}
	if resp.Reasoning != "weighing it up" {
		t.Fatalf("reasoning %q", resp.Reasoning)
	}
}

// TestUsageNeverSentStaysAbsent: a count the provider did not report is not a
// zero, and recording it as one bills a caller for a number nobody produced.
func TestUsageNeverSentStaysAbsent(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{streamed(
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)}}
	resp, err := ask(t, newPort(t, tr))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Usage.Reported {
		t.Fatalf("usage nobody sent was reported: %+v", resp.Usage)
	}
	if resp.Usage.CacheReadTokens != nil {
		t.Fatalf("a cached count nobody sent became %d", *resp.Usage.CacheReadTokens)
	}
}

// TestUsageReportedAsZeroStaysReported is the other half: a real zero is a
// measurement, and dropping it loses the difference between "nothing was used"
// and "nobody said".
func TestUsageReportedAsZeroStaysReported(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{streamed(
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0}}`,
	)}}
	resp, err := ask(t, newPort(t, tr))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !resp.Usage.Reported {
		t.Fatal("a reported zero was recorded as nothing said")
	}
}

// TestTheServedModelComesFromTheReply: a provider may answer from a different
// model than the one asked for, and the request is not evidence of which.
func TestTheServedModelComesFromTheReply(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{streamed(
		`{"id":"c1","model":"qwen-substituted","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}`,
		`{"id":"c1","model":"qwen-substituted","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)}}
	resp, err := ask(t, newPort(t, tr))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Model != "qwen-substituted" {
		t.Fatalf("served model %q", resp.Model)
	}
}

// TestAReplyThatNamesNoModelLeavesItUnknown: echoing the requested one would
// report a fact nobody confirmed.
func TestAReplyThatNamesNoModelLeavesItUnknown(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{streamed(
		`{"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)}}
	resp, err := ask(t, newPort(t, tr))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Model != "" {
		t.Fatalf("a model nobody named became %q", resp.Model)
	}
}

// TestThisPortRefusesAConfigurationItCannotHonour: each seam is required, so a
// test cannot reach the network or a real credential by omission, and a request
// cannot be built without an output cap.
func TestThisPortRefusesAConfigurationItCannotHonour(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*qwen.Config)
	}{
		{"no model", func(c *qwen.Config) { c.Model = "" }},
		{"no transport", func(c *qwen.Config) { c.Transport = nil }},
		{"no credential", func(c *qwen.Config) { c.Credential = ai.Credential{} }},
		{"no output cap", func(c *qwen.Config) { c.MaxOutputTokens = 0 }},
		{"negative output cap", func(c *qwen.Config) { c.MaxOutputTokens = -1 }},
		{"negative window", func(c *qwen.Config) { c.ContextWindow = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := qwen.Config{
				Model: "m", Transport: &recordedTransport{}, Credential: fixedKey,
				MaxOutputTokens: 8,
			}
			tc.mutate(&cfg)
			if _, err := qwen.New(cfg); err == nil {
				t.Fatal("a configuration that could not work was accepted")
			}
		})
	}
}

// TestAMissingCredentialIsATypedAbsence: a caller must be able to tell "nothing
// configured" from "the provider rejected what we sent", before anything is
// billed rather than from the reply to a request.
func TestAMissingCredentialIsATypedAbsence(t *testing.T) {
	tr := &recordedTransport{}
	_, err := qwen.New(qwen.Config{Model: "m", Transport: tr, MaxOutputTokens: 8})
	var classified *qwen.Error
	if !errors.As(err, &classified) {
		t.Fatalf("a missing credential produced %v, which a caller cannot branch on", err)
	}
	if classified.Failure != qwen.FailureAuth {
		t.Fatalf("classified %s", classified.Failure)
	}
	if tr.requests != 0 {
		t.Fatalf("made %d requests without a credential", tr.requests)
	}
}

// TestARequestForAnotherModelIsRefusedWithoutAsking: this port serves one
// model, and answering from another would answer from a model the caller's
// configuration never chose.
func TestARequestForAnotherModelIsRefusedWithoutAsking(t *testing.T) {
	tr := &recordedTransport{}
	_, err := newPort(t, tr).Generate(context.Background(), ai.Request{
		Model:    "some-other-model",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("a request for another model was served")
	}
	if tr.requests != 0 {
		t.Fatalf("made %d requests before refusing", tr.requests)
	}
}

// TestAConfigDoesNotPrintACallersSecret: a config holds a resolved key, and
// anything that formats a struct reaches every field it has.
func TestAConfigDoesNotPrintACallersSecret(t *testing.T) {
	const secret = "sk-a-callers-secret-value"
	cfg := qwen.Config{
		Model: "m", Transport: &recordedTransport{}, MaxOutputTokens: 8,
		Credential: ai.StoredCredential(secret, "a test"),
	}
	for name, rendered := range map[string]string{
		"%v":  fmt.Sprintf("%v", cfg),
		"%+v": fmt.Sprintf("%+v", cfg),
		"%#v": fmt.Sprintf("%#v", cfg),
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("formatting a config with %s printed the key: %s", name, rendered)
		}
	}
}

// TestQuotaAndThrottleReachOppositeOutcomes: both can arrive as the same
// status, and telling them apart is the difference between waiting and stopping.
func TestQuotaAndThrottleReachOppositeOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		want      ai.Failure
		retryable bool
	}{
		{"an exhausted balance", 429, `{"error":{"code":"Arrearage","message":"gone"}}`, ai.FailureQuota, false},
		{"an ordinary throttle", 429, `{"error":{"code":"Throttling.RateQuota","message":"slow"}}`, ai.FailureThrottled, true},
		{"a rejected credential", 401, `{"error":{"code":"InvalidApiKey","message":"no"}}`, ai.FailureAuth, false},
		{"a server error", 503, `{"error":{"message":"down"}}`, ai.FailureTransient, true},
		{"an unwrapped refusal", 400, `{"code":"InvalidParameter","message":"bad"}`, ai.FailureRefused, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &recordedTransport{responses: []*http.Response{refused(tc.status, tc.body)}}
			_, err := ask(t, newPort(t, tr))
			failure, ok := ai.FailureOf(err)
			if !ok {
				t.Fatalf("a caller cannot branch on %v", err)
			}
			if failure != tc.want {
				t.Fatalf("classified %s, want %s", failure, tc.want)
			}
			if ai.Retryable(err) != tc.retryable {
				t.Fatalf("retryable %v, want %v", ai.Retryable(err), tc.retryable)
			}
			if tr.requests != 1 {
				t.Fatalf("made %d requests; this port does not retry", tr.requests)
			}
		})
	}
}

// TestARefusedCallLedgersWhatItRead: a request the provider read is a request
// it charged for, whatever it answered.
func TestARefusedCallLedgersWhatItRead(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(429,
		`{"error":{"code":"Throttling","message":"slow"},"usage":{"prompt_tokens":37,"completion_tokens":0}}`)}}
	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	var carrier interface{ Consumed() []ai.Usage }
	if !errors.As(err, &carrier) {
		t.Fatalf("a refused call reported no usage at all: %v", err)
	}
	used := carrier.Consumed()
	if len(used) != 1 || used[0].InputTokens != 37 {
		t.Fatalf("the refused call ledgered %+v", used)
	}
}

// TestTheProvidersOwnRetryInstructionSurvives: nothing here retries, so an
// instruction that stops here is one the caller who decides never learns of.
func TestTheProvidersOwnRetryInstructionSurvives(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		advice string
		want   bool
	}{
		{"a transient status the provider says not to repeat", 503, `{"error":{"message":"down"}}`, "false", false},
		{"a terminal status the provider asks to repeat", 400, `{"error":{"message":"odd"}}`, "true", true},
		{"an exhausted balance the provider asks to repeat", 429, `{"error":{"code":"Arrearage","message":"gone"}}`, "true", false},
		{"a transient status with no instruction", 503, `{"error":{"message":"down"}}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := refused(tc.status, tc.body)
			if tc.advice != "" {
				resp.Header.Set("x-should-retry", tc.advice)
			}
			tr := &recordedTransport{responses: []*http.Response{resp}}
			_, err := ask(t, newPort(t, tr))
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if got := ai.Retryable(err); got != tc.want {
				t.Fatalf("Retryable %v, want %v, for %v", got, tc.want, err)
			}
		})
	}
}

// TestAFailureInsideA200IsNotASuccess: a reply can fail after a 200, and
// reading only the status would hand back a partial answer as the model's word.
func TestAFailureInsideA200IsNotASuccess(t *testing.T) {
	for _, tc := range []struct {
		name  string
		final string
		want  ai.Failure
	}{
		{
			name:  "an exhausted balance",
			final: `{"id":"c1","model":"m","error":{"code":"Arrearage","message":"gone"}}`,
			want:  ai.FailureQuota,
		},
		{
			name:  "no finish reason at all",
			final: `{"id":"c1","model":"m","choices":[{"index":0,"delta":{}}]}`,
			want:  ai.FailureUnknown,
		},
		{
			name:  "a finish reason nothing maps",
			final: `{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"something_new"}]}`,
			want:  ai.FailureUnknown,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &recordedTransport{responses: []*http.Response{streamed(
				`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"}}]}`,
				tc.final,
			)}}
			_, err := ask(t, newPort(t, tr))
			failure, ok := ai.FailureOf(err)
			if !ok {
				t.Fatalf("a failure inside a 200 arrived as %v", err)
			}
			if failure != tc.want {
				t.Fatalf("classified %s, want %s", failure, tc.want)
			}
		})
	}
}

// TestAnOverflowIsRecoverableWhereverItIsReported: the runtime recovers from
// this by shortening, so it has to arrive as the same sentinel from both places.
func TestAnOverflowIsRecoverableWhereverItIsReported(t *testing.T) {
	t.Run("reported by a status", func(t *testing.T) {
		tr := &recordedTransport{responses: []*http.Response{refused(400,
			`{"error":{"code":"context_length_exceeded","message":"too long"}}`)}}
		if _, err := ask(t, newPort(t, tr)); !errors.Is(err, ai.ErrContextOverflow) {
			t.Fatalf("an overflow arrived as %v", err)
		}
	})
	t.Run("reported inside a 200", func(t *testing.T) {
		tr := &recordedTransport{responses: []*http.Response{streamed(
			`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
			`{"id":"c1","model":"m","error":{"code":"range_of_input_length_exceeded_limit","message":"too long"}}`,
		)}}
		if _, err := ask(t, newPort(t, tr)); !errors.Is(err, ai.ErrContextOverflow) {
			t.Fatalf("an overflow inside a 200 arrived as %v", err)
		}
	})
}

// TestCountBasedOverflowDetection reads typed numbers, never text.
func TestCountBasedOverflowDetection(t *testing.T) {
	windowed := func(t *testing.T, tr http.RoundTripper, window int) *qwen.Port {
		t.Helper()
		p, err := qwen.New(qwen.Config{
			Model: "qwen-test", Transport: tr, Credential: fixedKey,
			MaxOutputTokens: 16, ContextWindow: window,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return p
	}
	completed := func(usage string) *recordedTransport {
		return &recordedTransport{responses: []*http.Response{streamed(
			`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}`,
			`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":`+usage+`}`,
		)}}
	}
	call := func(p *qwen.Port) error {
		_, err := p.Generate(context.Background(), ai.Request{
			Model: "qwen-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
		})
		return err
	}

	t.Run("accepted input beyond the window", func(t *testing.T) {
		tr := completed(`{"prompt_tokens":150,"completion_tokens":2}`)
		if err := call(windowed(t, tr, 100)); !errors.Is(err, ai.ErrContextOverflow) {
			t.Fatalf("input past the window arrived as %v", err)
		}
	})
	t.Run("cached tokens occupy the window too", func(t *testing.T) {
		tr := completed(`{"prompt_tokens":150,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":120}}`)
		if err := call(windowed(t, tr, 100)); !errors.Is(err, ai.ErrContextOverflow) {
			t.Fatalf("a cached prompt past the window arrived as %v", err)
		}
	})
	t.Run("within the window is not an overflow", func(t *testing.T) {
		tr := completed(`{"prompt_tokens":50,"completion_tokens":2}`)
		if err := call(windowed(t, tr, 100)); err != nil {
			t.Fatalf("a request that fitted was refused: %v", err)
		}
	})
	t.Run("no window leaves the check off", func(t *testing.T) {
		tr := completed(`{"prompt_tokens":150,"completion_tokens":2}`)
		if err := call(windowed(t, tr, 0)); err != nil {
			t.Fatalf("a window nobody measured was invented: %v", err)
		}
	})
	t.Run("unreported usage is not read as zero", func(t *testing.T) {
		tr := &recordedTransport{responses: []*http.Response{streamed(
			`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}`,
			`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)}}
		if err := call(windowed(t, tr, 100)); err != nil {
			t.Fatalf("silence was read as a measurement: %v", err)
		}
	})
}

// failingTransport fails the way a network does, without a response.
type failingTransport struct {
	err      error
	requests int
}

func (f *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.requests++
	return nil, f.err
}

// TestATransportFailureLeavesTyped: an error that arrives as prose forces a
// caller to read text, which the typed set exists to remove.
func TestATransportFailureLeavesTyped(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want ai.Failure
	}{
		{"a truncated body", io.ErrUnexpectedEOF, ai.FailureTransient},
		{"a refused connection", &net.OpError{Op: "dial", Err: errors.New("refused")}, ai.FailureTransient},
		{"something unrecognised", errors.New("the adapter disagreed"), ai.FailureUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ask(t, newPort(t, &failingTransport{err: tc.err}))
			failure, ok := ai.FailureOf(err)
			if !ok {
				t.Fatalf("a caller cannot branch on %v", err)
			}
			if failure != tc.want {
				t.Fatalf("classified %s, want %s", failure, tc.want)
			}
			if got := ai.Retryable(err); got != (tc.want == ai.FailureTransient) {
				t.Fatalf("Retryable %v for %s", got, tc.want)
			}
		})
	}
}

// TestATransportErrorDoesNotCarryTheConfiguredKey: a transport error names the
// request it failed on, headers and all.
func TestATransportErrorDoesNotCarryTheConfiguredKey(t *testing.T) {
	const secret = "9f2c-not-shaped-like-a-key"
	p, err := qwen.New(qwen.Config{
		Model: "qwen-test", MaxOutputTokens: 8,
		Credential: ai.StoredCredential(secret, "a test"),
		Transport:  &failingTransport{err: fmt.Errorf("proxy rejected Authorization=%s", secret)},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, genErr := p.Generate(context.Background(), ai.Request{
		Model: "qwen-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if genErr == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(genErr.Error(), secret) {
		t.Fatalf("the configured key reached the error: %v", genErr)
	}
}

// TestARedirectIsNotFollowed: a redirect is another request, and the default
// client would make it — carrying the credential to wherever it points.
func TestARedirectIsNotFollowed(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{
		{
			StatusCode: 307,
			Header:     http.Header{"Location": []string{"https://elsewhere.example/v1/chat/completions"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"moved"}}`)),
		},
		streamed(`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
	}}
	if _, err := ask(t, newPort(t, tr)); err == nil {
		t.Fatal("a redirect was followed and reported as success")
	}
	if tr.requests != 1 {
		t.Fatalf("a redirect turned one call into %d requests", tr.requests)
	}
}

// TestCancellationStaysCancellation: a caller that cannot tell its own stop
// from a provider failure will report the wrong thing and may retry what it
// just stopped.
func TestCancellationStaysCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newPort(t, &failingTransport{err: context.Canceled}).Generate(ctx, ai.Request{
		Model: "qwen-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation arrived as %v", err)
	}
	if _, classified := ai.FailureOf(err); classified {
		t.Fatalf("the caller's own stop was reported as a provider failure: %v", err)
	}
}

// haltingBody delivers what it was given and then fails, as a body does when
// the transport underneath it is stopped mid-reply.
type haltingBody struct {
	prefix *strings.Reader
	err    error
}

func (h *haltingBody) Read(p []byte) (int, error) {
	if h.prefix.Len() > 0 {
		return h.prefix.Read(p)
	}
	return 0, h.err
}

func (h *haltingBody) Close() error { return nil }

// TestAStreamStoppedMidReplyEndsAborted: a stopped call is not a failed one.
// The caller's context stays live here, so only the error chain can say what
// happened.
func TestAStreamStoppedMidReplyEndsAborted(t *testing.T) {
	for name, cause := range map[string]error{
		"a cancellation": context.Canceled,
		"a deadline":     context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			tr := &recordedTransport{responses: []*http.Response{{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body: &haltingBody{
					prefix: strings.NewReader("data: " +
						`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"already said"}}]}` +
						"\n\n"),
					err: fmt.Errorf("transport stopped: %w", cause),
				},
			}}}
			events, err := newPort(t, tr).Stream(context.Background(), ai.Request{
				Model: "qwen-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
			})
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			var final *ai.AssistantMessage
			terminals := 0
			for ev := range events {
				if ev.Terminal() {
					terminals++
					final = ev.Final
				}
			}
			if terminals != 1 {
				t.Fatalf("the stream delivered %d terminal events, want exactly 1", terminals)
			}
			if final.StopReason != ai.StopAborted {
				t.Fatalf("a stopped call ended as %q, not aborted", final.StopReason)
			}
			if !errors.Is(final.Cause, cause) {
				t.Fatalf("the cause was lost: %v", final.Cause)
			}
			if ai.Retryable(final.Cause) {
				t.Fatalf("a stopped call was judged worth repeating: %v", final.Cause)
			}
			var shown strings.Builder
			for _, b := range final.Blocks {
				shown.WriteString(b.Text)
			}
			if shown.String() != "already said" {
				t.Fatalf("content delivered before the stop was lost: %q", shown.String())
			}
		})
	}
}

// TestAnOrdinaryReadFailureStillEndsAsAFailure guards the abort path from
// swallowing the case it does not cover.
func TestAnOrdinaryReadFailureStillEndsAsAFailure(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: &haltingBody{
			prefix: strings.NewReader("data: " +
				`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"}}]}` +
				"\n\n"),
			err: io.ErrUnexpectedEOF,
		},
	}}}
	events, err := newPort(t, tr).Stream(context.Background(), ai.Request{
		Model: "qwen-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var final *ai.AssistantMessage
	for ev := range events {
		if ev.Terminal() {
			final = ev.Final
		}
	}
	if final == nil || final.StopReason != ai.StopError {
		t.Fatalf("a broken stream ended as %v", final)
	}
	if _, classified := ai.FailureOf(final.Cause); !classified {
		t.Fatalf("a caller cannot branch on %v", final.Cause)
	}
}

// TestABlockEndsBeforeTheNextBegins: a consumer rendering as it goes is never
// told a block is still growing when it is not.
func TestABlockEndsBeforeTheNextBegins(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{streamed(
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking"}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"answer"}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"alpha","arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)}}
	events, err := newPort(t, tr).Stream(context.Background(), ai.Request{
		Model: "qwen-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	open := map[int]bool{}
	for ev := range events {
		switch ev.Kind {
		case ai.StreamThinkingStart, ai.StreamTextStart, ai.StreamToolCallStart:
			for at := range open {
				t.Fatalf("block %d began while %d was still open", ev.ContentIndex, at)
			}
			open[ev.ContentIndex] = true
		case ai.StreamThinkingEnd, ai.StreamTextEnd, ai.StreamToolCallEnd:
			delete(open, ev.ContentIndex)
		}
	}
	if len(open) != 0 {
		t.Fatalf("the reply ended with %d blocks still open", len(open))
	}
}

// TestACancellationInsideATransportErrorStaysCancellation.
//
// The caller's own context is not the only place a stop appears: a transport
// can report one it was told about before that context is observably done.
// Classified, it would tell a caller to retry what was just stopped — and a
// deadline would leave as retryable, which is worse than merely wrong.
func TestACancellationInsideATransportErrorStaysCancellation(t *testing.T) {
	for name, cause := range map[string]error{
		"a cancellation": context.Canceled,
		"a deadline":     context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			// The caller's own context stays live throughout.
			tr := &failingTransport{err: fmt.Errorf("transport stopped: %w", cause)}
			_, err := newPort(t, tr).Generate(context.Background(), ai.Request{
				Model: "qwen-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
			})
			if !errors.Is(err, cause) {
				t.Fatalf("the cause was lost: %v", err)
			}
			if _, classified := ai.FailureOf(err); classified {
				t.Fatalf("a stopped call was reported as a provider failure: %v", err)
			}
			if ai.Retryable(err) {
				t.Fatalf("a stopped call was judged worth repeating: %v", err)
			}
		})
	}
}

// TestStreamingAndCollectingAgree: two ways of asking cannot become two
// answers. The collected path is the streamed one drained, and a difference
// between them would mean one of the two has its own conversion.
func TestStreamingAndCollectingAgree(t *testing.T) {
	chunks := []string{
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking"}}]}`,
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"content":"the "}}]}`,
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"content":"answer"}}]}`,
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"alpha","arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":4}}}`,
	}

	collected, err := ask(t, newPort(t, &recordedTransport{
		responses: []*http.Response{streamed(chunks...)}}))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	events, err := newPort(t, &recordedTransport{
		responses: []*http.Response{streamed(chunks...)}}).Stream(context.Background(), ai.Request{
		Model: "qwen-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var final *ai.AssistantMessage
	terminals := 0
	for ev := range events {
		if ev.Terminal() {
			terminals++
			final = ev.Final
		}
	}
	if terminals != 1 {
		t.Fatalf("the stream delivered %d terminal events, want exactly 1", terminals)
	}

	var text, reasoning strings.Builder
	var calls []ai.ToolCall
	for _, b := range final.Blocks {
		switch b.Kind {
		case ai.BlockText:
			text.WriteString(b.Text)
		case ai.BlockThinking:
			reasoning.WriteString(b.Text)
		case ai.BlockToolCall:
			calls = append(calls, b.Call)
		}
	}
	if text.String() != collected.Content {
		t.Fatalf("streamed content %q, collected %q", text.String(), collected.Content)
	}
	if reasoning.String() != collected.Reasoning {
		t.Fatalf("streamed reasoning %q, collected %q", reasoning.String(), collected.Reasoning)
	}
	// Compared call by call, not counted. A collected reply that kept the right
	// number of calls and lost every id, name and argument would answer the
	// count and be useless to dispatch.
	if len(calls) != len(collected.ToolCalls) {
		t.Fatalf("streamed %d calls, collected %d", len(calls), len(collected.ToolCalls))
	}
	for at, streamedCall := range calls {
		got := collected.ToolCalls[at]
		if got != streamedCall {
			t.Fatalf("call %d streamed as %+v and collected as %+v", at, streamedCall, got)
		}
		if got.ID == "" || got.Name == "" {
			t.Fatalf("call %d arrived with no identity to dispatch: %+v", at, got)
		}
	}
	if final.Model != collected.Model {
		t.Fatalf("streamed model %q, collected %q", final.Model, collected.Model)
	}
	if final.Usage.Total() != collected.Usage.Total() {
		t.Fatalf("streamed total %d, collected %d", final.Usage.Total(), collected.Usage.Total())
	}
	// A reported zero and an absent count must agree too, not just the total.
	if final.Usage.Reported != collected.Usage.Reported {
		t.Fatal("the two paths disagree about whether usage was reported at all")
	}
	// The optional counts as well, presence and value: a total can agree while
	// one path invents a cached count and the other leaves it absent.
	if !sameOptional(final.Usage.CacheReadTokens, collected.Usage.CacheReadTokens) {
		t.Fatalf("streamed cache read %v, collected %v",
			final.Usage.CacheReadTokens, collected.Usage.CacheReadTokens)
	}
	if !sameOptional(final.Usage.ReasoningTokens, collected.Usage.ReasoningTokens) {
		t.Fatalf("streamed reasoning tokens %v, collected %v",
			final.Usage.ReasoningTokens, collected.Usage.ReasoningTokens)
	}
	if final.Usage.CacheReadTokens == nil {
		t.Fatal("this fixture reports a cached count; without one the comparison above proves nothing")
	}
	// The ending of this reply too, not only the truncated one below: a reply
	// that asked for tools is a different outcome from one that finished.
	if final.StopReason != ai.StopToolUse {
		t.Fatalf("a reply that asked for tools ended as %q", final.StopReason)
	}
	if collected.Truncated {
		t.Fatal("a reply that asked for tools was collected as cut short")
	}

	// And how the reply ended. The collected path carries the ending as the one
	// thing a caller acts on — whether the answer was cut short — so a
	// disagreement here is a caller told a truncated reply was complete.
	cut := []string{
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"role":"assistant","content":"half an "}}]}`,
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":2,"completion_tokens":9}}`,
	}
	short, err := ask(t, newPort(t, &recordedTransport{
		responses: []*http.Response{streamed(cut...)}}))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !short.Truncated {
		t.Fatal("a reply the provider cut short was collected as complete")
	}
	shortEvents, err := newPort(t, &recordedTransport{
		responses: []*http.Response{streamed(cut...)}}).Stream(context.Background(), ai.Request{
		Model: "qwen-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var shortFinal *ai.AssistantMessage
	for ev := range shortEvents {
		if ev.Terminal() {
			shortFinal = ev.Final
		}
	}
	if shortFinal.StopReason != ai.StopLength {
		t.Fatalf("streamed ending %q, want the cap doing its job", shortFinal.StopReason)
	}
	if short.Truncated != (shortFinal.StopReason == ai.StopLength) {
		t.Fatal("the two paths disagree about whether the reply was cut short")
	}
}

// sameOptional reports whether two optional counts agree in presence and value.
func sameOptional(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// TestABodyReadFailureDoesNotCarryTheCallsKey: a read that fails partway can
// name the request it was reading. Left to the shape pass alone, a key that
// does not look like one survives into the report.
func TestABodyReadFailureDoesNotCarryTheCallsKey(t *testing.T) {
	const secret = "7c1e-not-shaped-like-a-key"
	p, err := qwen.New(qwen.Config{
		Model: "qwen-test", MaxOutputTokens: 8,
		Credential: ai.StoredCredential(secret, "a test"),
		Transport: &recordedTransport{responses: []*http.Response{{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: &haltingBody{
				prefix: strings.NewReader("data: " +
					`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"}}]}` +
					"\n\n"),
				err: fmt.Errorf("proxy dropped the connection for Authorization=%s", secret),
			},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, genErr := p.Generate(context.Background(), ai.Request{
		Model: "qwen-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if genErr == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(genErr.Error(), secret) {
		t.Fatalf("the key this call used reached the failure: %v", genErr)
	}
}

// openBody delivers what it was given and then waits, ending only when the
// caller's context does — which is what a real transport does to a body when
// the request it belongs to is cancelled. A fixture that ignored the context
// would hang instead of proving anything.
type openBody struct {
	prefix *strings.Reader
	ctx    context.Context
}

func (b *openBody) Read(p []byte) (int, error) {
	if b.prefix.Len() > 0 {
		return b.prefix.Read(p)
	}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *openBody) Close() error { return nil }

// TestACancelledStreamStillEnds: a consumer that watched a reply appear should
// not have it vanish because they stopped it, and a channel that simply closes
// says nothing about what they have.
func TestACancelledStreamStillEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Cancelled below once content has arrived; this releases the body if it
	// never does, so a fixture that stops sending fails the test rather than
	// hanging it.
	defer cancel()
	tr := &recordedTransport{responses: []*http.Response{{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: &openBody{
			prefix: strings.NewReader("data: " +
				`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"already said"}}]}` +
				"\n\n"),
			ctx: ctx,
		},
	}}}
	events, err := newPort(t, tr).Stream(ctx, ai.Request{
		Model: "qwen-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var final *ai.AssistantMessage
	terminals := 0
	for ev := range events {
		if ev.Kind == ai.StreamTextDelta {
			// Cancelled only once something has arrived, so the terminal has
			// something to carry.
			cancel()
		}
		if ev.Terminal() {
			terminals++
			final = ev.Final
		}
	}
	if terminals != 1 {
		t.Fatalf("a cancelled stream delivered %d terminal events, want exactly 1", terminals)
	}
	if final.StopReason != ai.StopAborted {
		t.Fatalf("a cancelled stream ended as %q", final.StopReason)
	}
	if !errors.Is(final.Cause, context.Canceled) {
		t.Fatalf("the cause was lost: %v", final.Cause)
	}
	var shown strings.Builder
	for _, b := range final.Blocks {
		shown.WriteString(b.Text)
	}
	if shown.String() != "already said" {
		t.Fatalf("what had already arrived was lost: %q", shown.String())
	}
}
