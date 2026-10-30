package tui

import (
	"slices"
	"strconv"
	"strings"
)

// SettingItem is one row of a SettingsList.
type SettingItem struct {
	ID string
	// Label is the left side; CurrentValue the right.
	Label        string
	CurrentValue string
	// Description shows under the list while the item is selected.
	Description string
	// Values are what enter or space cycles through.
	Values []string
	// Submenu, when set, is what enter opens instead. It is given the current
	// value, and done with the value chosen or an empty ok for none.
	Submenu func(current string, done func(value string, ok bool)) Component
}

// SettingsListTheme styles a SettingsList.
type SettingsListTheme struct {
	Label       func(text string, selected bool) string
	Value       func(text string, selected bool) string
	Description Style
	Cursor      string
	Hint        Style
}

// SettingsList is a scrolling list of settings, aligned in two columns, with
// the selected one's description below — Pi's SettingsList.
type SettingsList struct {
	items      []*SettingItem
	filtered   []*SettingItem
	theme      SettingsListTheme
	selected   int
	maxVisible int
	onChange   func(id, value string)
	onCancel   func()
	search     *Input

	// Keybindings are the keys it answers to; nil is Pi's defaults.
	Keybindings *Keybindings

	submenu     Component
	submenuItem int
}

// NewSettingsList is a list showing at most maxVisible items, searchable when
// asked. The items are held by reference, so UpdateValue changes what they show.
func NewSettingsList(items []*SettingItem, maxVisible int, theme SettingsListTheme,
	onChange func(id, value string), onCancel func(), enableSearch bool) *SettingsList {

	l := &SettingsList{items: items, filtered: items, theme: theme, maxVisible: maxVisible,
		onChange: onChange, onCancel: onCancel, submenuItem: -1}
	if enableSearch {
		l.search = &Input{}
	}
	return l
}

func (l *SettingsList) keys() *Keybindings {
	if l.Keybindings == nil {
		l.Keybindings = NewKeybindings(TUIKeybindings, nil)
	}
	return l.Keybindings
}

// UpdateValue changes what an item shows.
func (l *SettingsList) UpdateValue(id, value string) {
	for _, item := range l.items {
		if item.ID == id {
			item.CurrentValue = value
			return
		}
	}
}

// SetFocused passes focus to the search, which marks the cursor.
func (l *SettingsList) SetFocused(focused bool) {
	if l.search != nil {
		l.search.SetFocused(focused)
	}
}

// Invalidate passes on to a submenu.
func (l *SettingsList) Invalidate() {
	if l.submenu != nil {
		l.submenu.Invalidate()
	}
}

// Render draws the submenu if one is open, otherwise the list.
func (l *SettingsList) Render(width int) []string {
	if l.submenu != nil {
		return l.submenu.Render(width)
	}
	var lines []string
	if l.search != nil {
		lines = append(lines, l.search.Render(width)...)
		lines = append(lines, "")
	}
	if len(l.items) == 0 {
		lines = append(lines, l.theme.Hint.apply("  No settings available"))
		if l.search != nil {
			lines = l.addHint(lines, width)
		}
		return lines
	}
	display := l.display()
	if len(display) == 0 {
		lines = append(lines, TruncateToWidth(l.theme.Hint.apply("  No matching settings"), width, "...", false))
		return l.addHint(lines, width)
	}

	start := max(0, min(l.selected-l.maxVisible/2, len(display)-l.maxVisible))
	end := min(start+l.maxVisible, len(display))

	// From every item rather than the filtered ones, so the value column stays
	// put as a search narrows the list.
	labelWidth := 0
	for _, item := range l.items {
		labelWidth = max(labelWidth, VisibleWidth(item.Label))
	}
	labelWidth = min(30, labelWidth)

	for i := start; i < end; i++ {
		item := display[i]
		selected := i == l.selected
		prefix := "  "
		if selected {
			prefix = l.theme.Cursor
		}
		padded := item.Label + strings.Repeat(" ", max(0, labelWidth-VisibleWidth(item.Label)))
		const separator = "  "
		used := VisibleWidth(prefix) + labelWidth + VisibleWidth(separator)
		value := l.theme.Value(TruncateToWidth(item.CurrentValue, width-used-2, "", false), selected)
		lines = append(lines, TruncateToWidth(prefix+l.theme.Label(padded, selected)+separator+value, width, "...", false))
	}
	if start > 0 || end < len(display) {
		scroll := "  (" + strconv.Itoa(l.selected+1) + "/" + strconv.Itoa(len(display)) + ")"
		lines = append(lines, l.theme.Hint.apply(TruncateToWidth(scroll, width-2, "", false)))
	}
	if l.selected < len(display) && display[l.selected].Description != "" {
		lines = append(lines, "")
		for _, line := range WrapText(display[l.selected].Description, width-4) {
			lines = append(lines, l.theme.Description.apply("  "+line))
		}
	}
	return l.addHint(lines, width)
}

func (l *SettingsList) display() []*SettingItem {
	if l.search != nil {
		return l.filtered
	}
	return l.items
}

func (l *SettingsList) addHint(lines []string, width int) []string {
	hint := "  Enter/Space to change · Esc to cancel"
	if l.search != nil {
		hint = "  Type to search · Enter/Space to change · Esc to cancel"
	}
	return append(lines, "", TruncateToWidth(l.theme.Hint.apply(hint), width, "...", false))
}

// HandleKey moves the selection, changes a value, or goes to a submenu or search.
func (l *SettingsList) HandleKey(k Key) {
	if l.submenu != nil {
		if handler, ok := l.submenu.(KeyHandler); ok {
			handler.HandleKey(k)
		}
		return
	}
	kb := l.keys()
	display := l.display()
	space := k.Text == ' ' && (l.search == nil || l.search.Value() == "")
	switch {
	case kb.Matches(k, "tui.select.up"):
		if len(display) == 0 {
			return
		}
		if l.selected == 0 {
			l.selected = len(display) - 1
		} else {
			l.selected--
		}
	case kb.Matches(k, "tui.select.down"):
		if len(display) == 0 {
			return
		}
		if l.selected == len(display)-1 {
			l.selected = 0
		} else {
			l.selected++
		}
	case kb.Matches(k, "tui.select.confirm"), space:
		l.activate(display)
	case kb.Matches(k, "tui.select.cancel"):
		l.onCancel()
	case l.search != nil:
		l.search.Keybindings = kb
		l.search.HandleKey(k)
		l.filtered = FuzzyFilter(l.items, l.search.Value(), func(item *SettingItem) string { return item.Label })
		l.selected = 0
	}
}

func (l *SettingsList) activate(display []*SettingItem) {
	if l.selected >= len(display) {
		return
	}
	item := display[l.selected]
	switch {
	case item.Submenu != nil:
		l.submenuItem = l.selected
		l.submenu = item.Submenu(item.CurrentValue, func(value string, ok bool) {
			if ok {
				item.CurrentValue = value
				l.onChange(item.ID, value)
			}
			l.closeSubmenu()
		})
	case len(item.Values) > 0:
		next := item.Values[(slices.Index(item.Values, item.CurrentValue)+1)%len(item.Values)]
		item.CurrentValue = next
		l.onChange(item.ID, next)
	}
}

func (l *SettingsList) closeSubmenu() {
	l.submenu = nil
	if l.submenuItem >= 0 {
		l.selected = l.submenuItem
		l.submenuItem = -1
	}
}
