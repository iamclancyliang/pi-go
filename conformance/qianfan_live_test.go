package conformance

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/qianfan"
)

// The Qianfan port has NOT been verified against the provider, and it carries
// one assumption the other unverified ports do not.
//
// It reaches the provider's OpenAI-compatible v2 endpoint through the shared
// chat-completions dialect, rather than through eino-ext's Qianfan component —
// which cannot meet this repository's port contract, for the reasons in #38.
// That the endpoint really speaks this dialect is the premise the whole
// approach rests on, and it is the first thing a live run settles.
//
// The gate is separate from the credential on purpose. A key existing is not
// permission to spend it.
const qianfanGate = "PI_GO_LIVE_QIANFAN"

func TestLiveQianfanAnswersAndReportsWhatItSpent(t *testing.T) {
	if os.Getenv(qianfanGate) == "" {
		t.Skipf("live provider test is off; set %s=1 with QIANFAN_BEARER_TOKEN to spend a real credential",
			qianfanGate)
	}
	if strings.TrimSpace(os.Getenv("QIANFAN_BEARER_TOKEN")) == "" {
		t.Fatal("the live test needs QIANFAN_BEARER_TOKEN")
	}
	model := strings.TrimSpace(os.Getenv("PI_GO_QIANFAN_MODEL"))
	if model == "" {
		model = "ernie-4.5-turbo-128k"
	}

	transport := &countingRoundTripper{inner: http.DefaultTransport}
	cred, err := qianfan.Resolve(context.Background(), processEnvironment{}, "")
	if err != nil {
		t.Fatalf("resolving the credential: %v", err)
	}
	port, err := qianfan.New(qianfan.Config{
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
		// The failure worth reading closely: a stream that ended without a
		// terminal frame would mean the endpoint does not speak this dialect
		// after all, which is the premise rather than a bug in the call.
		t.Fatalf("the provider did not answer: %v", err)
	}
	if strings.TrimSpace(got.Content) == "" {
		t.Fatalf("the reply carried no text: %+v", got)
	}
	// Read off the wire on this port, unlike the ones on vendor SDKs.
	if !got.Usage.Reported {
		t.Fatalf("the provider reported no usage at all: %+v", got.Usage)
	}
	if got.Usage.InputTokens <= 0 || got.Usage.OutputTokens <= 0 {
		t.Fatalf("usage is implausible for a call that answered: %+v", got.Usage)
	}
	if got.Model == "" {
		t.Fatalf("the reply did not say which model answered: %+v", got)
	}
	if sent := transport.count(); sent != 1 {
		t.Fatalf("one model call sent %d requests", sent)
	}

	t.Logf("model=%s content=%q", got.Model, strings.TrimSpace(got.Content))
	t.Logf("usage: input=%d output=%d requests=%d",
		got.Usage.InputTokens, got.Usage.OutputTokens, transport.count())
}
