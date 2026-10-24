package tui

import (
	"reflect"
	"testing"
)

// TestTheTableIsPisFortySeven, counted by action, unbound ones included.
func TestTheTableIsPisFortySeven(t *testing.T) {
	if len(TUIKeybindings) != 47 {
		t.Fatalf("%d actions, want 47", len(TUIKeybindings))
	}
	kb := NewKeybindings(TUIKeybindings, nil)
	if keys := kb.Keys("tui.editor.historyPrevious"); len(keys) != 0 {
		t.Fatalf("history ships unbound in Pi, bound here to %v", keys)
	}
	if keys := kb.Keys("tui.editor.cursorLineStart"); !reflect.DeepEqual(keys, []string{"home", "ctrl+home", "ctrl+a"}) {
		t.Fatalf("line start is %v", keys)
	}
}

// TestAUserBindingReplacesTheDefaults rather than adding to them, and an
// empty list unbinds — Pi's rule.
func TestAUserBindingReplacesTheDefaults(t *testing.T) {
	kb := NewKeybindings(TUIKeybindings, map[string][]string{
		"tui.editor.cursorLeft":      {"alt+h"},
		"tui.altScreen.pageUp":       {},
		"tui.no.such.action":         {"ctrl+q"},
		"tui.editor.historyPrevious": {"ctrl+p"},
	})
	left := Key{Name: "left"}
	if kb.Matches(left, "tui.editor.cursorLeft") || !kb.Matches(Key{Name: "alt+h"}, "tui.editor.cursorLeft") {
		t.Fatal("the user's binding did not replace the defaults")
	}
	if len(kb.Keys("tui.altScreen.pageUp")) != 0 {
		t.Fatal("an empty list did not unbind")
	}
	if !kb.Matches(Key{Name: "ctrl+p"}, "tui.editor.historyPrevious") {
		t.Fatal("an unbound action could not be bound")
	}
}

// TestAKeyClaimedTwiceIsAConflict.
func TestAKeyClaimedTwiceIsAConflict(t *testing.T) {
	kb := NewKeybindings(TUIKeybindings, map[string][]string{
		"tui.editor.yank": {"ctrl+q"}, "tui.editor.undo": {"ctrl+q", "ctrl+z"},
	})
	want := []KeybindingConflict{{Key: "ctrl+q", Actions: []string{"tui.editor.undo", "tui.editor.yank"}}}
	if got := kb.Conflicts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("conflicts %+v", got)
	}
}

// TestMatchingIgnoresModifierOrderAndCase, and reaches every name a key is
// known by, as Pi's matcher does.
func TestMatchingIgnoresModifierOrderAndCase(t *testing.T) {
	cases := []struct {
		key     Key
		binding string
		want    bool
	}{
		{Key{Name: "shift+ctrl+p"}, "ctrl+shift+p", true},
		{Key{Name: "shift+ctrl+p"}, "Shift+Ctrl+P", true},
		{Key{Name: "pageUp"}, "pageup", true},
		{Key{Name: "escape"}, "esc", true},
		{Key{Name: "alt+b", Also: []string{"alt+left"}}, "alt+left", true},
		{Key{Text: 'L'}, "shift+l", true},
		{Key{Text: ' '}, "space", true},
		{Key{Text: 'a'}, "a", true},
		{Key{Name: "ctrl+p"}, "shift+ctrl+p", false},
		{Key{Name: "up", Release: true}, "up", false},
	}
	for _, c := range cases {
		if got := KeyMatches(c.key, c.binding); got != c.want {
			t.Errorf("KeyMatches(%+v, %q) = %v", c.key, c.binding, got)
		}
	}
}
