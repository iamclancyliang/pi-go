package interactive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/tui"
)

func TestFormatTokensIsPis(t *testing.T) {
	for n, want := range map[int]string{
		0: "0", 950: "950", 1234: "1.2k", 9999: "10.0k", 45000: "45k", 999499: "999k",
		1300000: "1.3M", 12000000: "12M",
	} {
		if got := formatTokens(n); got != want {
			t.Errorf("formatTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestFooterShowsWhereAndWhat(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := &Env{Theme: DarkTheme(true), Keys: NewKeybindings("linux", nil), Cwd: filepath.Join(dir, "sub"), Home: dir}
	f := NewFooter(env, func() FooterState {
		return FooterState{Cwd: filepath.Join(dir, "sub"), SessionName: "fix bug", Input: 1500, Output: 300,
			ContextTokens: 950, ContextKnown: true, ContextWindow: 1000, AutoCompact: true, Model: "m1", Thinking: "high"}
	})
	lines := f.Render(60)
	top, bottom := tui.StripSequences(lines[0]), tui.StripSequences(lines[1])
	if top != "~/sub (feature) • fix bug" {
		t.Fatalf("top line %q", top)
	}
	if !strings.HasPrefix(bottom, "↑1.5k ↓300 95.0%/1.0k (auto)") || !strings.HasSuffix(bottom, "m1 • high") || tui.VisibleWidth(lines[1]) != 60 {
		t.Fatalf("bottom line %q", bottom)
	}
	if !strings.Contains(lines[1], env.Theme.Fg("error", "95.0%/1.0k (auto)")) {
		t.Fatal("a nearly full context is not shown in the error colour")
	}
}

func TestFooterContextUnknownAfterCompaction(t *testing.T) {
	env := &Env{Theme: DarkTheme(true), Keys: NewKeybindings("linux", nil), Cwd: t.TempDir()}
	f := NewFooter(env, func() FooterState { return FooterState{Cwd: env.Cwd, ContextWindow: 128000, Model: "m"} })
	if got := tui.StripSequences(f.Render(60)[1]); !strings.HasPrefix(got, "?/128k") {
		t.Fatalf("bottom line %q", got)
	}
}
