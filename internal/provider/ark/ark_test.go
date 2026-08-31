package ark_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/ark"
)

// recordedTransport replays a recorded exchange and counts what was sent.
type recordedTransport struct {
	requests  int
	responses []*http.Response
}

func (r *recordedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.requests++
	if r.requests > len(r.responses) {
		return nil, io.ErrUnexpectedEOF
	}
	return r.responses[r.requests-1], nil
}

var fixedKey = ai.StoredCredential("ark-test-key", "a test")

func refused(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newPort(t *testing.T, tr http.RoundTripper) *ark.Port {
	t.Helper()
	p, err := ark.New(ark.Config{
		Model: "ep-test-endpoint", Transport: tr,
		Credential: fixedKey, MaxOutputTokens: 64,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func ask(t *testing.T, p *ark.Port) (ai.Response, error) {
	t.Helper()
	return p.Generate(context.Background(), ai.Request{
		Model:    "ep-test-endpoint",
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

// TestTheStatusesThisPortClassifiesOn.
//
// The status is all it has: this provider sends its own error codes and no
// source available here records what they mean, so a mapping for them would be
// a guess wearing the shape of knowledge. What the status CAN settle is settled.
func TestTheStatusesThisPortClassifiesOn(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		want   ai.Failure
	}{
		{"unauthorized", 401, ai.FailureAuth},
		{"forbidden", 403, ai.FailureAuth},
		{"payment required", 402, ai.FailureQuota},
		{"rate limited", 429, ai.FailureThrottled},
		{"server fault", 500, ai.FailureTransient},
		{"gateway", 503, ai.FailureTransient},
		{"bad request", 400, ai.FailureRefused},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr := &recordedTransport{responses: []*http.Response{
				refused(c.status, `{"error":{"code":"SomeCode","message":"nope","type":"BadRequest"}}`)}}
			_, err := ask(t, newPort(t, tr))
			if err == nil {
				t.Fatal("expected a failure")
			}
			if got := failureOf(t, err); got != c.want {
				t.Fatalf("%d classified as %q, want %q", c.status, got, c.want)
			}
		})
	}
}

// TestWhatThisPortCannotClassifyItStillCarries.
//
// The code and the request id are the two things a person can act on when this
// port cannot: one names the condition in the provider's own vocabulary, the
// other is what their support asks for. Dropping them because nothing here
// branches on them would take the judgement away from the reader too.
func TestWhatThisPortCannotClassifyItStillCarries(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(400,
		`{"error":{"code":"SensitiveContentDetected","message":"the request was refused","type":"BadRequest","request_id":"021700000000000000000000000000000000000000000abcdef"}}`)}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("expected a failure")
	}
	for _, want := range []string{"SensitiveContentDetected", "021700000000000000000000000000000000000000000abcdef"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the failure does not carry %q: %v", want, err)
		}
	}
}

// TestOneCallSendsOneRequest.
//
// The vendor SDK retries twice by default, on 429 and every 5xx, so this is
// checked against a 500 — the case where a default retry would show. A retry
// nobody asked for is a second bill for one call, and the failure that finally
// surfaces hides the attempts before it.
func TestOneCallSendsOneRequest(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(500,
		`{"error":{"code":"InternalServiceError","message":"boom","type":"ServerError"}}`)}}

	if _, err := ask(t, newPort(t, tr)); err == nil {
		t.Fatal("expected a failure")
	}
	if tr.requests != 1 {
		t.Fatalf("one call sent %d requests", tr.requests)
	}
}

// TestServingOnlyTheConfiguredModel. An endpoint id is billed to whoever
// created it, so serving one nobody named is worse here than elsewhere.
func TestServingOnlyTheConfiguredModel(t *testing.T) {
	tr := &recordedTransport{}
	p := newPort(t, tr)
	_, err := p.Generate(context.Background(), ai.Request{
		Model:    "ep-some-other-endpoint",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hello"}},
	})
	if err == nil {
		t.Fatal("a request naming another endpoint was served")
	}
	if tr.requests != 0 {
		t.Fatalf("a refused request still reached the provider %d times", tr.requests)
	}
}

// TestAConfigurationThatCouldNotWorkIsRefusedAtConstruction.
func TestAConfigurationThatCouldNotWorkIsRefusedAtConstruction(t *testing.T) {
	tr := &recordedTransport{}
	for name, cfg := range map[string]ark.Config{
		"no model":        {Transport: tr, Credential: fixedKey, MaxOutputTokens: 8},
		"no transport":    {Model: "m", Credential: fixedKey, MaxOutputTokens: 8},
		"no credential":   {Model: "m", Transport: tr, MaxOutputTokens: 8},
		"no output cap":   {Model: "m", Transport: tr, Credential: fixedKey},
		"negative window": {Model: "m", Transport: tr, Credential: fixedKey, MaxOutputTokens: 8, ContextWindow: -1},
	} {
		if _, err := ark.New(cfg); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// TestTheCredentialNeverReachesAFailure.
func TestTheCredentialNeverReachesAFailure(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(400,
		`{"error":{"code":"InvalidParameter","message":"we received ark-test-key and disliked it"}}`)}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(err.Error(), "ark-test-key") {
		t.Fatalf("the credential survived into a failure: %v", err)
	}
}

// TestAnUnreadableRefusalStillClassifies. A gateway in front of the provider
// can answer with HTML; the status is still a fact.
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
