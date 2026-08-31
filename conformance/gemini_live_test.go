package conformance

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/gemini"
)

// The Gemini port has NOT been verified against the provider.
//
// Its offline tests are written from the vendor SDK's own types and from
// wordings pi recorded at the pin. That is a weaker standard than the one
// verified ports here meet, and the difference is not theoretical: the same
// standard caught a tool schema that never reached the wire and an overflow
// refusal whose real shape was nothing like the assumed one.
//
// The gate is separate from the credential on purpose. A key existing is not
// permission to spend it.
const geminiGate = "PI_GO_LIVE_GEMINI"

func TestLiveGeminiAnswersAndReportsWhatItSpent(t *testing.T) {
	if os.Getenv(geminiGate) == "" {
		t.Skipf("live provider test is off; set %s=1 with GEMINI_API_KEY to spend a real credential",
			geminiGate)
	}
	if strings.TrimSpace(os.Getenv("GEMINI_API_KEY")) == "" {
		t.Fatal("the live test needs GEMINI_API_KEY")
	}
	model := strings.TrimSpace(os.Getenv("PI_GO_GEMINI_MODEL"))
	if model == "" {
		model = "gemini-2.5-pro"
	}

	transport := &countingRoundTripper{inner: http.DefaultTransport}
	cred, err := gemini.Resolve(context.Background(), processEnvironment{}, "")
	if err != nil {
		t.Fatalf("resolving the credential: %v", err)
	}
	port, err := gemini.New(gemini.Config{
		Model:           model,
		Transport:       transport,
		Credential:      cred,
		MaxOutputTokens: 32,
	})
	if err != nil {
		t.Fatalf("configuring the provider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	got, err := port.Generate(ctx, ai.Request{
		Model:    model,
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "Reply with the single word: ok"}},
	})
	if err != nil {
		t.Fatalf("the provider did not answer: %v", err)
	}
	if strings.TrimSpace(got.Content) == "" {
		t.Fatalf("the reply carried no text: %+v", got)
	}
	if !got.Usage.Reported {
		// This port reads usage from the framework's metadata rather than
		// from the wire, which is exactly what a live run exists to check.
		t.Fatalf("the provider reported no usage at all: %+v", got.Usage)
	}
	if got.Usage.InputTokens <= 0 || got.Usage.OutputTokens <= 0 {
		t.Fatalf("usage is implausible for a call that answered: %+v", got.Usage)
	}
	if sent := transport.count(); sent != 1 {
		t.Fatalf("one model call sent %d requests", sent)
	}

	t.Logf("model=%s content=%q", got.Model, strings.TrimSpace(got.Content))
	t.Logf("usage: input=%d output=%d requests=%d",
		got.Usage.InputTokens, got.Usage.OutputTokens, transport.count())
}
