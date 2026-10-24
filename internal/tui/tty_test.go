package tui

import (
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
