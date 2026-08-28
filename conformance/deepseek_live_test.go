package conformance

import (
	"bytes"
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
	"github.com/iamclancyliang/pi-go/internal/cli"
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

// TestLiveDeepSeekUsesTheBuiltInSetToChangeAFile is the end the whole tool set
// exists for, and it cannot be established offline.
//
// Offline tests prove each tool does what it says and that its schema reaches
// the framework. What they cannot prove is that a real model, given these seven
// declarations and nothing else, chooses among them and produces calls this
// repository can execute — which is exactly where the argument schema was found
// to be dropped. The task needs more than one tool, so a model that can only
// read still fails it.
func TestLiveDeepSeekUsesTheBuiltInSetToChangeAFile(t *testing.T) {
	liveOrSkip(t)

	dir := t.TempDir()
	const before = "package main\n\nfunc greet() string {\n\treturn \"hello\"\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "greet.go"), []byte(before), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	built, err := tools.NewBuiltInRegistry(dir)
	if err != nil {
		t.Fatalf("building the tool set: %v", err)
	}
	registry, used := recordUses(t, built)

	transport := &countingTransport{inner: http.Client{Timeout: 60 * time.Second}}
	port := livePort(t, transport, 1024)
	sess := session.New("You are pi-go, a coding agent. Use the tools available to you.")

	agent, err := runtime.New(runtime.Config{
		Model:     port,
		ModelName: "deepseek-chat",
		Tools:     registry,
		Session:   sess,
		// AllowAll rather than DenyWrites: this task changes a file, and the
		// point is to watch the mutating tools work.
		Policy: runtime.AllowAll,
	})
	if err != nil {
		t.Fatalf("building the agent: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	err = agent.Run(ctx, "In greet.go, change the returned string from \"hello\" to \"goodbye\". "+
		"Find the file first, then make the change.")
	if err != nil {
		t.Fatalf("the run failed: %v", err)
	}

	// The file is the evidence. What the model said it did is not.
	raw, err := os.ReadFile(filepath.Join(dir, "greet.go"))
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, `"goodbye"`) {
		t.Fatalf("the change was not made:\n%s\ntools used: %v", got, used())
	}
	if strings.Contains(got, `"hello"`) {
		t.Fatalf("the old string is still there:\n%s", got)
	}
	if !strings.Contains(got, "func greet() string {") {
		t.Fatalf("the rest of the file did not survive:\n%s", got)
	}

	names := used()
	if len(names) < 2 {
		t.Fatalf("the task needed more than one tool; the model used %v", names)
	}
	t.Logf("tools used, in order: %v", names)
	t.Logf("requests sent: %d", transport.count())
	t.Logf("session usage: %+v", sess.Usage())
}

// recordUses wraps every registered tool so the test can report which ones the
// model reached for, without changing what any of them does.
func recordUses(t *testing.T, r *tools.Registry) (*tools.Registry, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var order []string

	wrapped := tools.NewRegistry()
	for _, inner := range r.All() {
		rec := &usageRecorder{inner: inner, note: func(name string) {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
		}}
		if err := wrapped.Register(rec); err != nil {
			t.Fatalf("wrapping %s: %v", inner.Name(), err)
		}
	}
	return wrapped, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), order...)
	}
}

type usageRecorder struct {
	inner tools.Tool
	note  func(string)
}

func (u *usageRecorder) Name() string               { return u.inner.Name() }
func (u *usageRecorder) Description() string        { return u.inner.Description() }
func (u *usageRecorder) Execution() tools.Execution { return u.inner.Execution() }
func (u *usageRecorder) Parameters() *tools.Schema  { return u.inner.Parameters() }

func (u *usageRecorder) Call(ctx context.Context, args string) (tools.Result, error) {
	u.note(u.inner.Name())
	return u.inner.Call(ctx, args)
}

// TestLiveDeepSeekThroughTheCommandLine drives the composition a user actually
// gets: flags parsed, provider selected from the environment, the built-in
// tools rooted at a directory, and print mode's stream discipline.
//
// The pieces are each covered offline against a scripted model. What this adds
// is that they compose — a provider chosen by credential rather than by flag,
// a real model deciding to use a tool, and the answer arriving on stdout with
// nothing beside it.
func TestLiveDeepSeekThroughTheCommandLine(t *testing.T) {
	liveOrSkip(t)

	dir := t.TempDir()
	const token = "wombat-40219-saffron"
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"),
		[]byte("marker: "+token+"\n"), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	args := cli.ParseArgs([]string{"-p", "Read notes.txt and tell me the marker."})
	if !args.Print || len(args.Messages) != 1 {
		t.Fatalf("the command line parsed as %+v", args)
	}
	// stdin and stdout are not terminals under `go test`, so this would resolve
	// to print even without the flag — which is the behaviour, not an accident.
	if mode := cli.ResolveAppMode(args, false, false); mode != cli.AppPrint {
		t.Fatalf("a redirected one-shot resolved to %q", mode)
	}

	transport := &countingRoundTripper{inner: http.DefaultTransport}
	port, provider, model, err := cli.Open(args, transport)
	if err != nil {
		t.Fatalf("opening the provider: %v", err)
	}
	if provider != "deepseek" {
		t.Fatalf("the credential in the environment selected %q", provider)
	}

	registry, err := tools.NewBuiltInRegistry(dir)
	if err != nil {
		t.Fatalf("building the tool set: %v", err)
	}

	// A run that records nothing: this test is about the command line's
	// composition, and leaving a session behind would make it depend on the
	// state of whatever ran before it.
	conversation, err := cli.OpenConversation(cli.Args{NoSession: true}, dir, cli.DefaultSystemPrompt)
	if err != nil {
		t.Fatalf("opening the conversation: %v", err)
	}
	defer conversation.Close()

	var out, errOut bytes.Buffer
	code := cli.RunPrint(context.Background(), cli.Runtime{
		Model:        port,
		ModelName:    model,
		Tools:        registry,
		System:       cli.DefaultSystemPrompt,
		Provider:     provider,
		Conversation: conversation,
	}, cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errOut}, args.Messages)

	if code != 0 {
		t.Fatalf("exit %d; stderr: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), token) {
		t.Fatalf("the answer does not carry what the file held:\n%s", out.String())
	}
	// Nothing but the answer: a caller piping this into another program must
	// not receive a banner or a progress line.
	if errOut.Len() != 0 {
		t.Fatalf("a successful run wrote to stderr: %q", errOut.String())
	}

	t.Logf("provider=%s model=%s requests=%d", provider, model, transport.count())
	t.Logf("stdout: %s", strings.TrimSpace(out.String()))
}

// countingRoundTripper counts requests at the layer cli.Open injects, which is
// a RoundTripper rather than a client: the providers differ in which they take,
// and the count has to be at the point every one of them passes through.
type countingRoundTripper struct {
	inner http.RoundTripper
	mu    sync.Mutex
	sent  int
}

func (c *countingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.sent++
	c.mu.Unlock()
	return c.inner.RoundTrip(req)
}

func (c *countingRoundTripper) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sent
}

// TestLiveDeepSeekRemembersAcrossTwoRuns is what session persistence is for,
// and the only check that can prove it: two runs, each with its own session
// object, connected by nothing but the file on disk.
//
// The codeword cannot be guessed and is not in either prompt's own context, so
// the second run answering it means the first run's conversation was read back
// and sent to the provider.
func TestLiveDeepSeekRemembersAcrossTwoRuns(t *testing.T) {
	liveOrSkip(t)

	agentDir := t.TempDir()
	work := t.TempDir()
	const codeword = "axolotl-51772-tundra"

	transport := &countingRoundTripper{inner: http.DefaultTransport}
	port, provider, model, err := cli.Open(cli.Args{}, transport)
	if err != nil {
		t.Fatalf("opening the provider: %v", err)
	}

	answer := func(args cli.Args, prompt string) (string, *cli.Conversation) {
		t.Helper()
		conversation, err := cli.OpenConversation(args, work, cli.DefaultSystemPrompt)
		if err != nil {
			t.Fatalf("opening the conversation: %v", err)
		}
		var out, errOut bytes.Buffer
		code := cli.RunPrint(context.Background(), cli.Runtime{
			Model: port, ModelName: model, Tools: tools.NewRegistry(),
			System: cli.DefaultSystemPrompt, Provider: provider,
			Conversation: conversation,
		}, cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errOut}, []string{prompt})
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errOut.String())
		}
		return out.String(), conversation
	}

	_, first := answer(cli.Args{SessionDir: agentDir},
		"Remember this codeword: "+codeword+". Just acknowledge it.")
	if first.Path == "" {
		t.Fatal("the first run recorded nothing, so there is nothing to continue")
	}
	first.Close()

	// A second run, connected to the first by the file alone.
	said, second := answer(cli.Args{SessionDir: agentDir, Continue: true},
		"What was the codeword I gave you? Reply with just the codeword.")
	defer second.Close()

	if !second.Resumed {
		t.Fatal("the second run did not report itself as resumed")
	}
	if !strings.Contains(said, codeword) {
		t.Fatalf("the second run did not remember the codeword: %q", said)
	}

	t.Logf("session %s at %s", second.ID, second.Path)
	t.Logf("second run said: %s", strings.TrimSpace(said))
	t.Logf("requests across both runs: %d", transport.count())
}
