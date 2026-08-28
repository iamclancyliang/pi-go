package conformance

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/deepseek"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// These are the only tests in this repository that reach a network, spend a
// person's money and depend on a third party being up. Everything about them is
// therefore deliberate rather than convenient:
//
//   - They are off unless PI_GO_LIVE_DEEPSEEK is set, so `go test ./...` and CI
//     never reach them. The gate is a separate variable from the credential:
//     having a key configured is not consent to spend it.
//   - The credential enters only through the injected environment seam, never
//     through a literal, a file or a flag. Nothing here prints it, and the
//     types that hold it refuse to format it.
//   - The retry budget is zero, so one model call is one billed request, and a
//     counting transport checks that rather than trusting it.
//   - Prompts and output caps are the smallest that still prove the behaviour.
//   - A failure is reported, not retried. Rerunning a live failure in a loop is
//     how a smoke test becomes a bill.
const liveGate = "PI_GO_LIVE_DEEPSEEK"

func liveOrSkip(t *testing.T) {
	t.Helper()
	if os.Getenv(liveGate) == "" {
		t.Skipf("live provider test is off; set %s=1 to spend a real credential", liveGate)
	}
}

// processEnvironment is the injected environment seam backed by this process.
//
// It exists here rather than in the product because nothing in the product has
// needed one yet: the composition root that reads a real environment is the CLI
// this repository has not built.
type processEnvironment struct{}

func (processEnvironment) Lookup(_ context.Context, name string) (string, error) {
	return os.Getenv(name), nil
}

// countingTransport is the evidence for the one-request-per-call claim. The
// claim is about what leaves the machine, so it is counted where that happens.
type countingTransport struct {
	inner http.Client
	mu    sync.Mutex
	sent  int
}

func (t *countingTransport) Do(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.sent++
	t.mu.Unlock()
	return t.inner.Do(req)
}

func (t *countingTransport) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sent
}

func livePort(t *testing.T, transport *countingTransport, maxOutput int) *deepseek.Port {
	t.Helper()
	port, err := deepseek.New(deepseek.Config{
		Model:       "deepseek-chat",
		Transport:   transport,
		Environment: processEnvironment{},
		// The zero retry policy is the shipped one: one request, no retry.
		MaxOutputTokens: maxOutput,
	})
	if err != nil {
		t.Fatalf("configuring the provider: %v", err)
	}
	return port
}

// TestLiveDeepSeekAnswersAndReportsWhatItSpent is the smallest thing that can
// go wrong end to end: a request is built, sent, accepted, and the reply is
// drained through the shared collector rather than a second parser.
func TestLiveDeepSeekAnswersAndReportsWhatItSpent(t *testing.T) {
	liveOrSkip(t)

	transport := &countingTransport{inner: http.Client{Timeout: 60 * time.Second}}
	port := livePort(t, transport, 32)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	got, err := port.Generate(ctx, ai.Request{
		// Named on the request, not taken from the configuration: this provider
		// invents no default, so a request naming nothing is refused before it
		// could reach whichever model the configuration happened to hold.
		Model:    "deepseek-chat",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "Reply with the single word: ok"}},
	})
	if err != nil {
		t.Fatalf("the provider did not answer: %v", err)
	}
	if strings.TrimSpace(got.Content) == "" {
		t.Fatalf("the reply carried no text: %+v", got)
	}

	// A call that read the request is not free, and this repository refuses to
	// guess a number the provider did not give. Silence here would mean the
	// ledger is recording nothing for a call that was billed.
	if !got.Usage.Reported {
		t.Fatalf("the provider reported no usage at all: %+v", got.Usage)
	}
	if got.Usage.InputTokens <= 0 || got.Usage.OutputTokens <= 0 {
		t.Fatalf("usage is implausible for a call that answered: %+v", got.Usage)
	}
	if sent := transport.count(); sent != 1 {
		t.Fatalf("one model call sent %d requests", sent)
	}

	cacheRead := "not reported"
	if got.Usage.CacheReadTokens != nil {
		cacheRead = strconv.Itoa(*got.Usage.CacheReadTokens)
	}
	t.Logf("model=%s content=%q", got.Model, strings.TrimSpace(got.Content))
	t.Logf("usage: input(uncached)=%d output=%d cache_read=%s requests=%d",
		got.Usage.InputTokens, got.Usage.OutputTokens, cacheRead, transport.count())
}

// recordingTool remembers what the model actually asked for.
type recordingTool struct {
	inner tools.Tool

	mu   sync.Mutex
	args []string
}

func (r *recordingTool) Name() string               { return r.inner.Name() }
func (r *recordingTool) Description() string        { return r.inner.Description() }
func (r *recordingTool) Execution() tools.Execution { return r.inner.Execution() }
func (r *recordingTool) Parameters() *tools.Schema  { return r.inner.Parameters() }

func (r *recordingTool) Call(ctx context.Context, args string) (tools.Result, error) {
	r.mu.Lock()
	r.args = append(r.args, args)
	r.mu.Unlock()
	return r.inner.Call(ctx, args)
}

func (r *recordingTool) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.args...)
}

// TestLiveDeepSeekCallsTheReadToolFromItsDeclaredSchema is the check the
// offline suite structurally cannot make.
//
// Every offline test of the tool seam asserts that a declared schema reaches
// the framework. Whether a real model, given that schema and nothing else, then
// produces a call this repository can execute is a fact about the provider. It
// was unproven until this ran: before the schema existed, a tool with arguments
// was described to the model as taking none, and the model invented a shape.
func TestLiveDeepSeekCallsTheReadToolFromItsDeclaredSchema(t *testing.T) {
	liveOrSkip(t)

	dir := t.TempDir()
	// A token the model cannot produce from the prompt alone, so quoting it
	// back is evidence the file was actually read rather than guessed at.
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

	transport := &countingTransport{inner: http.Client{Timeout: 60 * time.Second}}
	port := livePort(t, transport, 256)
	sess := session.New("You are pi-go. Use the tools available to you.")

	agent, err := runtime.New(runtime.Config{
		Model:     port,
		ModelName: "deepseek-chat",
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
	// The argument has to be one this repository can execute, not merely one
	// the model was willing to emit: a payload shaped from an invented schema
	// is exactly what this test exists to catch.
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
	t.Logf("requests sent: %d", transport.count())
	t.Logf("session usage: %+v", sess.Usage())
}
