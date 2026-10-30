package interactive

import (
	"encoding/json"
	"fmt"
	"os"
)

// KeybindingsFileName is the user's key file, in the agent directory — Pi's
// `keybindings.json`.
const KeybindingsFileName = "keybindings.json"

// legacyKeybindingNames are the action ids older files used, before each was
// put under its namespace — Pi's KEYBINDING_NAME_MIGRATIONS.
var legacyKeybindingNames = map[string]string{
	"cursorUp": "tui.editor.cursorUp", "cursorDown": "tui.editor.cursorDown",
	"cursorLeft": "tui.editor.cursorLeft", "cursorRight": "tui.editor.cursorRight",
	"cursorWordLeft": "tui.editor.cursorWordLeft", "cursorWordRight": "tui.editor.cursorWordRight",
	"cursorLineStart": "tui.editor.cursorLineStart", "cursorLineEnd": "tui.editor.cursorLineEnd",
	"jumpForward": "tui.editor.jumpForward", "jumpBackward": "tui.editor.jumpBackward",
	"pageUp": "tui.editor.pageUp", "pageDown": "tui.editor.pageDown",
	"deleteCharBackward": "tui.editor.deleteCharBackward", "deleteCharForward": "tui.editor.deleteCharForward",
	"deleteWordBackward": "tui.editor.deleteWordBackward", "deleteWordForward": "tui.editor.deleteWordForward",
	"deleteToLineStart": "tui.editor.deleteToLineStart", "deleteToLineEnd": "tui.editor.deleteToLineEnd",
	"yank": "tui.editor.yank", "yankPop": "tui.editor.yankPop", "undo": "tui.editor.undo",
	"newLine": "tui.input.newLine", "submit": "tui.input.submit", "tab": "tui.input.tab", "copy": "tui.input.copy",
	"selectUp": "tui.select.up", "selectDown": "tui.select.down",
	"selectPageUp": "tui.select.pageUp", "selectPageDown": "tui.select.pageDown",
	"selectConfirm": "tui.select.confirm", "selectCancel": "tui.select.cancel",
	"interrupt": "app.interrupt", "clear": "app.clear", "exit": "app.exit", "suspend": "app.suspend",
	"cycleThinkingLevel": "app.thinking.cycle", "cycleModelForward": "app.model.cycleForward",
	"cycleModelBackward": "app.model.cycleBackward", "selectModel": "app.model.select",
	"expandTools": "app.tools.expand", "toggleThinking": "app.thinking.toggle",
	"toggleSessionNamedFilter": "app.session.toggleNamedFilter", "externalEditor": "app.editor.external",
	"followUp": "app.message.followUp", "dequeue": "app.message.dequeue", "pasteImage": "app.clipboard.pasteImage",
	"newSession": "app.session.new", "tree": "app.session.tree", "fork": "app.session.fork", "resume": "app.session.resume",
	"treeFoldOrUp": "app.tree.foldOrUp", "treeUnfoldOrDown": "app.tree.unfoldOrDown",
	"treeEditLabel": "app.tree.editLabel", "treeToggleLabelTimestamp": "app.tree.toggleLabelTimestamp",
	"toggleSessionPath": "app.session.togglePath", "toggleSessionSort": "app.session.toggleSort",
	"renameSession": "app.session.rename", "deleteSession": "app.session.delete",
	"deleteSessionNoninvasive": "app.session.deleteNoninvasive",
}

// LoadKeybindingsFile reads the user's bindings: an object of action id to a
// key or a list of keys. A missing file is no bindings. Entries of any other
// shape are left out, as Pi leaves them; an older file's ids are migrated to
// their namespaced names, and where a file has both the old and the new, the
// new one stands. A file that is not JSON is an error, where Pi quietly uses
// the defaults: a user who edited the file and sees nothing change has been
// told nothing.
func LoadKeybindingsFile(path string) (map[string][]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("keybindings: %w", err)
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("keybindings: %s: %w", path, err)
	}
	bindings := map[string][]string{}
	for id, value := range parsed {
		var keys []string
		var one string
		switch {
		case json.Unmarshal(value, &one) == nil:
			keys = []string{one}
		case json.Unmarshal(value, &keys) == nil:
		default:
			continue
		}
		if current, legacy := legacyKeybindingNames[id]; legacy {
			if _, bothNamed := parsed[current]; bothNamed {
				continue
			}
			id = current
		}
		bindings[id] = keys
	}
	return bindings, nil
}
