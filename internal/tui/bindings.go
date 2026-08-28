package tui

// Binding ties keys to an action, with the words /hotkeys shows.
type Binding struct {
	Action      string
	Keys        []string
	Description string
}

// DefaultBindings are Pi's, for the actions this slice implements.
//
// The assignments are kept exactly — a user coming from Pi has these in their
// fingers, and an editor that moves ctrl+w is an editor that eats a word the
// user meant to keep. Actions Pi has that this build does not (jump-to-
// character, page scrolling, the app-level chords) are absent rather than bound
// to nothing: a listed key that does nothing teaches the user it is broken.
var DefaultBindings = []Binding{
	{Action: "editor.cursorUp", Keys: []string{"up"}, Description: "Move cursor up, or back through history"},
	{Action: "editor.cursorDown", Keys: []string{"down"}, Description: "Move cursor down, or forward through history"},
	{Action: "editor.cursorLeft", Keys: []string{"left", "ctrl+b"}, Description: "Move cursor left"},
	{Action: "editor.cursorRight", Keys: []string{"right", "ctrl+f"}, Description: "Move cursor right"},
	{Action: "editor.cursorWordLeft", Keys: []string{"alt+left", "ctrl+left", "alt+b"}, Description: "Move cursor word left"},
	{Action: "editor.cursorWordRight", Keys: []string{"alt+right", "ctrl+right", "alt+f"}, Description: "Move cursor word right"},
	{Action: "editor.cursorLineStart", Keys: []string{"home", "ctrl+a"}, Description: "Move to line start"},
	{Action: "editor.cursorLineEnd", Keys: []string{"end", "ctrl+e"}, Description: "Move to line end"},
	{Action: "editor.deleteCharBackward", Keys: []string{"backspace"}, Description: "Delete character backward"},
	{Action: "editor.deleteCharForward", Keys: []string{"delete", "ctrl+d"}, Description: "Delete character forward"},
	{Action: "editor.deleteWordBackward", Keys: []string{"ctrl+w", "alt+backspace"}, Description: "Delete word backward"},
	{Action: "editor.deleteWordForward", Keys: []string{"alt+d", "alt+delete"}, Description: "Delete word forward"},
	{Action: "editor.deleteToLineStart", Keys: []string{"ctrl+u"}, Description: "Delete to line start"},
	{Action: "editor.deleteToLineEnd", Keys: []string{"ctrl+k"}, Description: "Delete to line end"},
	{Action: "editor.yank", Keys: []string{"ctrl+y"}, Description: "Paste the most-recently-deleted text"},
	{Action: "editor.yankPop", Keys: []string{"alt+y"}, Description: "Cycle through deleted text after pasting"},
	{Action: "editor.undo", Keys: []string{"ctrl+-"}, Description: "Undo"},
	{Action: "input.newLine", Keys: []string{"shift+enter", "ctrl+j"}, Description: "Insert a newline"},
	{Action: "input.submit", Keys: []string{"enter"}, Description: "Send the prompt"},
	{Action: "app.interrupt", Keys: []string{"ctrl+c"}, Description: "Stop the answer in progress; twice to exit"},
	{Action: "app.exit", Keys: []string{"ctrl+d"}, Description: "Exit, when the prompt is empty"},
}

// actionFor resolves one keystroke against the table.
func actionFor(k Key) string {
	if k.IsText() {
		return "insert"
	}
	for _, b := range DefaultBindings {
		for _, key := range b.Keys {
			if key == k.Name {
				return b.Action
			}
		}
	}
	return ""
}
