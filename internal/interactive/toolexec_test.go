package interactive

import (
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/tui"
)

func plain(lines []string) string { return tui.StripSequences(strings.Join(lines, "\n")) }

// TestBashShowsElapsedWhileRunningAndTookAfter: a running command's box
// counts the seconds and asks for frames to show them; a finished one says
// how long it took.
func TestBashShowsElapsedWhileRunningAndTookAfter(t *testing.T) {
	env := goldenEnv(t)
	frames := make(chan struct{}, 8)
	env.RequestRender = func() {
		select {
		case frames <- struct{}{}:
		default:
		}
	}
	x := NewToolExecution(env, "bash", `{"command":"make"}`)
	x.SetArgsComplete()
	x.MarkExecutionStarted()
	x.UpdateResult(ToolResult{Text: "building"}, true)
	if got := plain(x.Render(60)); !strings.Contains(got, "Elapsed 0.0s") {
		t.Fatalf("running:\n%s", got)
	}
	select {
	case <-frames:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("a running command asked for no frame to update its clock")
	}
	x.UpdateResult(ToolResult{Text: "done"}, false)
	got := plain(x.Render(60))
	if !strings.Contains(got, "Took ") || strings.Contains(got, "Elapsed") {
		t.Fatalf("finished:\n%s", got)
	}
}

// TestEditPreviewRunsOffTheLoop: with a loop to post to, the preview is
// worked out on another goroutine and applied through it.
func TestEditPreviewRunsOffTheLoop(t *testing.T) {
	env := goldenEnv(t)
	posted := make(chan func(), 1)
	env.Do = func(fn func()) { posted <- fn }
	x := NewToolExecution(env, "edit", `{"path":"notes.txt","edits":[{"oldText":"five","newText":"5"}]}`)
	x.SetArgsComplete()
	if strings.Contains(plain(x.Render(60)), "five") {
		t.Fatal("the preview was applied before the loop ran it")
	}
	select {
	case fn := <-posted:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("no preview was posted")
	}
	got := plain(x.Render(60))
	if !strings.Contains(got, "- 5 five") || !strings.Contains(got, "+ 5 5") {
		t.Fatalf("preview:\n%s", got)
	}
}

// TestToolOutputExpands: folded output shows its first lines and a hint;
// expanded shows it all.
func TestToolOutputExpands(t *testing.T) {
	env := goldenEnv(t)
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, "line")
	}
	x := NewToolExecution(env, "grep", `{"pattern":"x"}`)
	x.UpdateResult(ToolResult{Text: strings.Join(lines, "\n")}, false)
	if got := plain(x.Render(60)); !strings.Contains(got, "... (15 more lines, ctrl+o to expand)") {
		t.Fatalf("folded:\n%s", got)
	}
	x.SetExpanded(true)
	if got := plain(x.Render(60)); strings.Contains(got, "more lines") || strings.Count(got, "line") != 30 {
		t.Fatalf("expanded:\n%s", got)
	}
}

func TestKeyHintsNameTheBoundKeys(t *testing.T) {
	env := goldenEnv(t)
	if got := env.KeyText("app.message.followUp"); got != "option+enter" {
		t.Fatalf("on a Mac: %q", got)
	}
	env.GOOS = "linux"
	if got := env.KeyText("app.message.followUp"); got != "alt+enter" {
		t.Fatalf("elsewhere: %q", got)
	}
	env.Keys = NewKeybindings("linux", map[string][]string{"app.tools.expand": {"ctrl+e", "f2"}})
	if got := env.KeyText("app.tools.expand"); got != "ctrl+e/f2" {
		t.Fatalf("rebound: %q", got)
	}
}
