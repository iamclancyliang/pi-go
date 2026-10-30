package interactive

import (
	"strconv"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/tui"
)

// border is a rule across the width — Pi's DynamicBorder.
type border struct{ color tui.Style }

func (b *border) Invalidate() {}
func (b *border) Render(width int) []string {
	return []string{b.color(strings.Repeat("─", max(1, width)))}
}

func newBorder(env *Env) *border { return &border{color: env.Theme.FgStyle("border")} }

func spacer() *tui.Spacer { return &tui.Spacer{Lines: 1} }

// rawKeyHint is keyHint for a key that is not a binding.
func (e *Env) rawKeyHint(key, description string) string {
	return e.Theme.Fg("dim", e.formatKeyText(key, false)) + e.Theme.Fg("muted", " "+description)
}

// windowOf is the range of a list to show: at most limit items, with the selected
// one near the middle.
func windowOf(selected, length, limit int) (start, end int) {
	start = max(0, min(selected-limit/2, length-limit))
	return start, min(start+limit, length)
}

// position is the "(3/10)" a list shows when it does not all fit.
func position(selected, length int) string {
	return "  (" + strconv.Itoa(selected+1) + "/" + strconv.Itoa(length) + ")"
}

// wrapUp moves a selection one step up, from the top to the bottom.
func wrapUp(selected, length int) int {
	if selected == 0 {
		return length - 1
	}
	return selected - 1
}

// wrapDown moves a selection one step down, from the bottom to the top.
func wrapDown(selected, length int) int {
	if selected == length-1 {
		return 0
	}
	return selected + 1
}

// ChoiceDialog asks the user to pick one of a few options — Pi's
// ExtensionSelectorComponent, which the interactive mode also uses for its own
// questions.
type ChoiceDialog struct {
	tui.Container
	env      *Env
	options  []string
	selected int
	list     tui.Container
	onSelect func(string)
	onCancel func()
}

// NewChoiceDialog is a dialog titled title.
func NewChoiceDialog(env *Env, title string, options []string, onSelect func(string), onCancel func()) *ChoiceDialog {
	t := env.Theme
	d := &ChoiceDialog{env: env, options: options, onSelect: onSelect, onCancel: onCancel}
	d.AddChild(newBorder(env))
	d.AddChild(spacer())
	d.AddChild(tui.NewText(t.Fg("accent", Bold(title)), 1, 0, nil))
	d.AddChild(spacer())
	d.AddChild(&d.list)
	d.AddChild(spacer())
	d.AddChild(tui.NewText(env.rawKeyHint("↑↓", "navigate")+"  "+
		env.KeyHint("tui.select.confirm", "select")+"  "+
		env.KeyHint("tui.select.cancel", "cancel"), 1, 0, nil))
	d.AddChild(spacer())
	d.AddChild(newBorder(env))
	d.updateList()
	return d
}

func (d *ChoiceDialog) updateList() {
	t := d.env.Theme
	d.list.Clear()
	for i, option := range d.options {
		text := "  " + t.Fg("text", option)
		if i == d.selected {
			text = t.Fg("accent", "→ ") + t.Fg("accent", option)
		}
		d.list.AddChild(tui.NewText(text, 1, 0, nil))
	}
}

// HandleKey moves the selection, picks or cancels.
func (d *ChoiceDialog) HandleKey(k tui.Key) {
	kb := d.env.Keys
	switch {
	case kb.Matches(k, "tui.select.up"), k.Text == 'k':
		d.selected = max(0, d.selected-1)
		d.updateList()
	case kb.Matches(k, "tui.select.down"), k.Text == 'j':
		d.selected = min(len(d.options)-1, d.selected+1)
		d.updateList()
	case kb.Matches(k, "tui.select.confirm"):
		if d.selected < len(d.options) {
			d.onSelect(d.options[d.selected])
		}
	case kb.Matches(k, "tui.select.cancel"):
		d.onCancel()
	}
}

// EditorDialog asks for some text with the full editor — Pi's
// ExtensionEditorComponent, without its external editor key, which the interactive
// mode has yet to gain.
type EditorDialog struct {
	tui.Container
	env      *Env
	editor   *tui.Editor
	onCancel func()
}

// NewEditorDialog is a dialog titled title. host is what the editor sizes itself by.
func NewEditorDialog(env *Env, host tui.EditorHost, title string, onSubmit func(string), onCancel func()) *EditorDialog {
	t := env.Theme
	d := &EditorDialog{env: env, onCancel: onCancel}
	d.editor = tui.NewEditor(host, t.EditorTheme(), tui.EditorOptions{})
	d.editor.Keybindings = env.Keys
	d.editor.OnSubmit = onSubmit
	d.AddChild(newBorder(env))
	d.AddChild(spacer())
	d.AddChild(tui.NewText(t.Fg("accent", title), 1, 0, nil))
	d.AddChild(spacer())
	d.AddChild(d.editor)
	d.AddChild(spacer())
	d.AddChild(tui.NewText(env.KeyHint("tui.select.confirm", "submit")+"  "+
		env.KeyHint("tui.input.newLine", "newline")+"  "+
		env.KeyHint("tui.select.cancel", "cancel"), 1, 0, nil))
	d.AddChild(spacer())
	d.AddChild(newBorder(env))
	return d
}

// SetFocused passes focus to the editor.
func (d *EditorDialog) SetFocused(focused bool) { d.editor.SetFocused(focused) }

// HandleKey cancels, or edits.
func (d *EditorDialog) HandleKey(k tui.Key) {
	if d.env.Keys.Matches(k, "tui.select.cancel") {
		d.onCancel()
		return
	}
	d.editor.HandleKey(k)
}
