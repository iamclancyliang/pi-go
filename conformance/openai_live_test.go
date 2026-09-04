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
	"github.com/iamclancyliang/pi-go/internal/provider/openai"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// The OpenAI port had no live test at all until this file existed.
//
// That is a weaker position than the ports the parity matrix labels
// unverified-against-provider: those name what they have not checked and carry
// a written test that runs the moment a credential appears. This one was
// recorded as `compatible` while nothing in the tree could ever reach the
// provider — the offline suite proves the port satisfies its interface and
// survives a recorded wire, not that the real endpoint agrees with it.
//
// The two checks below are the ones every other port is held to, so the row's
// evidence can be as strong as its claim. Both spend money and are off unless
// the gate is set; the gate is separate from the credential because a key
// existing is not permission to spend it.
const openAIGate = "PI_GO_LIVE_OPENAI"

// openAIModel is what a live run spends on. The CLI's default for this
// provider, overridable because which model an account may reach is the
// operator's fact, not this repository's.
func openAIModel() string {
	if model := strings.TrimSpace(os.Getenv("PI_GO_OPENAI_MODEL")); model != "" {
		return model
	}
	return "gpt-5"
}

func TestLiveOpenAIAnswersAndReportsWhatItSpent(t *testing.T) {
	if os.Getenv(openAIGate) == "" {
		t.Skipf("live provider test is off; set %s=1 with OPENAI_API_KEY to spend a real credential",
			openAIGate)
	}
	if strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) == "" {
		t.Fatal("the live test needs OPENAI_API_KEY")
	}
	model := openAIModel()

	transport := &countingRoundTripper{inner: http.DefaultTransport}
	cred, err := openai.Resolve(context.Background(), processEnvironment{}, "")
	if err != nil {
		t.Fatalf("resolving the credential: %v", err)
	}
	port, err := openai.New(openai.Config{
		Model:           model,
		Transport:       transport,
		Credential:      cred,
		MaxOutputTokens: 512,
	})
	if err != nil {
		t.Fatalf("configuring the provider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
	// This port reads its provider's own bytes, so a reply that does not name
	// the model behind it is a gap in the port rather than a fact it cannot
	// have. A substitution nobody can see is a bill nobody can attribute.
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

// TestLiveOpenAICallsTheReadToolFromItsDeclaredSchema is the check the offline
// suite structurally cannot make.
//
// Offline tests prove a declared schema reaches the framework. What they cannot
// prove is that the provider receives one it can act on — which is exactly
// where this repository was wrong before, with a tool argument schema that
// never reached the wire while the offline tests passed throughout.
//
// This port reaches the Responses API rather than the chat-completions dialect
// the other ports share, so its tool payloads are built by a different path and
// are not covered by any other live run.
func TestLiveOpenAICallsTheReadToolFromItsDeclaredSchema(t *testing.T) {
	if os.Getenv(openAIGate) == "" {
		t.Skipf("live local test is off; set %s=1 to spend a real credential", openAIGate)
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

	model := openAIModel()
	cred, err := openai.Resolve(context.Background(), processEnvironment{}, "")
	if err != nil {
		t.Fatalf("resolving the credential: %v", err)
	}
	transport := &countingRoundTripper{inner: http.DefaultTransport}
	port, err := openai.New(openai.Config{
		Model: model, Transport: transport, Credential: cred, MaxOutputTokens: 1024,
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

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	if err := agent.Run(ctx, "Read the file notes.txt and tell me the secret marker."); err != nil {
		t.Fatalf("the run failed: %v", err)
	}

	calls := reader.calls()
	if len(calls) == 0 {
		t.Fatal("the model never called read, so either the tool or its schema did not reach it")
	}
	for _, args := range calls {
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
