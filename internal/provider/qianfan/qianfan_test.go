package qianfan_test

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/qianfan"
)

// recordedTransport replays a recorded exchange and remembers what was sent.
type recordedTransport struct {
	requests  int
	urls      []string
	responses []*http.Response
}

func (r *recordedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.requests++
	r.urls = append(r.urls, req.URL.String())
	if r.requests > len(r.responses) {
		return nil, io.ErrUnexpectedEOF
	}
	return r.responses[r.requests-1], nil
}

var fixedKey = ai.StoredCredential("qianfan-test-token", "a test")

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

func newPort(t *testing.T, tr http.RoundTripper) *qianfan.Port {
	t.Helper()
	p, err := qianfan.New(qianfan.Config{
		Model: "ernie-test", Transport: tr,
		Credential: fixedKey, MaxOutputTokens: 64,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func ask(t *testing.T, p *qianfan.Port) (ai.Response, error) {
	t.Helper()
	return p.Generate(context.Background(), ai.Request{
		Model:    "ernie-test",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hello"}},
	})
}

func failureOf(t *testing.T, err error) ai.Failure {
	t.Helper()
	got, ok := ai.FailureOf(err)
	if !ok {
		t.Fatalf("the failure carries no classification: %v", err)
	}
	return got
}

// TestAReplyArrivesThroughTheCompatibleEndpoint.
//
// The premise of this port is that the v2 endpoint speaks chat-completions, so
// the shared dialect can drive it. A reply arriving — with the usage the
// capture reads off the wire rather than out of the framework's metadata — is
// what says the premise was carried through correctly.
func TestAReplyArrivesThroughTheCompatibleEndpoint(t *testing.T) {
	// The served model differs from the one asked for on purpose: a provider
	// that substitutes is worth being able to see. On a reply that completes,
	// the framework carries it too — what reading the wire adds shows up on the
	// paths this cannot reach, and is pinned separately by the renumbering
	// guard below.
	tr := &recordedTransport{responses: []*http.Response{streamed(
		`{"choices":[{"delta":{"content":"hi"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}],"model":"ernie-4.5-turbo-128k",`+
			`"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`)}}

	got, err := ask(t, newPort(t, tr))
	if err != nil {
		t.Fatalf("the provider did not answer: %v", err)
	}
	if got.Content != "hi" {
		t.Fatalf("content %q", got.Content)
	}
	if !got.Usage.Reported || got.Usage.InputTokens != 5 || got.Usage.OutputTokens != 1 {
		t.Fatalf("usage did not come off the wire: %+v", got.Usage)
	}
	if got.Model != "ernie-4.5-turbo-128k" {
		t.Fatalf("the model that answered was reported as %q; the wire said ernie-4.5-turbo-128k", got.Model)
	}
	if tr.requests != 1 {
		t.Fatalf("one call sent %d requests", tr.requests)
	}
}

// TestTheCompatibleEndpointIsWhatIsReached, not the classic one.
//
// The two are different surfaces on the same host: different paths, different
// authentication. Reaching the classic one with a bearer token would fail in a
// way that reads as a bad credential rather than as a wrong endpoint.
func TestTheCompatibleEndpointIsWhatIsReached(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(400, `{"error_code":336003}`)}}
	if _, err := ask(t, newPort(t, tr)); err == nil {
		t.Fatal("expected a failure")
	}
	if len(tr.urls) != 1 || !strings.Contains(tr.urls[0], "/v2/chat/completions") {
		t.Fatalf("the call went to %v", tr.urls)
	}
}

// TestTheCodesThisProvidersOwnSDKNames.
//
// Taken from the constants the vendor's client compiles against, which is a
// better source than a page about them. Two of these change what a caller
// should do and are invisible in the HTTP status.
func TestTheCodesThisProvidersOwnSDKNames(t *testing.T) {
	for _, c := range []struct {
		name string
		code int
		want ai.Failure
	}{
		{"api token invalid", 110, ai.FailureAuth},
		{"api token expired", 111, ai.FailureAuth},
		{"no permission to access", 6, ai.FailureAuth},

		// Quota, not a throttle: an allowance already spent does not refill by
		// waiting a moment, and reporting it as a throttle sends a caller to
		// retry against nothing.
		{"daily limit reached", 17, ai.FailureQuota},
		{"total request limit", 19, ai.FailureQuota},

		{"qps limit", 18, ai.FailureThrottled},
		{"rpm limit", 336501, ai.FailureThrottled},
		{"tpm limit", 336502, ai.FailureThrottled},

		{"service unavailable", 2, ai.FailureTransient},
		{"server high load", 336100, ai.FailureTransient},
		{"internal error", 336000, ai.FailureTransient},

		{"invalid param", 336003, ai.FailureRefused},
		{"invalid json", 336002, ai.FailureRefused},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Sent under a status that would classify differently on its own,
			// which is what makes this a test of the code and not the status.
			tr := &recordedTransport{responses: []*http.Response{refused(400,
				`{"error_code":`+itoa(c.code)+`,"error_msg":"something"}`)}}
			_, err := ask(t, newPort(t, tr))
			if err == nil {
				t.Fatal("expected a failure")
			}
			if got := failureOf(t, err); got != c.want {
				t.Fatalf("code %d classified as %q, want %q", c.code, got, c.want)
			}
		})
	}
}

// TestAModelThisAccountCannotUseIsNotReportedAsABadCredential.
//
// **The provider answers an unknown model with HTTP 401.** Measured, not
// assumed — recorded on 2026-08-31 from a real refusal. Classified on the
// status alone, that sends a user to replace a key that is working perfectly
// while the actual problem is the name they typed.
//
// The refusal here is the whole reason the code is read before the status, and
// the reason this port was rewritten after its first live run: the vocabulary
// it had been given by the vendor's SDK is the CLASSIC surface's, and the
// compatible endpoint uses none of it.
func TestAModelThisAccountCannotUseIsNotReportedAsABadCredential(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(401,
		`{"error":{"code":"invalid_model","message":"The model does not exist or you do not have access to it.",`+
			`"type":"invalid_request_error"},"id":"as-bkv66krfus"}`)}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("an unknown model reported success")
	}
	if got := failureOf(t, err); got != ai.FailureRefused {
		t.Fatalf("an unknown model was classified as %q; the user would go and change their key", got)
	}
	if !strings.Contains(err.Error(), "invalid_model") {
		t.Fatalf("the failure does not say which refusal it was: %v", err)
	}
	// The request id is what this provider's own support asks for, and nothing
	// here could reconstruct it.
	if !strings.Contains(err.Error(), "as-bkv66krfus") {
		t.Fatalf("the request id was dropped: %v", err)
	}
}

// TestARealCredentialFailureIsStillOne. The mapping above must not turn every
// 401 into a refusal — the case it exists to separate has to stay separated.
func TestARealCredentialFailureIsStillOne(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(401,
		`{"error":{"code":"invalid_iam_token","message":"invalid_iam_token",`+
			`"type":"invalid_request_error"},"id":"as-r43wh8e35y"}`)}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("a bad credential reported success")
	}
	if got := failureOf(t, err); got != ai.FailureAuth {
		t.Fatalf("a bad credential was classified as %q", got)
	}
}

// TestAnAccountInArrearsIsNotReportedAsABadCredential.
//
// The provider answers an overdue account with HTTP **403** — measured on
// 2026-08-31, when the credential under test ran out mid-session. Every port
// here reads 403 as authentication, so without the code this failure tells a
// user to go and check a key that is perfectly valid, when what they have to do
// is settle a bill.
//
// It is also not a throttle: waiting does not add money. Classified as quota,
// which is the one class that means "this will not succeed until you do
// something outside this program".
func TestAnAccountInArrearsIsNotReportedAsABadCredential(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(403,
		`{"error":{"code":"account_overdue","message":"Access denied due to overdue account",`+
			`"type":"access_denied"},"id":"as-xp1jmkxe03"}`)}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("an overdue account reported success")
	}
	if got := failureOf(t, err); got != ai.FailureQuota {
		t.Fatalf("an overdue account was classified as %q; the user would go and change their key", got)
	}
	if ai.Retryable(err) {
		t.Fatalf("an overdue account was reported as worth retrying: %v", err)
	}
	if !strings.Contains(err.Error(), "overdue") {
		t.Fatalf("the failure does not say what is wrong: %v", err)
	}
}

// TestTheRecordedRefusalsAreClassifiedAsRecorded.
//
// Every one of these is a body this provider actually sent, captured on
// 2026-08-31 with the owner's credential. Written from what came back rather
// than from what a source said would come back — which is the difference this
// port exists to demonstrate, since the first version was written the other way
// and was wrong.
func TestTheRecordedRefusalsAreClassifiedAsRecorded(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		want   ai.Failure
	}{
		{"a missing model parameter", 400,
			`{"error":{"code":"invalid_argument","message":"you must provide a model parameter","type":"invalid_request_error"},"id":"as-yqc9riacmc"}`,
			ai.FailureRefused},
		{"a role the provider does not accept", 400,
			`{"error":{"code":"invalid_argument","message":"the role must be one of the following: system,developer,user,assistant,tool","type":"invalid_request_error"},"id":"as-0vmp2re2ed"}`,
			ai.FailureRefused},
		{"an output cap out of range", 400,
			`{"error":{"code":"invalid_argument","message":"parameter check failed, max_completion_tokens range is [1, 12288]","type":"invalid_request_error"},"id":"as-k3s5gezrta"}`,
			ai.FailureRefused},
		{"no credential at all", 401,
			`{"error":{"code":"invalid_iam_token","message":"invalid_iam_token","type":"invalid_request_error"},"id":"as-qiawvbpq8x"}`,
			ai.FailureAuth},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr := &recordedTransport{responses: []*http.Response{refused(c.status, c.body)}}
			_, err := ask(t, newPort(t, tr))
			if err == nil {
				t.Fatal("expected a failure")
			}
			if got := failureOf(t, err); got != c.want {
				t.Fatalf("classified as %q, want %q", got, c.want)
			}
		})
	}
}

// TestEitherErrorShapeIsRead.
//
// A compatible surface may answer in the envelope it is compatible with or in
// this provider's own flat pair. The flat one carries the vocabulary, so
// reading only the other would classify the informative case on its status
// alone.
func TestEitherErrorShapeIsRead(t *testing.T) {
	for name, body := range map[string]string{
		"the classic surface's flat pair": `{"error_code":17,"error_msg":"Open api daily request limit reached"}`,
		"a number inside the envelope":    `{"error":{"code":"17","message":"Open api daily request limit reached","type":"invalid_request_error"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			tr := &recordedTransport{responses: []*http.Response{refused(400, body)}}
			_, err := ask(t, newPort(t, tr))
			if err == nil {
				t.Fatal("expected a failure")
			}
			if got := failureOf(t, err); got != ai.FailureQuota {
				t.Fatalf("a spent daily allowance in %s classified as %q", name, got)
			}
		})
	}
}

// TestAnUnmappedCodeIsStillCarried. It is the provider's own name for what
// happened, and the reader can look it up where this port could not.
func TestAnUnmappedCodeIsStillCarried(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(400,
		`{"error_code":999999,"error_msg":"something new"}`)}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("expected a failure")
	}
	if !strings.Contains(err.Error(), "999999") {
		t.Fatalf("an unmapped code was dropped: %v", err)
	}
	if got := failureOf(t, err); got != ai.FailureRefused {
		t.Fatalf("an unmapped code fell back to %q rather than the status's answer", got)
	}
}

// TestOneCallSendsOneRequest, counted at the transport.
func TestOneCallSendsOneRequest(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(500,
		`{"error_code":336000,"error_msg":"internal error"}`)}}
	if _, err := ask(t, newPort(t, tr)); err == nil {
		t.Fatal("expected a failure")
	}
	if tr.requests != 1 {
		t.Fatalf("one call sent %d requests", tr.requests)
	}
}

// TestServingOnlyTheConfiguredModel.
func TestServingOnlyTheConfiguredModel(t *testing.T) {
	tr := &recordedTransport{}
	p := newPort(t, tr)
	_, err := p.Generate(context.Background(), ai.Request{
		Model:    "ernie-something-else",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hello"}},
	})
	if err == nil {
		t.Fatal("a request naming another model was served")
	}
	if tr.requests != 0 {
		t.Fatalf("a refused request still reached the provider %d times", tr.requests)
	}
}

// TestAConfigurationThatCouldNotWorkIsRefusedAtConstruction.
func TestAConfigurationThatCouldNotWorkIsRefusedAtConstruction(t *testing.T) {
	tr := &recordedTransport{}
	for name, cfg := range map[string]qianfan.Config{
		"no model":        {Transport: tr, Credential: fixedKey, MaxOutputTokens: 8},
		"no transport":    {Model: "m", Credential: fixedKey, MaxOutputTokens: 8},
		"no credential":   {Model: "m", Transport: tr, MaxOutputTokens: 8},
		"no output cap":   {Model: "m", Transport: tr, Credential: fixedKey},
		"negative window": {Model: "m", Transport: tr, Credential: fixedKey, MaxOutputTokens: 8, ContextWindow: -1},
	} {
		if _, err := qianfan.New(cfg); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// TestTheCredentialNeverReachesAFailure.
func TestTheCredentialNeverReachesAFailure(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(400,
		`{"error_code":336003,"error_msg":"we received qianfan-test-token and disliked it"}`)}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(err.Error(), "qianfan-test-token") {
		t.Fatalf("the credential survived into a failure: %v", err)
	}
}

// TestAnUnreadableRefusalStillClassifies.
func TestAnUnreadableRefusalStillClassifies(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(503, `<html>gateway</html>`)}}
	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("expected a failure")
	}
	if got := failureOf(t, err); got != ai.FailureTransient {
		t.Fatalf("an unreadable 503 classified as %q", got)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestAStreamThatRenumbersItsToolCallsIsRefused.
//
// This is what reading the wire buys that the framework's metadata cannot: the
// adapter renumbers tool-call positions contiguously from zero whatever
// arrived, so a provider that skipped a position looks identical afterwards. A
// skipped position means a call the provider announced and never sent, and
// running the ones that did arrive as though the set were complete is acting on
// half a decision.
//
// It is also the guard that silently disappears if this port ever stops reading
// its provider's bytes, which is why it is tested here rather than assumed from
// the dialect.
func TestAStreamThatRenumbersItsToolCallsIsRefused(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{streamed(
		`{"id":"c1","model":"ernie-test","choices":[{"index":0,"delta":{"tool_calls":`+
			`[{"index":0,"id":"call_a","function":{"name":"alpha","arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"ernie-test","choices":[{"index":0,"delta":{"tool_calls":`+
			`[{"index":2,"id":"call_c","function":{"name":"gamma","arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"ernie-test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("a stream that skipped a tool-call position was accepted")
	}
	if !strings.Contains(err.Error(), "skips") {
		t.Fatalf("the failure does not say what was wrong: %v", err)
	}
}
