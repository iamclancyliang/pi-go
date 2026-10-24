package tui

import (
	"sort"
	"strings"
)

// KeybindingDefinition is an action's default keys and what /hotkeys says it
// does.
type KeybindingDefinition struct {
	ID          string
	DefaultKeys []string
	Description string
}

// TUIKeybindings are the interface's own 47 actions with Pi's defaults and
// ids (TUI_KEYBINDINGS, keybindings.ts at the pin), in Pi's order. An action
// that ships unbound — the dedicated history keys, the finer fullscreen
// scrolls — is listed with no keys rather than left out, because a user can
// still bind it.
var TUIKeybindings = []KeybindingDefinition{
	{"tui.editor.cursorUp", []string{"up"}, "Move cursor up"},
	{"tui.editor.cursorDown", []string{"down"}, "Move cursor down"},
	{"tui.editor.historyPrevious", nil, "Select previous prompt history entry"},
	{"tui.editor.historyNext", nil, "Select next prompt history entry"},
	{"tui.editor.cursorLeft", []string{"left", "ctrl+b"}, "Move cursor left"},
	{"tui.editor.cursorRight", []string{"right", "ctrl+f"}, "Move cursor right"},
	{"tui.editor.cursorWordLeft", []string{"alt+left", "ctrl+left", "alt+b"}, "Move cursor word left"},
	{"tui.editor.cursorWordRight", []string{"alt+right", "ctrl+right", "alt+f"}, "Move cursor word right"},
	{"tui.editor.cursorLineStart", []string{"home", "ctrl+home", "ctrl+a"}, "Move to line start"},
	{"tui.editor.cursorLineEnd", []string{"end", "ctrl+end", "ctrl+e"}, "Move to line end"},
	{"tui.editor.jumpForward", []string{"ctrl+]"}, "Jump forward to character"},
	{"tui.editor.jumpBackward", []string{"ctrl+alt+]"}, "Jump backward to character"},
	{"tui.editor.pageUp", []string{"pageUp", "ctrl+pageUp"}, "Page up"},
	{"tui.editor.pageDown", []string{"pageDown", "ctrl+pageDown"}, "Page down"},
	{"tui.editor.deleteCharBackward", []string{"backspace"}, "Delete character backward"},
	{"tui.editor.deleteCharForward", []string{"delete", "ctrl+d"}, "Delete character forward"},
	{"tui.editor.deleteWordBackward", []string{"ctrl+w", "alt+backspace"}, "Delete word backward"},
	{"tui.editor.deleteWordForward", []string{"alt+d", "alt+delete"}, "Delete word forward"},
	{"tui.editor.deleteToLineStart", []string{"ctrl+u"}, "Delete to line start"},
	{"tui.editor.deleteToLineEnd", []string{"ctrl+k"}, "Delete to line end"},
	{"tui.editor.yank", []string{"ctrl+y"}, "Yank"},
	{"tui.editor.yankPop", []string{"alt+y"}, "Yank pop"},
	{"tui.editor.undo", []string{"ctrl+-"}, "Undo"},
	{"tui.input.newLine", []string{"shift+enter", "ctrl+j"}, "Insert newline"},
	{"tui.input.submit", []string{"enter"}, "Submit input"},
	{"tui.input.tab", []string{"tab"}, "Tab / autocomplete"},
	{"tui.input.copy", []string{"ctrl+c"}, "Copy selection"},
	{"tui.select.up", []string{"up"}, "Move selection up"},
	{"tui.select.down", []string{"down"}, "Move selection down"},
	{"tui.select.pageUp", []string{"pageUp"}, "Selection page up"},
	{"tui.select.pageDown", []string{"pageDown"}, "Selection page down"},
	{"tui.select.confirm", []string{"enter"}, "Confirm selection"},
	{"tui.select.cancel", []string{"escape", "ctrl+c"}, "Cancel selection"},
	{"tui.altScreen.pageUp", []string{"pageUp"}, "Scroll viewport up one page"},
	{"tui.altScreen.pageDown", []string{"pageDown"}, "Scroll viewport down one page"},
	{"tui.altScreen.halfPageUp", nil, "Scroll viewport up half a page"},
	{"tui.altScreen.halfPageDown", nil, "Scroll viewport down half a page"},
	{"tui.altScreen.lineUp", nil, "Scroll viewport up one line"},
	{"tui.altScreen.lineDown", nil, "Scroll viewport down one line"},
	{"tui.altScreen.previousPrompt", []string{"ctrl+shift+up"}, "Jump to previous semantic prompt"},
	{"tui.altScreen.nextPrompt", []string{"ctrl+shift+down"}, "Jump to next semantic prompt"},
	{"tui.altScreen.search", []string{"ctrl+shift+f"}, "Search the primary scroll view"},
	{"tui.altScreen.searchNext", []string{"enter", "ctrl+g"}, "Select the next search match"},
	{"tui.altScreen.searchPrevious", []string{"shift+enter", "ctrl+shift+g"}, "Select the previous search match"},
	{"tui.altScreen.searchClose", []string{"escape"}, "Close transcript search"},
	{"tui.altScreen.top", []string{"home"}, "Scroll viewport to top"},
	{"tui.altScreen.bottom", []string{"end"}, "Scroll viewport to bottom"},
}

// KeybindingConflict is one key the user bound to more than one action.
type KeybindingConflict struct {
	Key     string
	Actions []string
}

// Keybindings resolves actions to keys: each action's defaults, or the
// user's keys for it when the user gave any — which replace the defaults
// rather than adding to them, as in Pi (KeybindingsManager). An action the
// user bound to nothing, an empty list, is unbound.
type Keybindings struct {
	definitions []KeybindingDefinition
	byID        map[string]KeybindingDefinition
	keys        map[string][]string
	conflicts   []KeybindingConflict
}

// NewKeybindings resolves definitions against the user's bindings. A user
// binding for an action no definition names is ignored, as Pi ignores it.
func NewKeybindings(definitions []KeybindingDefinition, user map[string][]string) *Keybindings {
	kb := &Keybindings{definitions: definitions, byID: map[string]KeybindingDefinition{}, keys: map[string][]string{}}
	for _, d := range definitions {
		kb.byID[d.ID] = d
	}

	claims := map[string][]string{}
	for id, keys := range user {
		if _, ok := kb.byID[id]; !ok {
			continue
		}
		for _, k := range dedupe(keys) {
			claims[k] = append(claims[k], id)
		}
	}
	for key, ids := range claims {
		if len(ids) > 1 {
			sort.Strings(ids)
			kb.conflicts = append(kb.conflicts, KeybindingConflict{Key: key, Actions: ids})
		}
	}
	sort.Slice(kb.conflicts, func(i, j int) bool { return kb.conflicts[i].Key < kb.conflicts[j].Key })

	for _, d := range definitions {
		if keys, ok := user[d.ID]; ok {
			kb.keys[d.ID] = dedupe(keys)
		} else {
			kb.keys[d.ID] = dedupe(d.DefaultKeys)
		}
	}
	return kb
}

func dedupe(keys []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// Keys is what an action is bound to now.
func (kb *Keybindings) Keys(id string) []string { return append([]string(nil), kb.keys[id]...) }

// Definition is an action's definition.
func (kb *Keybindings) Definition(id string) (KeybindingDefinition, bool) {
	d, ok := kb.byID[id]
	return d, ok
}

// Definitions are every action, in order.
func (kb *Keybindings) Definitions() []KeybindingDefinition {
	return append([]KeybindingDefinition(nil), kb.definitions...)
}

// Conflicts are the keys the user bound to more than one action.
func (kb *Keybindings) Conflicts() []KeybindingConflict {
	return append([]KeybindingConflict(nil), kb.conflicts...)
}

// Matches reports whether a keystroke is bound to the action.
func (kb *Keybindings) Matches(k Key, id string) bool {
	for _, bound := range kb.keys[id] {
		if KeyMatches(k, bound) {
			return true
		}
	}
	return false
}

// KeyMatches reports whether a keystroke is the key a binding names. The
// binding may name its modifiers in any order and any case — "ctrl+shift+p"
// is "shift+ctrl+p" — as Pi's matcher allows; the keystroke matches under
// its name or any of the other names its bytes are known by. A typed
// character matches the binding for its key: "a" for a, "shift+a" for A,
// "space" for a space.
func KeyMatches(k Key, binding string) bool {
	want, ok := parseKeyID(binding)
	if !ok || k.Release {
		return false
	}
	for _, name := range keyNames(k) {
		if got, ok := parseKeyID(name); ok && got == want {
			return true
		}
	}
	return false
}

func keyNames(k Key) []string {
	if k.IsText() {
		switch r := k.Text; {
		case r == ' ':
			return []string{"space"}
		case r >= 'A' && r <= 'Z':
			return []string{"shift+" + string(r+32)}
		default:
			return []string{string(r)}
		}
	}
	return append([]string{k.Name}, k.Also...)
}

type keyID struct {
	key                     string
	shift, ctrl, alt, super bool
}

// canonicalKeys maps every spelling Pi accepts to one key name.
var canonicalKeys = map[string]string{
	"esc": "escape", "return": "enter", "pageup": "pageUp", "pagedown": "pageDown",
}

func parseKeyID(s string) (keyID, bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(s)), "+")
	key := parts[len(parts)-1]
	if key == "" {
		// "ctrl++" names the plus key.
		if len(parts) >= 2 && parts[len(parts)-2] == "" {
			key, parts = "+", parts[:len(parts)-1]
		} else {
			return keyID{}, false
		}
	}
	if c, ok := canonicalKeys[key]; ok {
		key = c
	}
	id := keyID{key: key}
	for _, mod := range parts[:len(parts)-1] {
		switch mod {
		case "shift":
			id.shift = true
		case "ctrl":
			id.ctrl = true
		case "alt":
			id.alt = true
		case "super":
			id.super = true
		case "":
		default:
			return keyID{}, false
		}
	}
	return id, true
}
