package ollama_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/ollama"
)

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

func refused(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newPort(t *testing.T, tr http.RoundTripper) *ollama.Port {
	t.Helper()
	p, err := ollama.New(ollama.Config{Model: "llama-test", Transport: tr, MaxOutputTokens: 64})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func ask(t *testing.T, p *ollama.Port) (ai.Response, error) {
	t.Helper()
	return p.Generate(context.Background(), ai.Request{
		Model:    "llama-test",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hello"}},
	})
}

// TestNoCredentialIsNeeded. Every other provider here refuses to construct
// without one, and that check is right for them; a local server has none, and
// making the requirement optional would lose the check that catches a missing
// key where it matters.
func TestNoCredentialIsNeeded(t *testing.T) {
	if _, err := ollama.New(ollama.Config{
		Model: "llama-test", Transport: &recordedTransport{}, MaxOutputTokens: 8,
	}); err != nil {
		t.Fatalf("a local port refused to build without a credential: %v", err)
	}
}

// TestAModelThatWasNeverPulledSaysHowToFixIt.
//
// The one failure here a user resolves in a single command. Classified as a
// refusal rather than a server fault: retrying pulls nothing, and reading it as
// transient would have a caller wait for a model that will never appear.
func TestAModelThatWasNeverPulledSaysHowToFixIt(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{
		refused(404, `{"error":"model 'llama-test' not found"}`)}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("a missing model reported success")
	}
	got, ok := ai.FailureOf(err)
	if !ok {
		t.Fatalf("the failure carries no classification: %v", err)
	}
	if got != ai.FailureRefused {
		t.Fatalf("a missing model was classified as %q", got)
	}
	if ai.Retryable(err) {
		t.Fatalf("a missing model was reported as worth retrying: %v", err)
	}
	if !strings.Contains(err.Error(), "ollama pull") {
		t.Fatalf("the failure does not say how to fix it: %v", err)
	}
}

// TestALocalServerThatIsNotRunningFailsAsATransport, not as a refusal: nothing
// refused anything, and telling a user their request was rejected sends them to
// look at the request.
func TestALocalServerThatIsNotRunningFailsAsATransport(t *testing.T) {
	tr := &recordedTransport{} // no responses: the connection fails
	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("an unreachable server reported success")
	}
	if got, ok := ai.FailureOf(err); ok && got == ai.FailureRefused {
		t.Fatalf("an unreachable server was reported as a refusal: %v", err)
	}
}

// TestAServerFaultIsWorthRetrying, unlike a missing model.
func TestAServerFaultIsWorthRetrying(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{
		refused(500, `{"error":"internal error"}`)}}
	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("expected a failure")
	}
	got, ok := ai.FailureOf(err)
	if !ok || got != ai.FailureTransient {
		t.Fatalf("a server fault classified as %q (ok=%v)", got, ok)
	}
}

// TestServingOnlyTheConfiguredModel. Loading a model takes seconds to minutes,
// so answering from a different one than the caller configured is expensive as
// well as wrong.
func TestServingOnlyTheConfiguredModel(t *testing.T) {
	tr := &recordedTransport{}
	p := newPort(t, tr)
	_, err := p.Generate(context.Background(), ai.Request{
		Model:    "some-other-model",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hello"}},
	})
	if err == nil {
		t.Fatal("a request naming another model was served")
	}
	if tr.requests != 0 {
		t.Fatalf("a refused request still reached the server %d times", tr.requests)
	}
}

// TestAConfigurationThatCouldNotWorkIsRefusedAtConstruction.
func TestAConfigurationThatCouldNotWorkIsRefusedAtConstruction(t *testing.T) {
	tr := &recordedTransport{}
	for name, cfg := range map[string]ollama.Config{
		"no model":        {Transport: tr, MaxOutputTokens: 8},
		"no transport":    {Model: "m", MaxOutputTokens: 8},
		"no output cap":   {Model: "m", Transport: tr},
		"negative window": {Model: "m", Transport: tr, MaxOutputTokens: 8, ContextWindow: -1},
	} {
		if _, err := ollama.New(cfg); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// TestAPromptThatDidNotFitIsReportedAsOverflow, not as an ordinary refusal.
//
// It is the one failure this repository can recover from — by shortening the
// conversation and asking again — so reading it as a generic 400 gives up on
// the only case worth not giving up on. The message is pi's recorded one
// (packages/ai/test/overflow.test.ts:33 at the pin).
func TestAPromptThatDidNotFitIsReportedAsOverflow(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(400,
		`{"error":"prompt too long; exceeded max context length by 100918 tokens"}`)}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("an oversized prompt reported success")
	}
	if !errors.Is(err, ai.ErrContextOverflow) {
		t.Fatalf("an oversized prompt was not reported as an overflow: %v", err)
	}
}

// TestAnOrdinaryFaultIsNotMistakenForOverflow. A detector that fires on any
// failure would have a caller shorten a conversation that was never too long,
// paying for a second request to fix a problem it does not have. pi's own
// negative case: packages/ai/test/overflow.test.ts:78.
func TestAnOrdinaryFaultIsNotMistakenForOverflow(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(500,
		`{"error":"model runner crashed unexpectedly"}`)}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("expected a failure")
	}
	if errors.Is(err, ai.ErrContextOverflow) {
		t.Fatalf("a crashed runner was reported as an overflow: %v", err)
	}
}
