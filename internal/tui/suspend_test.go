package tui

import (
	"testing"
	"time"
)

// TestSuspendGivesTheTerminalBackWhileItsWorkRuns, and takes it again after,
// redrawing everything.
func TestSuspendGivesTheTerminalBackWhileItsWorkRuns(t *testing.T) {
	term := &fakeTerm{vt: newVT(t, 40, 10)}
	ui := NewUI(term)
	ui.AddChild(NewText("before", 0, 0, nil))
	done := make(chan error, 1)
	go func() { done <- ui.Run() }()
	defer func() { ui.Stop(); <-done }()

	ran := make(chan bool, 1)
	ui.Do(func() {
		var stoppedMeanwhile bool
		err := ui.Suspend(func() {
			term.mu.Lock()
			stoppedMeanwhile = term.stopped
			term.mu.Unlock()
		})
		if err != nil {
			t.Error(err)
		}
		ran <- stoppedMeanwhile
	})
	select {
	case stopped := <-ran:
		if !stopped {
			t.Fatal("the terminal was still the interface's while the work ran")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Suspend never ran its work")
	}
	term.mu.Lock()
	starts := term.starts
	term.mu.Unlock()
	if starts != 2 {
		t.Fatalf("the terminal was started %d times; Suspend should have started it again", starts)
	}
}

func TestUserBindingsCanBeReplacedInPlace(t *testing.T) {
	kb := NewKeybindings(TUIKeybindings, nil)
	if !kb.Matches(Key{Name: "ctrl+y"}, "tui.editor.yank") {
		t.Fatal("the default binding does not match")
	}
	kb.SetUser(map[string][]string{"tui.editor.yank": {"ctrl+q"}, "tui.editor.undo": {"ctrl+q"}})
	if kb.Matches(Key{Name: "ctrl+y"}, "tui.editor.yank") || !kb.Matches(Key{Name: "ctrl+q"}, "tui.editor.yank") {
		t.Fatalf("after replacing, yank is bound to %v", kb.Keys("tui.editor.yank"))
	}
	conflicts := kb.Conflicts()
	if len(conflicts) != 1 || conflicts[0].Key != "ctrl+q" {
		t.Fatalf("conflicts %v", conflicts)
	}
	kb.SetUser(nil)
	if !kb.Matches(Key{Name: "ctrl+y"}, "tui.editor.yank") || len(kb.Conflicts()) != 0 {
		t.Fatal("the defaults did not come back")
	}
}
