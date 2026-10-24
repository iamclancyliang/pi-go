package tui

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// TestTheRealTerminalStartsDrawsAndRestores drives a real terminal, so it
// runs only when asked and under a pseudo-terminal, for example:
//
//	PI_GO_TTY_SMOKE=1 script -q /dev/null go test ./internal/tui/ -run TestTheRealTerminal -count=1
//
// It checks what a unit test cannot: that raw mode is entered and left, that
// frames reach the terminal, that keystrokes typed at it arrive, and that
// stopping returns the terminal in the mode it was found. The harness must
// type "ab" and an up arrow while it runs; without them it fails.
func TestTheRealTerminalStartsDrawsAndRestores(t *testing.T) {
	if os.Getenv("PI_GO_TTY_SMOKE") != "1" {
		t.Skip("set PI_GO_TTY_SMOKE=1 and run under a pseudo-terminal")
	}
	tty, err := OpenTTY()
	if err != nil {
		t.Fatalf("OpenTTY: %v", err)
	}
	defer tty.Close()

	ui := NewUI(tty)
	ui.AddChild(NewText("pi-go interface smoke test", 1, 0, nil))
	ui.AddChild(NewLoader(ui, nil, nil, "Working...", nil, 0))
	keys := &echo{}
	ui.AddChild(keys)
	ui.SetFocus(keys)
	done := make(chan error, 1)
	go func() { done <- ui.Run() }()
	// The harness types "ab" and an up arrow into the terminal meanwhile.
	time.Sleep(600 * time.Millisecond)
	got := make(chan string, 1)
	ui.Do(func() { got <- strings.Join(keys.keys, " ") })
	if typed := <-got; typed != "a b up" {
		t.Errorf("keys typed at the terminal arrived as %q", typed)
	}
	ui.Stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the interface did not stop")
	}
}

// TestTheKeyboardProtocolFollowsTheTerminalsAnswer, written where a terminal
// would read it: modifyOtherKeys for a terminal that answers only the
// sentinel, then off again if a Kitty answer follows, and the protocol popped
// on the way out.
func TestTheKeyboardProtocolFollowsTheTerminalsAnswer(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	tty := &TTY{file: w, kittyPushed: true}

	tty.HandleReport("device-attributes")
	tty.HandleReport("device-attributes")
	tty.HandleReport("kitty:7")
	if !tty.KittyActive() {
		t.Fatal("a Kitty answer did not turn the protocol on")
	}
	tty.HandleReport("device-attributes") // late sentinel: Kitty stays
	_ = tty.Stop()
	w.Close()
	written, _ := io.ReadAll(r)
	want := "\x1b[>4;2m" + "\x1b[>4;0m" + "\x1b[?2004l" + "\x1b[<u"
	if string(written) != want {
		t.Fatalf("wrote %q, want %q", written, want)
	}
}
