package openrouter_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/openrouter"
)

// recordedTransport replays a recorded exchange and remembers what was sent.
type recordedTransport struct {
	requests  int
	responses []*http.Response
	headers   []http.Header
}

func (r *recordedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.requests++
	r.headers = append(r.headers, req.Header.Clone())
	if r.requests > len(r.responses) {
		return nil, io.ErrUnexpectedEOF
	}
	return r.responses[r.requests-1], nil
}

var fixedKey = ai.StoredCredential("test-key", "a test")

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

func newPort(t *testing.T, tr http.RoundTripper, shape func(*openrouter.Config)) *openrouter.Port {
	t.Helper()
	cfg := openrouter.Config{
		// An aggregator addresses models as vendor/model, and the port serves
		// exactly the one it was configured with.
		Model: "anthropic/claude-test", Transport: tr,
		Credential: fixedKey, MaxOutputTokens: 64,
	}
	if shape != nil {
		shape(&cfg)
	}
	p, err := openrouter.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func ask(t *testing.T, p *openrouter.Port) (ai.Response, error) {
	t.Helper()
	return p.Generate(context.Background(), ai.Request{
		Model:    "anthropic/claude-test",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hello"}},
	})
}

// TestAReplyArrivesThroughTheAdapter proves the wiring: this port supplies only
// a model builder and a classifier, and everything else comes from the shared
// dialect. A reply arriving at all is what says the two were connected
// correctly.
func TestAReplyArrivesThroughTheAdapter(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{streamed(
		`{"choices":[{"delta":{"content":"hi"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`,
	)}}
	got, err := ask(t, newPort(t, tr, nil))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.Content != "hi" {
		t.Fatalf("content %q", got.Content)
	}
	if tr.requests != 1 {
		t.Fatalf("one call sent %d requests", tr.requests)
	}
	if !got.Usage.Reported || got.Usage.InputTokens != 5 {
		t.Fatalf("usage came back as %+v", got.Usage)
	}
}

// TestAModerationRefusalIsNotAnAuthenticationFailure is the reason this
// provider cannot share the other ports' status mapping.
//
// OpenRouter answers 403 when its moderator refuses the input. Read as
// authentication — which is what 403 means to the other ports here — it would
// send a user to check a key that is working perfectly while the actual problem
// is what they asked for.
func TestAModerationRefusalIsNotAnAuthenticationFailure(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(403,
		`{"error":{"code":403,"message":"Input flagged","metadata":{"reasons":["harassment"]}}}`)}}

	_, err := ask(t, newPort(t, tr, nil))
	if err == nil {
		t.Fatal("a refused request reported success")
	}
	failure, ok := ai.FailureOf(err)
	if !ok {
		t.Fatalf("the refusal carries no classification: %v", err)
	}
	if failure == ai.FailureAuth {
		t.Fatalf("a moderation refusal was classified as an authentication failure: %v", err)
	}
	if failure != ai.FailureRefused {
		t.Fatalf("a moderation refusal was classified as %q", failure)
	}
	// The distinction is for the person reading it, since the classification is
	// the same as any other refusal.
	if !strings.Contains(err.Error(), "moderation") {
		t.Fatalf("the failure does not say what refused it: %v", err)
	}
}

// TestTheStatusesThisProviderDocuments, and what each has to mean for the
// caller above to act on it.
func TestTheStatusesThisProviderDocuments(t *testing.T) {
	cases := map[int]ai.Failure{
		401: ai.FailureAuth,
		// Insufficient credits: an exhausted balance, not a throttle. Retrying
		// spends nothing and fixes nothing.
		402: ai.FailureQuota,
		408: ai.FailureTransient,
		429: ai.FailureThrottled,
		// The model behind the aggregator is down, or no provider is available
		// for it. Both are worth another attempt.
		502: ai.FailureTransient,
		503: ai.FailureTransient,
	}
	for status, want := range cases {
		tr := &recordedTransport{responses: []*http.Response{
			refused(status, `{"error":{"code":`+itoa(status)+`,"message":"nope"}}`)}}
		_, err := ask(t, newPort(t, tr, nil))
		if err == nil {
			t.Fatalf("status %d reported success", status)
		}
		got, ok := ai.FailureOf(err)
		if !ok {
			t.Fatalf("status %d carries no classification: %v", status, err)
		}
		if got != want {
			t.Errorf("status %d classified as %q, want %q", status, got, want)
		}
	}
}

// TestAQuotaRefusalIsNotRetried. Spending against a balance that is already
// gone buys a second identical rejection.
func TestAQuotaRefusalIsNotRetried(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{
		refused(402, `{"error":{"code":402,"message":"Insufficient credits"}}`)}}
	_, err := ask(t, newPort(t, tr, nil))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if ai.Retryable(err) {
		t.Fatalf("an exhausted balance was reported as worth repeating: %v", err)
	}
}

// TestAttributionHeadersAreSentOnlyWhenAsked. They put the caller on a public
// leaderboard, so a caller who did not ask should not appear.
func TestAttributionHeadersAreSentOnlyWhenAsked(t *testing.T) {
	silent := &recordedTransport{responses: []*http.Response{streamed(
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)}}
	ask(t, newPort(t, silent, nil))
	if len(silent.headers) == 0 {
		t.Fatal("nothing was sent")
	}
	if got := silent.headers[0].Get("HTTP-Referer"); got != "" {
		t.Fatalf("a caller who asked for nothing was attributed as %q", got)
	}

	named := &recordedTransport{responses: []*http.Response{streamed(
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)}}
	ask(t, newPort(t, named, func(c *openrouter.Config) {
		c.Referer = "https://example.test"
		c.Title = "pi-go"
	}))
	if got := named.headers[0].Get("HTTP-Referer"); got != "https://example.test" {
		t.Fatalf("the referer arrived as %q", got)
	}
	if got := named.headers[0].Get("X-Title"); got != "pi-go" {
		t.Fatalf("the title arrived as %q", got)
	}
}

// TestServingOnlyTheConfiguredModel. An aggregator makes this matter more, not
// less: one port reaches many vendors, and answering from a model the
// configuration never chose would bill an account for a vendor nobody picked.
func TestServingOnlyTheConfiguredModel(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{streamed(
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)}}
	p := newPort(t, tr, nil)

	_, err := p.Generate(context.Background(), ai.Request{
		Model:    "openai/gpt-test",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hello"}},
	})
	if err == nil {
		t.Fatal("a request naming another model was served")
	}
	if tr.requests != 0 {
		t.Fatalf("a refused request still reached the wire %d times", tr.requests)
	}
}

// TestAConfigurationThatCouldNotWorkIsRefusedAtConstruction, before anything is
// billed.
func TestAConfigurationThatCouldNotWorkIsRefusedAtConstruction(t *testing.T) {
	tr := &recordedTransport{}
	for name, cfg := range map[string]openrouter.Config{
		"no model":      {Transport: tr, Credential: fixedKey, MaxOutputTokens: 8},
		"no transport":  {Model: "m", Credential: fixedKey, MaxOutputTokens: 8},
		"no credential": {Model: "m", Transport: tr, MaxOutputTokens: 8},
		"no output cap": {Model: "m", Transport: tr, Credential: fixedKey},
		"negative window": {Model: "m", Transport: tr, Credential: fixedKey,
			MaxOutputTokens: 8, ContextWindow: -1},
	} {
		if _, err := openrouter.New(cfg); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// TestTheCredentialNeverReachesAFailure. A gateway that echoes headers would
// otherwise put the key into an error a caller logs.
func TestTheCredentialNeverReachesAFailure(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(400,
		`{"error":{"code":400,"message":"bad request with Authorization: Bearer test-key"}}`)}}
	_, err := ask(t, newPort(t, tr, nil))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if strings.Contains(err.Error(), "test-key") {
		t.Fatalf("the credential reached the failure: %v", err)
	}
}

// TestAnUnreadableRefusalStillClassifies. A gateway can answer with HTML, and a
// caller must still learn whether to retry.
func TestAnUnreadableRefusalStillClassifies(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{
		refused(503, `<html>503 Service Unavailable</html>`)}}
	_, err := ask(t, newPort(t, tr, nil))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	got, ok := ai.FailureOf(err)
	if !ok || got != ai.FailureTransient {
		t.Fatalf("an unreadable 503 classified as %q (ok=%v)", got, ok)
	}
	var provider *ai.ProviderError
	if errors.As(err, &provider) && provider.Detail == "" {
		t.Fatal("the failure carries no detail at all")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
