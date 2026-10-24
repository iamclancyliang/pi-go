package tui

import (
	"strconv"
	"strings"
)

// SelectItem is one choice in a SelectList.
type SelectItem struct {
	Value       string
	Label       string
	Description string
}

// SelectListTheme styles a SelectList.
type SelectListTheme struct {
	SelectedPrefix Style
	SelectedText   Style
	Description    Style
	ScrollInfo     Style
	NoMatch        Style
}

// SelectListTruncateContext is what a custom truncation of an item's primary
// text is given.
type SelectListTruncateContext struct {
	Text        string
	MaxWidth    int
	ColumnWidth int
	Item        SelectItem
	Selected    bool
}

// SelectListLayout bounds the primary column, which is as wide as the widest
// label within these bounds, so the descriptions line up.
type SelectListLayout struct {
	MinPrimaryColumnWidth int
	MaxPrimaryColumnWidth int
	TruncatePrimary       func(SelectListTruncateContext) string
}

const (
	defaultPrimaryColumnWidth = 32
	primaryColumnGap          = 2
	minDescriptionWidth       = 10
)

// SelectList is a scrolling list of choices with the selected one marked —
// Pi's SelectList. Up and down wrap around; enter selects; escape cancels.
type SelectList struct {
	items      []SelectItem
	filtered   []SelectItem
	selected   int
	maxVisible int
	theme      SelectListTheme
	layout     SelectListLayout

	// Keybindings are the keys the list answers to; nil is Pi's defaults.
	Keybindings       *Keybindings
	OnSelect          func(SelectItem)
	OnCancel          func()
	OnSelectionChange func(SelectItem)
}

// NewSelectList is a list showing at most maxVisible items at a time.
func NewSelectList(items []SelectItem, maxVisible int, theme SelectListTheme, layout SelectListLayout) *SelectList {
	return &SelectList{items: items, filtered: items, maxVisible: maxVisible, theme: theme, layout: layout}
}

// SetFilter keeps the items whose value starts with filter, ignoring case,
// and selects the first.
func (l *SelectList) SetFilter(filter string) {
	l.filtered = nil
	for _, item := range l.items {
		if strings.HasPrefix(strings.ToLower(item.Value), strings.ToLower(filter)) {
			l.filtered = append(l.filtered, item)
		}
	}
	l.selected = 0
}

// SetSelectedIndex selects an item, clamped to the list.
func (l *SelectList) SetSelectedIndex(index int) {
	l.selected = max(0, min(index, len(l.filtered)-1))
}

// SelectedItem is the selected item, if there is one.
func (l *SelectList) SelectedItem() (SelectItem, bool) {
	if l.selected < 0 || l.selected >= len(l.filtered) {
		return SelectItem{}, false
	}
	return l.filtered[l.selected], true
}

// Invalidate drops cached rendering; the list keeps none.
func (l *SelectList) Invalidate() {}

// Render draws the visible window of items, scrolled to keep the selection
// near its middle, with a position line when not everything fits.
func (l *SelectList) Render(width int) []string {
	if len(l.filtered) == 0 {
		return []string{l.theme.NoMatch.apply("  No matching commands")}
	}
	primary := l.primaryColumnWidth()
	start := max(0, min(l.selected-l.maxVisible/2, len(l.filtered)-l.maxVisible))
	end := min(start+l.maxVisible, len(l.filtered))

	var out []string
	for i := start; i < end; i++ {
		item := l.filtered[i]
		description := ""
		if item.Description != "" {
			description = singleLine(item.Description)
		}
		out = append(out, l.renderItem(item, i == l.selected, width, description, primary))
	}
	if start > 0 || end < len(l.filtered) {
		scroll := "  (" + strconv.Itoa(l.selected+1) + "/" + strconv.Itoa(len(l.filtered)) + ")"
		out = append(out, l.theme.ScrollInfo.apply(TruncateToWidth(scroll, width-2, "", false)))
	}
	return out
}

func singleLine(s string) string {
	return strings.TrimSpace(strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' }), " "))
}

func (l *SelectList) keys() *Keybindings {
	if l.Keybindings == nil {
		l.Keybindings = NewKeybindings(TUIKeybindings, nil)
	}
	return l.Keybindings
}

// HandleKey moves the selection, selects or cancels.
func (l *SelectList) HandleKey(k Key) {
	kb := l.keys()
	switch {
	case kb.Matches(k, "tui.select.up"):
		if l.selected == 0 {
			l.selected = len(l.filtered) - 1
		} else {
			l.selected--
		}
		l.selectionChanged()
	case kb.Matches(k, "tui.select.down"):
		if l.selected == len(l.filtered)-1 {
			l.selected = 0
		} else {
			l.selected++
		}
		l.selectionChanged()
	case kb.Matches(k, "tui.select.confirm"):
		if item, ok := l.SelectedItem(); ok && l.OnSelect != nil {
			l.OnSelect(item)
		}
	case kb.Matches(k, "tui.select.cancel"):
		if l.OnCancel != nil {
			l.OnCancel()
		}
	}
}

func (l *SelectList) selectionChanged() {
	if item, ok := l.SelectedItem(); ok && l.OnSelectionChange != nil {
		l.OnSelectionChange(item)
	}
}

func (l *SelectList) renderItem(item SelectItem, selected bool, width int, description string, primaryWidth int) string {
	prefix := "  "
	if selected {
		prefix = "→ "
	}
	prefixWidth := VisibleWidth(prefix)

	if description != "" && width > 40 {
		column := max(1, min(primaryWidth, width-prefixWidth-4))
		value := l.truncatePrimary(item, selected, max(1, column-primaryColumnGap), column)
		valueWidth := VisibleWidth(value)
		spacing := strings.Repeat(" ", max(1, column-valueWidth))
		remaining := width - (prefixWidth + valueWidth + len(spacing)) - 2
		if remaining > minDescriptionWidth {
			desc := TruncateToWidth(description, remaining, "", false)
			if selected {
				return l.theme.SelectedText.apply(prefix + value + spacing + desc)
			}
			return prefix + value + l.theme.Description.apply(spacing+desc)
		}
	}

	maxWidth := width - prefixWidth - 2
	value := l.truncatePrimary(item, selected, maxWidth, maxWidth)
	if selected {
		return l.theme.SelectedText.apply(prefix + value)
	}
	return prefix + value
}

func (l *SelectList) primaryColumnWidth() int {
	lo, hi := l.primaryColumnBounds()
	widest := 0
	for _, item := range l.filtered {
		widest = max(widest, VisibleWidth(displayValue(item))+primaryColumnGap)
	}
	return max(lo, min(widest, hi))
}

func (l *SelectList) primaryColumnBounds() (int, int) {
	rawMin, rawMax := l.layout.MinPrimaryColumnWidth, l.layout.MaxPrimaryColumnWidth
	if rawMin == 0 {
		rawMin = rawMax
	}
	if rawMax == 0 {
		rawMax = rawMin
	}
	if rawMin == 0 {
		rawMin, rawMax = defaultPrimaryColumnWidth, defaultPrimaryColumnWidth
	}
	return max(1, min(rawMin, rawMax)), max(1, max(rawMin, rawMax))
}

func (l *SelectList) truncatePrimary(item SelectItem, selected bool, maxWidth, columnWidth int) string {
	text := displayValue(item)
	if l.layout.TruncatePrimary != nil {
		text = l.layout.TruncatePrimary(SelectListTruncateContext{
			Text: text, MaxWidth: maxWidth, ColumnWidth: columnWidth, Item: item, Selected: selected,
		})
	} else {
		text = TruncateToWidth(text, maxWidth, "", false)
	}
	return TruncateToWidth(text, maxWidth, "", false)
}

func displayValue(item SelectItem) string {
	if item.Label != "" {
		return item.Label
	}
	return item.Value
}
