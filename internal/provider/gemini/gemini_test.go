package gemini_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/gemini"
)

// recordedTransport replays a recorded exchange and counts what was sent.
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

var fixedKey = ai.StoredCredential("gemini-test-key", "a test")

func refused(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newPort(t *testing.T, tr http.RoundTripper) *gemini.Port {
	t.Helper()
	p, err := gemini.New(gemini.Config{
		Model: "gemini-test", Transport: tr,
		Credential: fixedKey, MaxOutputTokens: 64,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func ask(t *testing.T, p *gemini.Port) (ai.Response, error) {
	t.Helper()
	return p.Generate(context.Background(), ai.Request{
		Model:    "gemini-test",
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

// body is this provider's error envelope: the status code repeated as a field,
// a message, and Google's own canonical status name.
func body(code int, status, message string) string {
	return fmt.Sprintf(`{"error":{"code":%d,"message":%q,"status":%q}}`, code, message, status)
}

// TestTheCanonicalStatusNamesThisProviderSends.
//
// These are google.rpc.Code names, shared across every Google API rather than
// invented for this one. They are read before the HTTP status because they are
// more precise, and because a gateway can rewrite a status while leaving the
// body alone.
func TestTheCanonicalStatusNamesThisProviderSends(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		code   string
		want   ai.Failure
	}{
		{"unauthenticated", 401, "UNAUTHENTICATED", ai.FailureAuth},
		{"permission denied", 403, "PERMISSION_DENIED", ai.FailureAuth},
		{"resource exhausted", 429, "RESOURCE_EXHAUSTED", ai.FailureThrottled},
		{"unavailable", 503, "UNAVAILABLE", ai.FailureTransient},
		{"internal", 500, "INTERNAL", ai.FailureTransient},
		{"invalid argument", 400, "INVALID_ARGUMENT", ai.FailureRefused},
		{"not found", 404, "NOT_FOUND", ai.FailureRefused},

		// The reason the name is read first. A proxy in front of the provider
		// can rewrite the status; it does not rewrite the body. Classified by
		// status alone, a throttle arriving as a 400 would be reported as a
		// request to go and fix.
		{"throttle through a rewritten status", 400, "RESOURCE_EXHAUSTED", ai.FailureThrottled},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr := &recordedTransport{responses: []*http.Response{
				refused(c.status, body(c.status, c.code, "nope"))}}
			_, err := ask(t, newPort(t, tr))
			if err == nil {
				t.Fatal("expected a failure")
			}
			if got := failureOf(t, err); got != c.want {
				t.Fatalf("%s classified as %q, want %q", c.code, got, c.want)
			}
		})
	}
}

// TestAPromptThatDidNotFitIsReportedAsOverflow.
//
// The wording is pi's recorded one (packages/ai/src/utils/overflow.ts:16 at the
// pin). The two numbers are compared rather than the phrase matched.
func TestAPromptThatDidNotFitIsReportedAsOverflow(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(400,
		body(400, "INVALID_ARGUMENT",
			"The input token count (1196265) exceeds the maximum number of tokens allowed (1048575)"))}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("an oversized prompt reported success")
	}
	if !errors.Is(err, ai.ErrContextOverflow) {
		t.Fatalf("an oversized prompt was not reported as an overflow: %v", err)
	}
}

// TestAMessageMentioningTokensIsNotAnOverflow. A false positive spends a second
// billed request shortening a conversation that was never too long.
func TestAMessageMentioningTokensIsNotAnOverflow(t *testing.T) {
	for name, message := range map[string]string{
		"a count within the maximum": "The input token count (10) exceeds the maximum number of tokens allowed (1048575)",
		"an unrelated complaint":     "maxOutputTokens must be a positive number of tokens",
	} {
		t.Run(name, func(t *testing.T) {
			tr := &recordedTransport{responses: []*http.Response{
				refused(400, body(400, "INVALID_ARGUMENT", message))}}
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

// TestOneCallSendsOneRequest, counted at the transport.
func TestOneCallSendsOneRequest(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{
		refused(500, body(500, "INTERNAL", "boom"))}}
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
		Model:    "gemini-something-else",
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
	for name, cfg := range map[string]gemini.Config{
		"no model":        {Transport: tr, Credential: fixedKey, MaxOutputTokens: 8},
		"no transport":    {Model: "m", Credential: fixedKey, MaxOutputTokens: 8},
		"no credential":   {Model: "m", Transport: tr, MaxOutputTokens: 8},
		"no output cap":   {Model: "m", Transport: tr, Credential: fixedKey},
		"negative window": {Model: "m", Transport: tr, Credential: fixedKey, MaxOutputTokens: 8, ContextWindow: -1},
	} {
		if _, err := gemini.New(cfg); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// TestTheCredentialNeverReachesAFailure.
func TestTheCredentialNeverReachesAFailure(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(400,
		body(400, "INVALID_ARGUMENT", "we received gemini-test-key and disliked it"))}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(err.Error(), "gemini-test-key") {
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

// TestTheBackendIsNotChosenByTheEnvironment.
//
// Left to itself the SDK reads GOOGLE_GENAI_USE_VERTEXAI and switches to Vertex
// AI — a different service, billed to a Google Cloud project rather than to the
// key this port resolved — because of a variable this port never saw.
//
// Asserted at the URL, because that is where the difference actually shows.
// Measured with the pin removed, the same call goes to
// aiplatform.googleapis.com instead: it does not fail, it silently reaches
// somewhere else. A provider is chosen by asking for it.
func TestTheBackendIsNotChosenByTheEnvironment(t *testing.T) {
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "1")

	tr := &recordedTransport{responses: []*http.Response{
		refused(400, body(400, "INVALID_ARGUMENT", "nope"))}}
	if _, err := ask(t, newPort(t, tr)); err == nil {
		t.Fatal("expected a failure")
	}
	if len(tr.urls) != 1 {
		t.Fatalf("one call sent %d requests: %v", len(tr.urls), tr.urls)
	}
	if got := tr.urls[0]; !strings.Contains(got, "generativelanguage.googleapis.com") {
		t.Fatalf("an environment variable redirected the call to %s", got)
	}
}
