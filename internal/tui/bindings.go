package tui

// Binding ties keys to an action, with the words /hotkeys shows.
type Binding struct {
	Action      string
	Keys        []string
	Description string
}

// DefaultBindings are the keys the line prompt answers to: Pi's, under Pi's
// action ids, for the actions it implements.
//
// The assignments are kept exactly — a user coming from Pi has these in their
// fingers, and an editor that moves ctrl+w is an editor that eats a word the
// user meant to keep. The interface's full table is TUIKeybindings; this is
// the part of it the line prompt has, plus the two application keys it needs
// to stop and to leave.
var DefaultBindings = linePromptBindings()

// linePromptActions are the TUIKeybindings actions the line editor
// implements. Jumping to a character and paging need the full interface.
var linePromptActions = []string{
	"tui.editor.cursorUp", "tui.editor.cursorDown", "tui.editor.cursorLeft", "tui.editor.cursorRight",
	"tui.editor.cursorWordLeft", "tui.editor.cursorWordRight", "tui.editor.cursorLineStart",
	"tui.editor.cursorLineEnd", "tui.editor.deleteCharBackward", "tui.editor.deleteCharForward",
	"tui.editor.deleteWordBackward", "tui.editor.deleteWordForward", "tui.editor.deleteToLineStart",
	"tui.editor.deleteToLineEnd", "tui.editor.yank", "tui.editor.yankPop", "tui.editor.undo",
	"tui.input.newLine", "tui.input.submit",
}

func linePromptBindings() []Binding {
	defaults := NewKeybindings(TUIKeybindings, nil)
	var out []Binding
	for _, id := range linePromptActions {
		d, _ := defaults.Definition(id)
		out = append(out, Binding{Action: id, Keys: defaults.Keys(id), Description: d.Description})
	}
	return append(out,
		Binding{Action: "app.interrupt", Keys: []string{"ctrl+c"}, Description: "Stop the answer in progress; twice to exit"},
		Binding{Action: "app.exit", Keys: []string{"ctrl+d"}, Description: "Exit, when the prompt is empty"},
	)
}

// actionFor resolves one keystroke against the table.
func actionFor(k Key) string {
	if k.IsText() {
		return "insert"
	}
	for _, b := range DefaultBindings {
		for _, key := range b.Keys {
			if KeyMatches(k, key) {
				return b.Action
			}
		}
	}
	return ""
}
