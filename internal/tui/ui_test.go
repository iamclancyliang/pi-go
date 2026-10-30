package tui

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTerm is a Terminal over the test terminal, recording what the
// interface did to it.
type fakeTerm struct {
	vt       *vt
	mu       sync.Mutex
	onInput  func([]byte)
	onResize func()
	started  bool
	stopped  bool
	starts   int
	frames   int
}

func (f *fakeTerm) Start(onInput func([]byte), onResize func()) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onInput, f.onResize, f.started = onInput, onResize, true
	f.starts++
	return nil
}

func (f *fakeTerm) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	return nil
}

func (f *fakeTerm) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames += strings.Count(string(p), syncBegin)
	return f.vt.Write(p)
}

func (f *fakeTerm) Size() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.vt.width, f.vt.height
}

func (f *fakeTerm) send(s string) {
	f.mu.Lock()
	onInput := f.onInput
	f.mu.Unlock()
	onInput([]byte(s))
}

func (f *fakeTerm) isStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

func (f *fakeTerm) frameCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.frames
}

// echo is a component that shows the keys it was given.
type echo struct {
	keys    []string
	focused bool
	onKey   func(Key)
}

func (e *echo) Invalidate() {}
func (e *echo) Render(width int) []string {
	line := "keys: " + strings.Join(e.keys, " ")
	if e.focused {
		line += CursorMarker
	}
	return []string{TruncateToWidth(line, width, "", false)}
}
func (e *echo) SetFocused(f bool) { e.focused = f }
func (e *echo) HandleKey(k Key) {
	name := k.Name
	if k.IsText() {
		name = string(k.Text)
	}
	if k.Name == "paste" {
		name = "paste(" + k.Paste + ")"
	}
	e.keys = append(e.keys, name)
	if e.onKey != nil {
		e.onKey(k)
	}
}

type running struct {
	t    *testing.T
	ui   *UI
	term *fakeTerm
	done chan error
}

func startUI(t *testing.T, width, height int, children ...Component) *running {
	t.Helper()
	term := &fakeTerm{vt: newVT(t, width, height)}
	ui := NewUI(term)
	ui.escTimeout = 30 * time.Millisecond
	for _, c := range children {
		ui.AddChild(c)
	}
	r := &running{t: t, ui: ui, term: term, done: make(chan error, 1)}
	go func() { r.done <- ui.Run() }()
	r.idle()
	t.Cleanup(func() { ui.Stop(); <-r.done })
	return r
}

// idle waits until the loop has done everything queued so far.
func (r *running) idle() {
	r.t.Helper()
	for i := 0; i < 2; i++ {
		done := make(chan struct{})
		r.ui.Do(func() { close(done) })
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			r.t.Fatal("the interface loop is stuck")
		}
		time.Sleep(minFrameInterval + 5*time.Millisecond) // let a throttled frame land
	}
}

func (r *running) screen() string {
	r.term.mu.Lock()
	defer r.term.mu.Unlock()
	return strings.Join(r.term.vt.screen(), "\n")
}

func TestAKeystrokeReachesTheFocusedComponentAndIsDrawnAtOnce(t *testing.T) {
	e := &echo{}
	r := startUI(t, 40, 5, &Text{text: "header"}, e)
	r.ui.SetFocus(e)
	r.idle()
	before := r.term.frameCount()
	r.term.send("hi")
	done := make(chan struct{})
	r.ui.Do(func() { close(done) })
	<-done
	if !strings.Contains(r.screen(), "keys: h i") {
		t.Fatalf("screen %q", r.screen())
	}
	if r.term.frameCount() == before {
		t.Fatal("the keystroke waited for a throttled frame")
	}
	if !e.focused {
		t.Fatal("the focused component was not told")
	}
}

func TestALoneEscapeIsTheEscapeKeyOnlyOnceNothingFollows(t *testing.T) {
	e := &echo{}
	r := startUI(t, 40, 5, e)
	r.ui.SetFocus(e)
	r.term.send("\x1b")
	r.term.send("b") // within the timeout: alt+b, not escape then b
	r.idle()
	r.term.send("\x1b")
	time.Sleep(3 * r.ui.escTimeout)
	r.idle()
	if got := strings.Join(e.keys, " "); got != "alt+b escape" {
		t.Fatalf("keys %q", got)
	}
}

func TestAPasteReachesTheComponentWhole(t *testing.T) {
	e := &echo{}
	r := startUI(t, 60, 5, e)
	r.ui.SetFocus(e)
	r.term.send("\x1b[200~two\r\nlines")
	r.term.send("\x1b[201~")
	r.idle()
	if got := strings.Join(e.keys, " "); got != "paste(two\r\nlines)" {
		t.Fatalf("keys %q", got)
	}
}

func TestRenderRequestsAreFoldedIntoFrames(t *testing.T) {
	r := startUI(t, 40, 5, &Text{text: "static"})
	before := r.term.frameCount()
	for i := 0; i < 200; i++ {
		r.ui.RequestRender()
	}
	r.idle()
	if frames := r.term.frameCount() - before; frames > 2 {
		t.Fatalf("200 requests drew %d frames", frames)
	}
}

func TestAHandlerMayStopTheInterface(t *testing.T) {
	term := &fakeTerm{vt: newVT(t, 40, 5)}
	ui := NewUI(term)
	e := &echo{}
	e.onKey = func(k Key) {
		if k.Name == "ctrl+d" {
			ui.RequestRender()
			ui.Stop()
		}
	}
	ui.AddChild(e)
	ui.SetFocus(e)
	done := make(chan error, 1)
	go func() { done <- ui.Run() }()
	time.Sleep(20 * time.Millisecond)
	term.send("\x04")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stopping from a key handler deadlocked")
	}
	if !term.isStopped() {
		t.Fatal("the terminal was not given back")
	}
}

type tooWide struct{}

func (tooWide) Invalidate()               {}
func (tooWide) Render(width int) []string { return []string{strings.Repeat("x", width+1)} }

func TestAnOverflowingComponentStopsTheInterfaceAndSaysWhy(t *testing.T) {
	term := &fakeTerm{vt: newVT(t, 10, 5)}
	ui := NewUI(term)
	ui.AddChild(tooWide{})
	done := make(chan error, 1)
	go func() { done <- ui.Run() }()
	select {
	case err := <-done:
		var wide *errLineTooWide
		if !errors.As(err, &wide) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an overflowing frame did not stop the interface")
	}
	if !term.isStopped() {
		t.Fatal("the terminal was left raw")
	}
}

func TestTheEscapeTimeoutFollowsPi(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	cases := []struct {
		vars map[string]string
		want time.Duration
	}{
		{nil, 10 * time.Millisecond},
		{map[string]string{"SSH_TTY": "/dev/ttys001"}, 100 * time.Millisecond},
		{map[string]string{"SSH_CONNECTION": "1.2.3.4"}, 100 * time.Millisecond},
		{map[string]string{"PI_TUI_ESC_TIMEOUT": "25", "SSH_TTY": "x"}, 25 * time.Millisecond},
		{map[string]string{"PI_TUI_ESC_TIMEOUT": "nonsense"}, 10 * time.Millisecond},
	}
	for _, c := range cases {
		if got := escapeTimeout(env(c.vars)); got != c.want {
			t.Errorf("escapeTimeout(%v) = %v, want %v", c.vars, got, c.want)
		}
	}
}

// protocolTerm is a fakeTerm that negotiates.
type protocolTerm struct {
	*fakeTerm
	kitty   bool
	reports []string
}

func (p *protocolTerm) KittyActive() bool { return p.kitty }
func (p *protocolTerm) HandleReport(r string) {
	p.reports = append(p.reports, r)
	if r == "kitty:7" {
		p.kitty = true
	}
}

// TestProtocolRepliesGoToTheTerminalAndChangeDecoding. The terminal's answer
// arrives among the keystrokes; delivered as one, it would type its bytes.
// Once Kitty's protocol is on, a line feed is shift+enter, as Pi reads it.
func TestProtocolRepliesGoToTheTerminalAndChangeDecoding(t *testing.T) {
	term := &protocolTerm{fakeTerm: &fakeTerm{vt: newVT(t, 40, 5)}}
	ui := NewUI(term)
	e := &echo{}
	ui.AddChild(e)
	ui.SetFocus(e)
	done := make(chan error, 1)
	go func() { done <- ui.Run() }()
	defer func() { ui.Stop(); <-done }()
	time.Sleep(20 * time.Millisecond)

	term.send("\x1b[?7u")
	term.send("\n")
	term.send("\x1b[97;1:3u") // a released a
	finished := make(chan struct{})
	ui.Do(func() { close(finished) })
	<-finished
	got := make(chan string, 1)
	ui.Do(func() { got <- strings.Join(e.keys, " ") + "|" + strings.Join(term.reports, ",") })
	if s := <-got; s != "shift+enter|kitty:7" {
		t.Fatalf("keys|reports = %q", s)
	}
}
