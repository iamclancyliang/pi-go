package conformance

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/ollama"
)

// The Ollama port is the only one here that could ever be verified without
// spending anything: the server runs on this machine and bills nobody.
//
// It is still gated. Nothing is spent, but something is used — loading a model
// takes seconds to minutes and gigabytes of memory, which is not a thing to do
// to someone's laptop because they ran the test suite. The gate is separate
// from "is a server listening" for the same reason a credential is not consent
// to spend it: a server being up is not consent to load a model into it.
const ollamaGate = "PI_GO_LIVE_OLLAMA"

// ollamaModel is what the live test asks for. pi pulls this one for its own
// Ollama tests (packages/ai/test/context-overflow.test.ts:620 at the pin), so
// asking for the same thing keeps the two projects comparable.
const ollamaModel = "gpt-oss:20b"

// reachable reports whether something is listening where Ollama should be.
//
// Dialling rather than asking the API: this is a question about the socket, and
// answering it with a request would confuse "no server" with "server that
// refused", which is exactly the distinction the port has to keep.
func reachable(t *testing.T, base string) bool {
	t.Helper()
	parsed, err := url.Parse(base)
	if err != nil {
		t.Fatalf("the base URL is not a URL: %v", err)
	}
	conn, err := net.DialTimeout("tcp", parsed.Host, 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func TestLiveOllamaAnswersFromThisMachine(t *testing.T) {
	if os.Getenv(ollamaGate) == "" {
		t.Skipf("live local-model test is off; set %s=1 to load a model on this machine", ollamaGate)
	}
	base := strings.TrimSpace(os.Getenv("OLLAMA_HOST"))
	if base == "" {
		base = ollama.DefaultBaseURL
	}
	if !reachable(t, base) {
		t.Skipf("no server is listening at %s; start one with: ollama serve", base)
	}

	model := strings.TrimSpace(os.Getenv("PI_GO_OLLAMA_MODEL"))
	if model == "" {
		model = ollamaModel
	}

	transport := &countingRoundTripper{inner: http.DefaultTransport}
	port, err := ollama.New(ollama.Config{
		Model:           model,
		BaseURL:         base,
		Transport:       transport,
		MaxOutputTokens: 32,
	})
	if err != nil {
		t.Fatalf("configuring the provider: %v", err)
	}

	// Generous: the first call may be loading the model from disk.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	got, err := port.Generate(ctx, ai.Request{
		Model:    model,
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "Reply with the single word: ok"}},
	})
	if err != nil {
		t.Fatalf("the local server did not answer: %v", err)
	}
	if strings.TrimSpace(got.Content) == "" {
		t.Fatalf("the reply carried no text: %+v", got)
	}
	if !got.Usage.Reported {
		// Worth failing rather than tolerating: this port reads usage from the
		// framework's metadata instead of the wire, and that is exactly the
		// path a live run exists to check.
		t.Fatalf("the local server reported no usage at all: %+v", got.Usage)
	}
	if got.Usage.InputTokens <= 0 || got.Usage.OutputTokens <= 0 {
		t.Fatalf("usage is implausible for a call that answered: %+v", got.Usage)
	}

	t.Logf("model=%s content=%q", got.Model, strings.TrimSpace(got.Content))
	t.Logf("usage: input=%d output=%d requests=%d",
		got.Usage.InputTokens, got.Usage.OutputTokens, transport.count())
}
