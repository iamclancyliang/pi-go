package interactive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/tui"
)

func TestTheKeyFileIsReadWithItsOldNamesMigrated(t *testing.T) {
	path := filepath.Join(t.TempDir(), KeybindingsFileName)
	if got, err := LoadKeybindingsFile(path); got != nil || err != nil {
		t.Fatalf("a missing file gave %v, %v", got, err)
	}
	if err := os.WriteFile(path, []byte(`{
		"tui.editor.historyPrevious": "ctrl+p",
		"tui.editor.deleteWordBackward": ["ctrl+w", "alt+backspace"],
		"expandTools": "ctrl+e",
		"yank": "ctrl+v",
		"tui.editor.yank": "ctrl+y",
		"app.clear": 7
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadKeybindingsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"tui.editor.historyPrevious":    "ctrl+p",
		"tui.editor.deleteWordBackward": "ctrl+w alt+backspace",
		"app.tools.expand":              "ctrl+e",
		"tui.editor.yank":               "ctrl+y",
	}
	if len(got) != len(want) {
		t.Fatalf("read %v", got)
	}
	for id, keys := range want {
		if strings.Join(got[id], " ") != keys {
			t.Fatalf("%s is %v, want %s", id, got[id], keys)
		}
	}
	keys := NewKeybindings("linux", got)
	if !keys.Matches(tui.Key{Name: "ctrl+e"}, "app.tools.expand") || keys.Matches(tui.Key{Name: "ctrl+o"}, "app.tools.expand") {
		t.Fatalf("the migrated binding is not in force: %v", keys.Keys("app.tools.expand"))
	}
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeybindingsFile(path); err == nil {
		t.Fatal("a file that is not JSON was accepted")
	}
}

func TestCtrlXCopiesTheLastAnswer(t *testing.T) {
	r := startMode(t, Options{})
	r.term.send("\x18")
	r.idle()
	if got := r.screen(); !strings.Contains(got, "No agent messages to copy yet.") {
		t.Fatalf("with nothing to copy:\n%s", got)
	}
	r.host.lastText = "the answer"
	r.term.send("\x18")
	waitFor(t, "the copy", func() bool {
		r.host.smu.Lock()
		defer r.host.smu.Unlock()
		return len(r.host.copied) == 1 && r.host.copied[0] == "the answer"
	})
	waitFor(t, "the status", func() bool { return strings.Contains(r.screen(), "Copied last agent message to clipboard") })
}

func TestCtrlVPastesTheClipboardsText(t *testing.T) {
	r := startMode(t, Options{})
	r.host.clipboard = "from the clipboard"
	r.term.send("abc")
	r.idle()
	r.term.send("\x16")
	waitFor(t, "the paste", func() bool { return strings.Contains(r.screen(), "abcfrom the clipboard") })
}

func TestCtrlGEditsTheTextOutside(t *testing.T) {
	r := startMode(t, Options{})
	r.host.edited, r.host.editOK = "written elsewhere", true
	r.term.send("draft")
	r.idle()
	r.term.send("\x07")
	r.idle()
	if len(r.host.editSeen) != 1 || r.host.editSeen[0] != "draft" {
		t.Fatalf("the editor was given %v", r.host.editSeen)
	}
	if got := r.screen(); !strings.Contains(got, "written elsewhere") || strings.Contains(got, "draft") {
		t.Fatalf("after editing:\n%s", got)
	}
	// An editor that failed leaves the text alone.
	r.host.editOK = false
	r.term.send("\x07")
	r.idle()
	if got := r.screen(); !strings.Contains(got, "written elsewhere") {
		t.Fatalf("a failed edit changed the text:\n%s", got)
	}
}

func TestAltUpTakesQueuedMessagesBackIntoTheEditor(t *testing.T) {
	r := startMode(t, Options{})
	r.term.send("\x1b[1;3A")
	r.idle()
	if got := r.screen(); !strings.Contains(got, "No queued messages to restore") {
		t.Fatalf("with nothing queued:\n%s", got)
	}
	r.typeLine("first")
	waitFor(t, "the run to start", func() bool { return r.host.currentRun() != nil })
	r.idle()
	r.typeLine("change course")
	run := r.host.currentRun()
	waitFor(t, "the steer", func() bool {
		run.mu.Lock()
		defer run.mu.Unlock()
		return len(run.steered) == 1
	})
	r.term.send("and then")
	r.term.send("\x1b\r") // alt+enter: a follow-up
	waitFor(t, "the follow-up", func() bool {
		run.mu.Lock()
		defer run.mu.Unlock()
		return len(run.follows) == 1
	})
	r.idle()
	r.term.send("\x1b[1;3A")
	r.idle()
	got := r.screen()
	if !strings.Contains(got, "Restored 2 queued messages to editor") || strings.Contains(got, "Steering:") || strings.Contains(got, "Follow-up:") {
		t.Fatalf("after taking them back:\n%s", got)
	}
	if text := r.mode.editor.Text(); text != "change course\n\nand then" {
		t.Fatalf("the editor holds %q", text)
	}
	run.done <- nil
}

func TestSuspendIsRefusedOnWindows(t *testing.T) {
	r := startMode(t, Options{})
	r.mode.env.GOOS = "windows"
	r.mode.suspend()
	r.idle()
	if got := r.screen(); !strings.Contains(got, "Suspend to background is not supported on Windows") {
		t.Fatalf("on windows:\n%s", got)
	}
}

func TestHotkeysAreListedAsTheyAreBound(t *testing.T) {
	r := startMode(t, Options{})
	r.mode.env.Keys.SetUser(map[string][]string{"app.tools.expand": {"ctrl+e"}})
	r.typeLine("/hotkeys")
	got := r.screen()
	for _, want := range []string{"Keyboard Shortcuts", "Navigation", "Ctrl+E", "Toggle tool output expansion", "Run bash command (excluded from context)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if len(r.host.commands) != 0 {
		t.Fatalf("the line command ran too: %v", r.host.commands)
	}
}

func TestReloadRedrawsTheHeaderWithTheKeysInForce(t *testing.T) {
	r := startMode(t, Options{Version: "test"})
	if got := r.screen(); !strings.Contains(got, "Press ctrl+o to show") {
		t.Fatalf("the header before:\n%s", got)
	}
	r.mode.env.Keys.SetUser(map[string][]string{"app.tools.expand": {"ctrl+e"}})
	r.typeLine("/reload")
	time.Sleep(50 * time.Millisecond)
	r.idle()
	if got := r.screen(); !strings.Contains(got, "Press ctrl+e to show") || strings.Contains(got, "ctrl+o") {
		t.Fatalf("the header after:\n%s", got)
	}
}
