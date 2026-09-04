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
	"github.com/iamclancyliang/pi-go/internal/provider/qwen"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// The Qwen port had no live test at all until this file existed.
//
// Same position the OpenAI port was in, and the same reason it matters: the
// parity matrix records this row as `compatible`, which is a stronger claim
// than the ports labelled unverified-against-provider make, while nothing in
// the tree could reach the provider to support it. A recorded wire proves the
// port reads what was recorded; only a real call proves the provider sends it.
//
// The gate is separate from the credential: a key existing is not permission
// to spend it.
const qwenGate = "PI_GO_LIVE_QWEN"

// qwenModel is what a live run spends on — the CLI's default for this
// provider, overridable because which models an account may reach is the
// operator's fact rather than this repository's.
func qwenModel() string {
	if model := strings.TrimSpace(os.Getenv("PI_GO_QWEN_MODEL")); model != "" {
		return model
	}
	return "qwen-max"
}

func TestLiveQwenAnswersAndReportsWhatItSpent(t *testing.T) {
	if os.Getenv(qwenGate) == "" {
		t.Skipf("live provider test is off; set %s=1 with DASHSCOPE_API_KEY to spend a real credential",
			qwenGate)
	}
	if strings.TrimSpace(os.Getenv("DASHSCOPE_API_KEY")) == "" {
		t.Fatal("the live test needs DASHSCOPE_API_KEY")
	}
	model := qwenModel()

	transport := &countingRoundTripper{inner: http.DefaultTransport}
	cred, err := qwen.Resolve(context.Background(), processEnvironment{}, "")
	if err != nil {
		t.Fatalf("resolving the credential: %v", err)
	}
	port, err := qwen.New(qwen.Config{
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
	// Read off the wire on this port: it speaks the chat-completions dialect
	// through a transport this repository holds, so absent and zero are
	// distinguishable and the served model is visible.
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

// TestLiveQwenCallsTheReadToolFromItsDeclaredSchema is the check the offline
// suite structurally cannot make.
//
// Offline tests prove a declared schema reaches the framework; they cannot
// prove the provider receives one it can act on. This repository has already
// been wrong there once, with a tool argument schema that never reached the
// wire while every offline test passed.
func TestLiveQwenCallsTheReadToolFromItsDeclaredSchema(t *testing.T) {
	if os.Getenv(qwenGate) == "" {
		t.Skipf("live local test is off; set %s=1 to spend a real credential", qwenGate)
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

	model := qwenModel()
	cred, err := qwen.Resolve(context.Background(), processEnvironment{}, "")
	if err != nil {
		t.Fatalf("resolving the credential: %v", err)
	}
	transport := &countingRoundTripper{inner: http.DefaultTransport}
	port, err := qwen.New(qwen.Config{
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
