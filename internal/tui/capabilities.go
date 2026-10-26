package tui

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// TerminalCapabilities are what the terminal is known to draw beyond plain
// styled text.
type TerminalCapabilities struct {
	TrueColor  bool
	Hyperlinks bool
}

var (
	capabilitiesMu     sync.Mutex
	cachedCapabilities *TerminalCapabilities
)

// Capabilities are the terminal's capabilities, detected once from the
// environment.
func Capabilities() TerminalCapabilities {
	capabilitiesMu.Lock()
	defer capabilitiesMu.Unlock()
	if cachedCapabilities == nil {
		c := DetectCapabilities(os.Getenv, probeTmuxHyperlinks)
		cachedCapabilities = &c
	}
	return *cachedCapabilities
}

// SetCapabilities overrides detection, for tests and for a user's setting.
func SetCapabilities(c TerminalCapabilities) {
	capabilitiesMu.Lock()
	defer capabilitiesMu.Unlock()
	cachedCapabilities = &c
}

// DetectCapabilities reads the terminal from its environment, as Pi's
// detectCapabilities does. Hyperlinks are claimed only for terminals known
// to draw them: a terminal that swallows OSC 8 shows the link text with the
// address gone, so an unknown terminal gets the address printed instead.
// Under tmux they depend on whether tmux forwards them, which tmuxForwards
// asks.
func DetectCapabilities(getenv func(string) string, tmuxForwards func() bool) TerminalCapabilities {
	termProgram := strings.ToLower(getenv("TERM_PROGRAM"))
	term := strings.ToLower(getenv("TERM"))
	colorTerm := strings.ToLower(getenv("COLORTERM"))
	trueColorHint := colorTerm == "truecolor" || colorTerm == "24bit"

	switch {
	case getenv("TMUX") != "" || strings.HasPrefix(term, "tmux"):
		return TerminalCapabilities{TrueColor: trueColorHint, Hyperlinks: tmuxForwards()}
	case strings.HasPrefix(term, "screen"):
		return TerminalCapabilities{TrueColor: trueColorHint}
	case getenv("KITTY_WINDOW_ID") != "" || termProgram == "kitty",
		termProgram == "ghostty" || strings.Contains(term, "ghostty") || getenv("GHOSTTY_RESOURCES_DIR") != "",
		getenv("WEZTERM_PANE") != "" || termProgram == "wezterm",
		termProgram == "warpterminal" || getenv("WARP_SESSION_ID") != "" || getenv("WARP_TERMINAL_SESSION_UUID") != "",
		getenv("ITERM_SESSION_ID") != "" || termProgram == "iterm.app",
		getenv("WT_SESSION") != "",
		termProgram == "vscode",
		termProgram == "alacritty":
		return TerminalCapabilities{TrueColor: true, Hyperlinks: true}
	case strings.ToLower(getenv("TERMINAL_EMULATOR")) == "jetbrains-jediterm", runtime.GOOS == "windows":
		return TerminalCapabilities{TrueColor: true}
	}
	return TerminalCapabilities{TrueColor: trueColorHint}
}

// probeTmuxHyperlinks asks the attached tmux client whether it forwards
// OSC 8 hyperlinks; any failure is a no.
func probeTmuxHyperlinks() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "#{client_termfeatures}").Output()
	if err != nil {
		return false
	}
	for _, feature := range strings.Split(string(out), ",") {
		if strings.TrimSpace(feature) == "hyperlinks" {
			return true
		}
	}
	return false
}

// Hyperlink wraps text in an OSC 8 hyperlink to url.
func Hyperlink(text, url string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}
