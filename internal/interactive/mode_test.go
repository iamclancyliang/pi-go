package interactive

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/tools"
	"github.com/iamclancyliang/pi-go/internal/tui"
)

// modeTerm is a terminal that takes keystrokes from the test and discards
// what is drawn; the test reads the components instead.
type modeTerm struct {
	mu      sync.Mutex
	onInput func([]byte)
}

func (t *modeTerm) Start(onInput func([]byte), _ func()) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onInput = onInput
	return nil
}
func (t *modeTerm) Stop() error                 { return nil }
func (t *modeTerm) Write(p []byte) (int, error) { return len(p), nil }
func (t *modeTerm) Size() (int, int)            { return 80, 30 }
func (t *modeTerm) send(s string) {
	t.mu.Lock()
	in := t.onInput
	t.mu.Unlock()
	in([]byte(s))
}

type fakeRun struct {
	mu      sync.Mutex
	steered []string
	follows []string
	done    chan error
}

func (r *fakeRun) Steer(text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steered = append(r.steered, text)
	return nil
}
func (r *fakeRun) Follow(text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.follows = append(r.follows, text)
	return nil
}
func (r *fakeRun) Wait() error { return <-r.done }

type fakeHost struct {
	mu           sync.Mutex
	prompts      []string
	ctxs         []context.Context
	run          *fakeRun
	conversation []ai.Message
	commands     []string
	command      func(text string, out *bytes.Buffer, confirm func(string) bool) (bool, bool)
}

func (h *fakeHost) Start(ctx context.Context, prompt string) (Run, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prompts = append(h.prompts, prompt)
	h.ctxs = append(h.ctxs, ctx)
	h.conversation = append(h.conversation, ai.Message{Role: ai.RoleUser, Content: prompt})
	h.run = &fakeRun{done: make(chan error, 1)}
	return h.run, nil
}

func (h *fakeHost) Command(_ context.Context, text string, out *bytes.Buffer, confirm func(string) bool) (bool, bool) {
	h.mu.Lock()
	h.commands = append(h.commands, text)
	command := h.command
	h.mu.Unlock()
	if command != nil {
		return command(text, out, confirm)
	}
	out.WriteString("ran " + text + "\n")
	return true, text == "/quit"
}

func (h *fakeHost) Conversation() []ai.Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ai.Message(nil), h.conversation...)
}
func (h *fakeHost) Footer() FooterState {
	return FooterState{Cwd: "/work", Model: "test-model", ContextWindow: 1000}
}
func (h *fakeHost) SlashCommands() []tui.SlashCommand { return []tui.SlashCommand{{Name: "help"}} }
func (h *fakeHost) Files() tui.PathFinder             { return nil }

func (h *fakeHost) currentRun() *fakeRun {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.run
}

type modeRig struct {
	t    *testing.T
	term *modeTerm
	host *fakeHost
	mode *Mode
	ui   *tui.UI
	done chan error
}

func startMode(t *testing.T, opts Options) *modeRig {
	t.Helper()
	term := &modeTerm{}
	host := &fakeHost{}
	ui := tui.NewUI(term)
	env := &Env{Theme: DarkTheme(true), Keys: NewKeybindings("linux", nil), Cwd: t.TempDir(), Home: "/home/tester", GOOS: "linux"}
	r := &modeRig{t: t, term: term, host: host, ui: ui, done: make(chan error, 1)}
	r.mode = NewMode(ui, env, host, opts)
	go func() { r.done <- r.mode.Run() }()
	t.Cleanup(func() {
		ui.Stop()
		select {
		case <-r.done:
		case <-time.After(5 * time.Second):
		}
	})
	r.idle()
	return r
}

// idle waits for the loop to finish what it has been given.
func (r *modeRig) idle() {
	r.t.Helper()
	for i := 0; i < 3; i++ {
		done := make(chan struct{})
		r.ui.Do(func() { close(done) })
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			r.t.Fatal("the loop is stuck")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// screen is what the mode draws, without styling.
func (r *modeRig) screen() string {
	r.t.Helper()
	out := make(chan string, 1)
	r.ui.Do(func() { out <- tui.StripSequences(strings.Join(r.ui.Container.Render(80), "\n")) })
	return <-out
}

func (r *modeRig) typeLine(s string) {
	r.term.send(s)
	r.term.send("\r")
	r.idle()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestModeRunsAPromptWithAToolCall(t *testing.T) {
	r := startMode(t, Options{Version: "test"})
	r.typeLine("read the readme")
	waitFor(t, "the run to start", func() bool { return r.host.currentRun() != nil })
	if got := r.screen(); !strings.Contains(got, "read the readme") || !strings.Contains(got, "Working...") {
		t.Fatalf("after submit:\n%s", got)
	}

	reply := r.mode.ReplyObserver()
	partial := &ai.AssistantMessage{Blocks: []ai.Block{
		{Kind: ai.BlockText, Text: "Let me look."},
		{Kind: ai.BlockToolCall, Call: ai.ToolCall{ID: "c1", Name: "read", Args: `{"path":"README.md"}`}},
	}}
	reply(ai.StreamEvent{Kind: ai.StreamToolCallEnd, Partial: partial})
	final := *partial
	final.StopReason = ai.StopToolUse
	final.Usage = ai.Usage{InputTokens: 300, OutputTokens: 50, Reported: true}
	reply(ai.StreamEvent{Kind: ai.StreamDone, Final: &final})
	obs := r.mode.Observer()
	obs.OnEvent(events.Event{Kind: events.KindToolStart, ToolCallID: "c1", ToolName: "read", Detail: events.Detail{Args: `{"path":"README.md"}`}})
	obs.OnEvent(events.Event{Kind: events.KindToolEnd, ToolCallID: "c1", ToolName: "read"})
	obs.OnEvent(events.Event{Kind: events.KindToolResult, ToolCallID: "c1", ToolName: "read",
		Detail: events.Detail{Result: "hello", ToolDetails: tools.ReadDetails{}}})
	r.idle()
	got := r.screen()
	for _, want := range []string{"Let me look.", "read README.md", "35.0%/1.0k"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}

	r.host.currentRun().done <- nil
	r.idle()
	if got := r.screen(); strings.Contains(got, "Working...") {
		t.Fatalf("the working line stayed after the run:\n%s", got)
	}
}

func TestModeSteersARunAndShowsTheMessageWhenTaken(t *testing.T) {
	r := startMode(t, Options{})
	r.typeLine("first")
	waitFor(t, "the run to start", func() bool { return r.host.currentRun() != nil })
	r.idle()
	r.typeLine("change course")
	run := r.host.currentRun()
	waitFor(t, "the steer", func() bool {
		run.mu.Lock()
		defer run.mu.Unlock()
		return len(run.steered) == 1 && run.steered[0] == "change course"
	})
	if got := r.screen(); !strings.Contains(got, "Steering: change course") {
		t.Fatalf("pending:\n%s", got)
	}
	// The runtime takes the message into the conversation; the next
	// request shows it was delivered.
	r.host.mu.Lock()
	r.host.conversation = append(r.host.conversation, ai.Message{Role: ai.RoleUser, Content: "change course"})
	r.host.mu.Unlock()
	r.mode.Observer().OnEvent(events.Event{Kind: events.KindModelRequest})
	r.idle()
	got := r.screen()
	if strings.Contains(got, "Steering:") || strings.Count(got, "change course") != 1 {
		t.Fatalf("after delivery:\n%s", got)
	}
	run.done <- nil
}

func TestModeRunsCommandsAndAsksTheirQuestions(t *testing.T) {
	r := startMode(t, Options{})
	answered := make(chan bool, 1)
	r.host.command = func(text string, out *bytes.Buffer, confirm func(string) bool) (bool, bool) {
		answered <- confirm("Upload it? [y/N]")
		out.WriteString("uploaded\n")
		return true, false
	}
	r.typeLine("/share")
	waitFor(t, "the question", func() bool { return strings.Contains(r.screen(), "Upload it? [y/N]") })
	r.typeLine("y")
	select {
	case yes := <-answered:
		if !yes {
			t.Fatal("yes was read as no")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the question was never answered")
	}
	waitFor(t, "the output", func() bool { return strings.Contains(r.screen(), "uploaded") })
}

func TestModeEscapeInterruptsAndQueuedMessagesReturn(t *testing.T) {
	r := startMode(t, Options{})
	r.typeLine("long task")
	waitFor(t, "the run to start", func() bool { return r.host.currentRun() != nil })
	r.idle()
	r.term.send("\x1b")
	r.idle()
	time.Sleep(50 * time.Millisecond)
	r.host.mu.Lock()
	ctx := r.host.ctxs[0]
	r.host.mu.Unlock()
	if ctx.Err() == nil {
		t.Fatal("escape did not cancel the run")
	}
	r.host.currentRun().done <- context.Canceled
	r.idle()
	if got := r.screen(); strings.Contains(got, "Error") {
		t.Fatalf("an interrupt was shown as an error:\n%s", got)
	}
}

func TestModeCtrlCTwiceExits(t *testing.T) {
	r := startMode(t, Options{})
	r.term.send("abc")
	r.idle()
	r.term.send("\x03")
	r.idle()
	if strings.Contains(r.screen(), "abc") {
		t.Fatal("ctrl+c did not clear the editor")
	}
	r.term.send("\x03")
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("ctrl+c twice did not exit")
	}
}

func TestModeShowsTheConversationSoFar(t *testing.T) {
	term := &modeTerm{}
	host := &fakeHost{conversation: []ai.Message{
		{Role: ai.RoleUser, Content: "earlier question"},
		{Role: ai.RoleAssistant, Content: "earlier answer", ToolCalls: []ai.ToolCall{{ID: "t1", Name: "ls", Args: `{}`}}},
		{Role: ai.RoleTool, ToolCallID: "t1", Content: "a.txt"},
	}}
	ui := tui.NewUI(term)
	env := &Env{Theme: DarkTheme(true), Keys: NewKeybindings("linux", nil), Cwd: t.TempDir(), GOOS: "linux"}
	m := NewMode(ui, env, host, Options{QuietStartup: true})
	done := make(chan error, 1)
	go func() { done <- m.Run() }()
	defer func() { ui.Stop(); <-done }()
	time.Sleep(100 * time.Millisecond)
	out := make(chan string, 1)
	ui.Do(func() { out <- tui.StripSequences(strings.Join(ui.Container.Render(80), "\n")) })
	got := <-out
	for _, want := range []string{"earlier question", "earlier answer", "ls .", "a.txt"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}
