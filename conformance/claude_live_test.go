package conformance

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/claude"
)

// The Claude port has NOT been verified against the provider.
//
// Its offline tests are written from Anthropic's documented error types and
// from wordings pi recorded at the pin. That is a weaker standard than the one
// every verified port here meets, and the difference is not theoretical: the
// same standard caught a tool schema that never reached the wire and an
// overflow refusal whose real shape was nothing like the assumed one.
//
// The gate is separate from the credential on purpose. A key existing is not
// permission to spend it.
const claudeGate = "PI_GO_LIVE_CLAUDE"

func TestLiveClaudeAnswersAndReportsWhatItSpent(t *testing.T) {
	if os.Getenv(claudeGate) == "" {
		t.Skipf("live provider test is off; set %s=1 with ANTHROPIC_API_KEY to spend a real credential",
			claudeGate)
	}
	key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if key == "" {
		t.Fatal("the live test needs ANTHROPIC_API_KEY")
	}

	model := strings.TrimSpace(os.Getenv("PI_GO_CLAUDE_MODEL"))
	if model == "" {
		model = "claude-sonnet-4-5-20250929"
	}

	transport := &countingRoundTripper{inner: http.DefaultTransport}
	cred, err := claude.Resolve(context.Background(), processEnvironment{}, "")
	if err != nil {
		t.Fatalf("resolving the credential: %v", err)
	}
	port, err := claude.New(claude.Config{
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
		t.Fatalf("the provider reported no usage at all: %+v", got.Usage)
	}
	if got.Usage.InputTokens <= 0 || got.Usage.OutputTokens <= 0 {
		t.Fatalf("usage is implausible for a call that answered: %+v", got.Usage)
	}
	// The one thing this port does that no other one has to: it suppresses the
	// vendor SDK's own retries. A live run is where that is worth counting,
	// because the SDK's default only shows itself against a real endpoint.
	if sent := transport.count(); sent != 1 {
		t.Fatalf("one model call sent %d requests", sent)
	}

	t.Logf("model=%s content=%q", got.Model, strings.TrimSpace(got.Content))
	t.Logf("usage: input=%d output=%d requests=%d",
		got.Usage.InputTokens, got.Usage.OutputTokens, transport.count())
}
