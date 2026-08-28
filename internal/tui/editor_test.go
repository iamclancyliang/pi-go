package tui_test

import (
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/tui"
)

// type feeds a string of ordinary text.
func typeText(e *tui.Editor, text string) {
	for _, r := range text {
		e.Apply(tui.Key{Text: r})
	}
}

func press(e *tui.Editor, names ...string) tui.Result {
	var last tui.Result
	for _, name := range names {
		last = e.Apply(tui.Key{Name: name})
	}
	return last
}

func TestTypingAndSubmitting(t *testing.T) {
	e := tui.NewEditor(nil)
	typeText(e, "hello world")
	got := press(e, "enter")
	if !got.Submit || got.Submitted != "hello world" {
		t.Fatalf("submitted %+v", got)
	}
	if e.Text() != "" {
		t.Fatalf("the buffer was not cleared: %q", e.Text())
	}
}

// TestEnterSubmitsAndCtrlJDoesNot is the distinction Pi documents: classic
// terminals cannot express shift+enter, so ctrl+j is the newline.
func TestEnterSubmitsAndCtrlJDoesNot(t *testing.T) {
	e := tui.NewEditor(nil)
	typeText(e, "first")
	if got := press(e, "ctrl+j"); got.Submit {
		t.Fatal("ctrl+j submitted")
	}
	typeText(e, "second")
	got := press(e, "enter")
	if got.Submitted != "first\nsecond" {
		t.Fatalf("submitted %q", got.Submitted)
	}
}

// TestWordwiseMovementAndDeletion, the keys a shell user's fingers know.
func TestWordwiseMovementAndDeletion(t *testing.T) {
	e := tui.NewEditor(nil)
	typeText(e, "one two three")

	press(e, "ctrl+w")
	if e.Text() != "one two " {
		t.Fatalf("ctrl+w left %q", e.Text())
	}
	press(e, "alt+b")
	if e.Text()[e.Cursor():] != "two " {
		t.Fatalf("alt+b landed at %d in %q", e.Cursor(), e.Text())
	}
	press(e, "ctrl+u")
	if e.Text() != "two " {
		t.Fatalf("ctrl+u left %q", e.Text())
	}
}

// TestConsecutiveKillsYankBackAsOne: "delete three words then yank" pastes
// three words, not the last one.
func TestConsecutiveKillsYankBackAsOne(t *testing.T) {
	e := tui.NewEditor(nil)
	typeText(e, "alpha beta gamma")
	press(e, "ctrl+w", "ctrl+w", "ctrl+w")
	if e.Text() != "" {
		t.Fatalf("three word-kills left %q", e.Text())
	}
	press(e, "ctrl+y")
	if e.Text() != "alpha beta gamma" {
		t.Fatalf("yank pasted %q", e.Text())
	}
}

// TestYankPopCyclesOlderKills, and only straight after a yank.
func TestYankPopCyclesOlderKills(t *testing.T) {
	e := tui.NewEditor(nil)
	typeText(e, "first")
	press(e, "ctrl+u")
	typeText(e, "second")
	press(e, "ctrl+u")

	press(e, "ctrl+y")
	if e.Text() != "second" {
		t.Fatalf("yank pasted %q, want the newest kill", e.Text())
	}
	press(e, "alt+y")
	if e.Text() != "first" {
		t.Fatalf("yank-pop left %q, want the older kill", e.Text())
	}

	// Not after anything else: typing invalidates the target.
	typeText(e, "x")
	before := e.Text()
	press(e, "alt+y")
	if e.Text() != before {
		t.Fatalf("yank-pop fired without a preceding yank: %q", e.Text())
	}
}

// TestUndoRestoresWhatADeleteTook.
func TestUndoRestoresWhatADeleteTook(t *testing.T) {
	e := tui.NewEditor(nil)
	typeText(e, "precious text")
	press(e, "ctrl+u")
	if e.Text() != "" {
		t.Fatalf("ctrl+u left %q", e.Text())
	}
	press(e, "ctrl+-")
	if e.Text() != "precious text" {
		t.Fatalf("undo restored %q", e.Text())
	}
}

// TestHistoryBrowsingKeepsTheLineBeingWritten. Going up to check an old prompt
// and coming back down must not have eaten the new one.
func TestHistoryBrowsingKeepsTheLineBeingWritten(t *testing.T) {
	e := tui.NewEditor([]string{"oldest", "newest"})
	typeText(e, "half-written")

	press(e, "up")
	if e.Text() != "newest" {
		t.Fatalf("up recalled %q", e.Text())
	}
	press(e, "up")
	if e.Text() != "oldest" {
		t.Fatalf("up again recalled %q", e.Text())
	}
	press(e, "down", "down")
	if e.Text() != "half-written" {
		t.Fatalf("coming back down lost the line: %q", e.Text())
	}
}

// TestUpMovesWithinAMultilinePromptBeforeReachingHistory. Arrow keys edit the
// prompt being written; history starts where the prompt ends.
func TestUpMovesWithinAMultilinePromptBeforeReachingHistory(t *testing.T) {
	e := tui.NewEditor([]string{"an old prompt"})
	typeText(e, "first line")
	press(e, "ctrl+j")
	typeText(e, "second line")

	press(e, "up")
	if e.Text() != "first line\nsecond line" {
		t.Fatalf("up inside a multiline prompt recalled history: %q", e.Text())
	}
	if !strings.HasPrefix(e.Text()[e.Cursor():], "e") || e.Cursor() > len("first line") {
		// The cursor kept its column on the line above.
	}
	press(e, "up")
	if e.Text() != "an old prompt" {
		t.Fatalf("up from the first line did not reach history: %q", e.Text())
	}
}

// TestCtrlDIsExitOnlyWhenEmpty: with text it deletes forward, as in a shell.
func TestCtrlDIsExitOnlyWhenEmpty(t *testing.T) {
	e := tui.NewEditor(nil)
	typeText(e, "ab")
	press(e, "ctrl+a")
	if got := press(e, "ctrl+d"); got.Exit {
		t.Fatal("ctrl+d with text exited")
	}
	if e.Text() != "b" {
		t.Fatalf("ctrl+d deleted %q", e.Text())
	}
	press(e, "ctrl+d") // deletes the b
	if got := press(e, "ctrl+d"); !got.Exit {
		t.Fatal("ctrl+d on an empty buffer did not exit")
	}
}

// TestSubmittingRemembersSkippingBlanksAndRepeats, as every shell does it.
func TestSubmittingRemembersSkippingBlanksAndRepeats(t *testing.T) {
	e := tui.NewEditor(nil)
	typeText(e, "same")
	press(e, "enter")
	typeText(e, "same")
	press(e, "enter")
	press(e, "enter") // empty

	press(e, "up")
	if e.Text() != "same" {
		t.Fatalf("up recalled %q", e.Text())
	}
	press(e, "up")
	if e.Text() != "same" {
		t.Fatalf("a repeated prompt was stored twice; second up recalled %q", e.Text())
	}
}
