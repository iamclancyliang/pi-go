package conformance

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/openrouter"
)

// The OpenRouter port has NOT been verified against the provider.
//
// The DeepSeek port was, and that standard is what caught a tool schema never
// reaching the wire and the real shape of a context-overflow refusal — both
// cases where the offline tests were passing against assumptions that were
// wrong. (The claim this comment first made, that every other port had been
// verified, was not true: OpenAI and Qwen had no live test at all until
// 2026-09-02.) This one has no credential to run against, so its wire semantics
// are this repository's reading of OpenRouter's documentation and nothing
// more.
//
// The test below is written so that becomes false the moment a key exists.
// Until then the parity matrix records the port as unverified-against-provider,
// which is a weaker claim than a verified port makes.
const openRouterGate = "PI_GO_LIVE_OPENROUTER"

func TestLiveOpenRouterAnswersAndReportsWhatItSpent(t *testing.T) {
	if os.Getenv(openRouterGate) == "" {
		t.Skipf("live provider test is off; set %s=1 with OPENROUTER_API_KEY to spend a real credential",
			openRouterGate)
	}
	key := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if key == "" {
		t.Fatal("the live test needs OPENROUTER_API_KEY")
	}

	model := strings.TrimSpace(os.Getenv("PI_GO_OPENROUTER_MODEL"))
	if model == "" {
		// Named rather than defaulted silently: an aggregator bills by the
		// model behind it, so which one runs is the operator's choice.
		t.Skip("set PI_GO_OPENROUTER_MODEL to the vendor/model to spend on")
	}

	transport := &countingRoundTripper{inner: http.DefaultTransport}
	cred, err := openrouter.Resolve(context.Background(), processEnvironment{}, "")
	if err != nil {
		t.Fatalf("resolving the credential: %v", err)
	}
	port, err := openrouter.New(openrouter.Config{
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
	// The same three things every other port is held to.
	if !got.Usage.Reported {
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
