package conformance

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/qianfan"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
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

// TestLiveQianfanCallsTheReadToolFromItsDeclaredSchema is the check the offline
// suite structurally cannot make.
//
// Offline tests prove a declared schema reaches the framework. What they cannot
// prove is that the provider receives one it can act on — which is exactly
// where this repository was wrong before, with a tool argument schema that
// never reached the wire and offline tests passing the whole time.
//
// It matters more on this port than on most: this one reaches an endpoint
// through a dialect rather than through the vendor's own component, so "tools
// work here the way they work everywhere else" is an assumption rather than
// something the vendor promised.
func TestLiveQianfanCallsTheReadToolFromItsDeclaredSchema(t *testing.T) {
	if os.Getenv(qianfanGate) == "" {
		t.Skipf("live local test is off; set %s=1 to spend a real credential", qianfanGate)
	}
	dir := t.TempDir()
	// A token the model cannot produce from the prompt alone, so quoting it
	// back is evidence the file was read rather than guessed at.
	const token = "quokka-77341-halibut"
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"),
		[]byte("project notes\nsecret marker: "+token+"\nend\n"), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	reader := &recordingTool{inner: &tools.Read{Root: dir}}
	registry := tools.NewRegistry()
	if err := registry.Register(reader); err != nil {
		t.Fatalf("registering read: %v", err)
	}

	model := strings.TrimSpace(os.Getenv("PI_GO_QIANFAN_MODEL"))
	if model == "" {
		model = "ernie-4.5-turbo-128k"
	}
	cred, err := qianfan.Resolve(context.Background(), processEnvironment{}, "")
	if err != nil {
		t.Fatalf("resolving the credential: %v", err)
	}
	transport := &countingRoundTripper{inner: http.DefaultTransport}
	port, err := qianfan.New(qianfan.Config{
		Model: model, Transport: transport, Credential: cred, MaxOutputTokens: 256,
	})
	if err != nil {
		t.Fatalf("configuring the provider: %v", err)
	}

	sess := session.New("You are pi-go. Use the tools available to you.")
	agent, err := runtime.New(runtime.Config{
		Model:     port,
		ModelName: model,
		Tools:     registry,
		Session:   sess,
		Policy:    runtime.DenyWrites,
	})
	if err != nil {
		t.Fatalf("building the agent: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := agent.Run(ctx, "Read the file notes.txt and tell me the secret marker."); err != nil {
		t.Fatalf("the run failed: %v", err)
	}

	calls := reader.calls()
	if len(calls) == 0 {
		t.Fatal("the model never called read, so either the tool or its schema did not reach it")
	}
	for _, args := range calls {
		// An argument this repository can execute, not merely one the model was
		// willing to emit: a payload shaped from an invented schema is what
		// this test exists to catch.
		if !strings.Contains(args, `"path"`) {
			t.Fatalf("read was called without the declared path argument: %s", args)
		}
	}

	var answer string
	for _, m := range sess.Snapshot().Messages {
		if m.Role == ai.RoleAssistant && strings.TrimSpace(m.Content) != "" {
			answer = m.Content
		}
	}
	if !strings.Contains(answer, token) {
		t.Fatalf("the answer does not carry what the file held.\ncalls: %v\nanswer: %q", calls, answer)
	}

	t.Logf("read called %d time(s): %v", len(calls), calls)
	t.Logf("answer: %s", strings.TrimSpace(answer))
	t.Logf("requests sent: %d, usage: %+v", transport.count(), sess.Usage())
}
