package interactive

import (
	"runtime"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/tui"
)

// AppKeybindings are the interactive mode's own actions with Pi's defaults
// and ids (KEYBINDINGS, core/keybindings.ts at the pin), in Pi's order. The
// few that differ by platform differ as Pi's do.
func AppKeybindings(goos string) []tui.KeybindingDefinition {
	suspend := []string{"ctrl+z"}
	pasteImage := []string{"ctrl+v"}
	if goos == "windows" {
		suspend, pasteImage = nil, []string{"alt+v"}
	}
	foldUp, unfoldDown := []string{"ctrl+left", "alt+left"}, []string{"ctrl+right", "alt+right"}
	if goos == "darwin" {
		foldUp, unfoldDown = []string{"alt+left", "ctrl+left"}, []string{"alt+right", "ctrl+right"}
	}
	k := func(keys ...string) []string { return keys }
	return []tui.KeybindingDefinition{
		{ID: "app.interrupt", DefaultKeys: k("escape"), Description: "Cancel or abort"},
		{ID: "app.clear", DefaultKeys: k("ctrl+c"), Description: "Clear editor"},
		{ID: "app.exit", DefaultKeys: k("ctrl+d"), Description: "Exit when editor is empty"},
		{ID: "app.suspend", DefaultKeys: suspend, Description: "Suspend to background"},
		{ID: "app.thinking.cycle", DefaultKeys: k("shift+tab"), Description: "Cycle thinking level"},
		{ID: "app.model.cycleForward", DefaultKeys: k("ctrl+p"), Description: "Cycle to next model"},
		{ID: "app.model.cycleBackward", DefaultKeys: k("shift+ctrl+p"), Description: "Cycle to previous model"},
		{ID: "app.model.select", DefaultKeys: k("ctrl+l"), Description: "Open model selector"},
		{ID: "app.tools.expand", DefaultKeys: k("ctrl+o"), Description: "Toggle tool output"},
		{ID: "app.thinking.toggle", DefaultKeys: k("ctrl+t"), Description: "Toggle thinking blocks"},
		{ID: "app.session.toggleNamedFilter", DefaultKeys: k("ctrl+n"), Description: "Toggle named session filter"},
		{ID: "app.editor.external", DefaultKeys: k("ctrl+g"), Description: "Open external editor"},
		{ID: "app.message.copy", DefaultKeys: k("ctrl+x"), Description: "Copy message to clipboard"},
		{ID: "app.message.followUp", DefaultKeys: k("alt+enter"), Description: "Queue follow-up message"},
		{ID: "app.message.dequeue", DefaultKeys: k("alt+up"), Description: "Restore queued messages"},
		{ID: "app.clipboard.pasteImage", DefaultKeys: pasteImage, Description: "Paste image from clipboard (text fallback)"},
		{ID: "app.session.new", Description: "Start a new session"},
		{ID: "app.session.tree", Description: "Open session tree"},
		{ID: "app.session.fork", Description: "Fork current session"},
		{ID: "app.session.resume", Description: "Resume a session"},
		{ID: "app.tree.foldOrUp", DefaultKeys: foldUp, Description: "Fold tree branch or move up"},
		{ID: "app.tree.unfoldOrDown", DefaultKeys: unfoldDown, Description: "Unfold tree branch or move down"},
		{ID: "app.tree.editLabel", DefaultKeys: k("shift+l"), Description: "Edit tree label"},
		{ID: "app.tree.toggleLabelTimestamp", DefaultKeys: k("shift+t"), Description: "Toggle tree label timestamps"},
		{ID: "app.session.togglePath", DefaultKeys: k("ctrl+p"), Description: "Toggle session path display"},
		{ID: "app.session.toggleSort", DefaultKeys: k("ctrl+s"), Description: "Toggle session sort mode"},
		{ID: "app.session.rename", DefaultKeys: k("ctrl+r"), Description: "Rename session"},
		{ID: "app.session.delete", DefaultKeys: k("ctrl+d"), Description: "Delete session"},
		{ID: "app.session.deleteNoninvasive", DefaultKeys: k("ctrl+backspace"), Description: "Delete session when query is empty"},
		{ID: "app.models.save", DefaultKeys: k("ctrl+s"), Description: "Save model selection"},
		{ID: "app.models.enableAll", DefaultKeys: k("ctrl+a"), Description: "Enable all models"},
		{ID: "app.models.clearAll", DefaultKeys: k("ctrl+x"), Description: "Clear all models"},
		{ID: "app.models.toggleProvider", DefaultKeys: k("ctrl+p"), Description: "Toggle all models for provider"},
		{ID: "app.models.reorderUp", DefaultKeys: k("alt+up"), Description: "Move model up in order"},
		{ID: "app.models.reorderDown", DefaultKeys: k("alt+down"), Description: "Move model down in order"},
		{ID: "app.tree.filter.default", DefaultKeys: k("ctrl+d"), Description: "Tree filter: default view"},
		{ID: "app.tree.filter.noTools", DefaultKeys: k("ctrl+t"), Description: "Tree filter: hide tool results"},
		{ID: "app.tree.filter.userOnly", DefaultKeys: k("ctrl+u"), Description: "Tree filter: user messages only"},
		{ID: "app.tree.filter.labeledOnly", DefaultKeys: k("ctrl+l"), Description: "Tree filter: labeled entries only"},
		{ID: "app.tree.filter.all", DefaultKeys: k("ctrl+a"), Description: "Tree filter: show all entries"},
		{ID: "app.tree.filter.cycleForward", DefaultKeys: k("ctrl+o"), Description: "Tree filter: cycle forward"},
		{ID: "app.tree.filter.cycleBackward", DefaultKeys: k("shift+ctrl+o"), Description: "Tree filter: cycle backward"},
	}
}

// NewKeybindings is every key the interactive mode answers to — the
// interface's and the mode's own — with the user's bindings applied.
func NewKeybindings(goos string, user map[string][]string) *tui.Keybindings {
	defs := append(append([]tui.KeybindingDefinition(nil), tui.TUIKeybindings...), AppKeybindings(goos)...)
	return tui.NewKeybindings(defs, user)
}

// Env is what every component draws with: the theme, the keys (for the
// hints that name them), the working directory paths are shown against,
// the user's home for shortening them, and the platform, which names the
// alt key "option" on a Mac.
type Env struct {
	Theme *Theme
	Keys  *tui.Keybindings
	Cwd   string
	Home  string
	GOOS  string
	// RequestRender asks for a frame when a component changes outside a
	// keystroke — an edit's preview arriving, a running command's timer.
	RequestRender func()
	// Do runs work on the interface's loop. With it, slow work — reading a
	// file for an edit's preview — happens on another goroutine and its
	// result is applied through Do; without it, the work runs inline.
	Do func(func())
}

// NewEnv is an Env for this machine.
func NewEnv(theme *Theme, keys *tui.Keybindings, cwd, home string) *Env {
	return &Env{Theme: theme, Keys: keys, Cwd: cwd, Home: home, GOOS: runtime.GOOS}
}

func (e *Env) requestRender() {
	if e.RequestRender != nil {
		e.RequestRender()
	}
}

// formatKeyText spells a key for a hint — "ctrl+o", "option+enter" on a
// Mac, alternatives joined with "/".
func (e *Env) formatKeyText(key string, capitalize bool) string {
	alts := strings.Split(key, "/")
	for i, alt := range alts {
		parts := strings.Split(alt, "+")
		for j, p := range parts {
			if e.GOOS == "darwin" && strings.ToLower(p) == "alt" {
				p = "option"
			}
			if capitalize && p != "" {
				p = strings.ToUpper(p[:1]) + p[1:]
			}
			parts[j] = p
		}
		alts[i] = strings.Join(parts, "+")
	}
	return strings.Join(alts, "/")
}

// KeyText is the keys bound to an action, as a hint shows them.
func (e *Env) KeyText(action string) string {
	keys := e.Keys.Keys(action)
	if len(keys) == 0 {
		return ""
	}
	return e.formatKeyText(strings.Join(keys, "/"), false)
}

// KeyHint is "ctrl+o to expand": the keys dim, the description muted.
func (e *Env) KeyHint(action, description string) string {
	return e.Theme.Fg("dim", e.KeyText(action)) + e.Theme.Fg("muted", " "+description)
}
