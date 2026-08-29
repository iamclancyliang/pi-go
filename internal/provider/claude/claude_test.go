package claude_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/claude"
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

var fixedKey = ai.StoredCredential("sk-ant-test-key", "a test")

func refused(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newPort(t *testing.T, tr http.RoundTripper) *claude.Port {
	t.Helper()
	p, err := claude.New(claude.Config{
		Model: "claude-test", Transport: tr,
		Credential: fixedKey, MaxOutputTokens: 64,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func ask(t *testing.T, p *claude.Port) (ai.Response, error) {
	t.Helper()
	return p.Generate(context.Background(), ai.Request{
		Model:    "claude-test",
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

// TestPermissionIsNotAnAuthenticationProblemToReportAsOne.
//
// Both classify as auth, and the DETAIL is what separates them: a key that
// works but may not use this model is fixed by changing the model or the
// account, and an error saying only "authentication" sends a user to replace a
// credential that is fine.
func TestPermissionIsNotAnAuthenticationProblemToReportAsOne(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(403,
		`{"type":"error","error":{"type":"permission_error","message":"Your API key does not have permission to use the specified resource"}}`)}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("a permission refusal reported success")
	}
	if got := failureOf(t, err); got != ai.FailureAuth {
		t.Fatalf("a permission refusal was classified as %q", got)
	}
	if !strings.Contains(err.Error(), "permission_error") {
		t.Fatalf("the failure does not say which of the two it was: %v", err)
	}
}

// TestTheStatusesAndTypesThisProviderDocuments.
//
// The type is asked before the status because it is more precise. 529 is this
// provider's own — the service is up and over capacity — and reading it as a
// generic server fault would still be transient, so the case that matters is
// that it is not read as a refusal.
func TestTheStatusesAndTypesThisProviderDocuments(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		want   ai.Failure
	}{
		{"authentication", 401, `{"error":{"type":"authentication_error","message":"invalid x-api-key"}}`, ai.FailureAuth},
		{"rate limit", 429, `{"error":{"type":"rate_limit_error","message":"slow down"}}`, ai.FailureThrottled},
		{"overloaded", 529, `{"error":{"type":"overloaded_error","message":"Overloaded"}}`, ai.FailureTransient},
		{"server fault", 500, `{"error":{"type":"api_error","message":"Internal server error"}}`, ai.FailureTransient},
		{"not found", 404, `{"error":{"type":"not_found_error","message":"model: claude-test"}}`, ai.FailureRefused},
		{"bad request", 400, `{"error":{"type":"invalid_request_error","message":"max_tokens: required"}}`, ai.FailureRefused},

		// The reason the type is asked BEFORE the status. A gateway between
		// the caller and the provider can rewrite a status; it does not
		// rewrite the provider's own error type in the body. Classifying by
		// status alone would send a user whose account cannot use a model to
		// go and check a key that is working, and would have a caller retry a
		// throttle immediately as though it were a bad request.
		{"permission through a rewritten status", 400,
			`{"error":{"type":"permission_error","message":"not permitted"}}`, ai.FailureAuth},
		{"throttle through a rewritten status", 500,
			`{"error":{"type":"rate_limit_error","message":"slow down"}}`, ai.FailureThrottled},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr := &recordedTransport{responses: []*http.Response{refused(c.status, c.body)}}
			_, err := ask(t, newPort(t, tr))
			if err == nil {
				t.Fatal("expected a failure")
			}
			if got := failureOf(t, err); got != c.want {
				t.Fatalf("%d %s classified as %q, want %q", c.status, c.name, got, c.want)
			}
		})
	}
}

// TestAPromptThatDidNotFitIsReportedAsOverflow.
//
// The wording is pi's recorded one (packages/ai/src/utils/overflow.ts:11 at the
// pin). The two numbers are compared rather than the phrase matched, so a
// reworded message that still carries them keeps working.
func TestAPromptThatDidNotFitIsReportedAsOverflow(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(400,
		`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 213462 tokens > 200000 maximum"}}`)}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("an oversized prompt reported success")
	}
	if !errors.Is(err, ai.ErrContextOverflow) {
		t.Fatalf("an oversized prompt was not reported as an overflow: %v", err)
	}
}

// TestARequestTooLargeInBytesIsAlsoAnOverflow.
//
// A separate condition from the token one: this limit is on the SIZE of the
// request, so the provider never counted any tokens and a number comparison
// cannot see it. Recorded by pi at overflow.ts:12. Both are recovered the same
// way, by sending less, so both have to reach the same recovery.
func TestARequestTooLargeInBytesIsAlsoAnOverflow(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(413,
		`{"error":{"type":"request_too_large","message":"Request exceeds the maximum size"}}`)}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("an oversized request reported success")
	}
	if !errors.Is(err, ai.ErrContextOverflow) {
		t.Fatalf("a request refused for its size was not reported as an overflow: %v", err)
	}
}

// TestAMessageMentioningTokensIsNotAnOverflow. A false positive spends a second
// billed request shortening a conversation that was never too long.
func TestAMessageMentioningTokensIsNotAnOverflow(t *testing.T) {
	for name, body := range map[string]string{
		"a limit that was not exceeded": `{"error":{"type":"invalid_request_error","message":"prompt is too long: 100 tokens > 200000 maximum"}}`,
		"an unrelated complaint":        `{"error":{"type":"invalid_request_error","message":"max_tokens: 5000 tokens is above the model maximum"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			tr := &recordedTransport{responses: []*http.Response{refused(400, body)}}
			_, err := ask(t, newPort(t, tr))
			if err == nil {
				t.Fatal("expected a failure")
			}
			if errors.Is(err, ai.ErrContextOverflow) {
				t.Fatalf("%s was reported as an overflow: %v", name, err)
			}
		})
	}
}

// TestServingOnlyTheConfiguredModel: a request naming another model is refused
// before anything is sent, because a bill for a model nobody asked for is worse
// than an error.
func TestServingOnlyTheConfiguredModel(t *testing.T) {
	tr := &recordedTransport{}
	p := newPort(t, tr)
	_, err := p.Generate(context.Background(), ai.Request{
		Model:    "claude-something-else",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hello"}},
	})
	if err == nil {
		t.Fatal("a request naming another model was served")
	}
	if tr.requests != 0 {
		t.Fatalf("a refused request still reached the provider %d times", tr.requests)
	}
}

// TestAConfigurationThatCouldNotWorkIsRefusedAtConstruction, rather than once a
// user is waiting for a reply.
func TestAConfigurationThatCouldNotWorkIsRefusedAtConstruction(t *testing.T) {
	tr := &recordedTransport{}
	for name, cfg := range map[string]claude.Config{
		"no model":        {Transport: tr, Credential: fixedKey, MaxOutputTokens: 8},
		"no transport":    {Model: "m", Credential: fixedKey, MaxOutputTokens: 8},
		"no credential":   {Model: "m", Transport: tr, MaxOutputTokens: 8},
		"no output cap":   {Model: "m", Transport: tr, Credential: fixedKey},
		"negative window": {Model: "m", Transport: tr, Credential: fixedKey, MaxOutputTokens: 8, ContextWindow: -1},
	} {
		if _, err := claude.New(cfg); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// TestTheCredentialNeverReachesAFailure. A provider that echoes the request
// would otherwise put the key into an error a caller then logs.
func TestTheCredentialNeverReachesAFailure(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(400,
		`{"error":{"type":"invalid_request_error","message":"we received sk-ant-test-key and disliked it"}}`)}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(err.Error(), "sk-ant-test-key") {
		t.Fatalf("the credential survived into a failure: %v", err)
	}
}

// TestAnUnreadableRefusalStillClassifies. A gateway between the caller and the
// provider can answer with HTML; the status is still a fact.
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

// TestOneCallSendsOneRequest.
//
// The invariant every port here is held to, and it is worth checking on this
// one specifically: the vendor SDK underneath retries by default, and a retry
// nobody asked for is a second bill for one call. Counted at the transport,
// which is where a request either left the machine or did not.
func TestOneCallSendsOneRequest(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(500,
		`{"error":{"type":"api_error","message":"Internal server error"}}`)}}

	if _, err := ask(t, newPort(t, tr)); err == nil {
		t.Fatal("expected a failure")
	}
	if tr.requests != 1 {
		t.Fatalf("one call sent %d requests", tr.requests)
	}
}

// TestTheProvidersOwnRetryInstructionSurvivesBeingSuppressed.
//
// Stopping the SDK from retrying means answering its question before the
// provider's answer can be read. If the answer were simply overwritten, a
// caller deciding whether to try again would lose the one opinion that came
// from the provider — so it is moved, not discarded.
func TestTheProvidersOwnRetryInstructionSurvivesBeingSuppressed(t *testing.T) {
	body := `{"error":{"type":"api_error","message":"transient"}}`

	said := refused(500, body)
	said.Header.Set("x-should-retry", "true")
	advised := &recordedTransport{responses: []*http.Response{said}}

	_, err := ask(t, newPort(t, advised))
	if err == nil {
		t.Fatal("expected a failure")
	}
	if advised.requests != 1 {
		t.Fatalf("the provider said retry and the SDK sent %d requests", advised.requests)
	}
	if !ai.Retryable(err) {
		t.Fatalf("the provider's own instruction to retry did not reach the caller: %v", err)
	}

	// And the other direction: an instruction NOT to retry must not be turned
	// into permission by the same mechanism.
	refusedTwice := refused(500, body)
	refusedTwice.Header.Set("x-should-retry", "false")
	forbidden := &recordedTransport{responses: []*http.Response{refusedTwice}}

	_, err = ask(t, newPort(t, forbidden))
	if err == nil {
		t.Fatal("expected a failure")
	}
	if ai.Retryable(err) {
		t.Fatalf("a provider that said not to retry was reported as retryable: %v", err)
	}
}
