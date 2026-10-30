package tui

import (
	"strings"
)

// Input is a single line of text with horizontal scrolling — Pi's Input. It is
// what a selector searches with and what a dialog asks through. Kill and yank
// and undo work as they do in the editor.
//
// Columns are byte offsets, moved by whole grapheme clusters.
type Input struct {
	value  string
	cursor int

	// Keybindings are the keys it answers to; nil is Pi's defaults.
	Keybindings *Keybindings
	OnSubmit    func(string)
	OnEscape    func()

	// Mask draws every character as this one, for what must not be left on the
	// screen or in the scrollback: a credential being typed. Pi has no such
	// option because its login shows the key; see the login dialog.
	Mask string

	focused    bool
	ring       killRing
	lastAction editAction
	undoStack  []inputState
}

type inputState struct {
	value  string
	cursor int
}

// Value is the text.
func (i *Input) Value() string { return i.value }

// SetValue replaces the text, keeping the cursor where it was if it still fits.
func (i *Input) SetValue(value string) {
	i.value = value
	i.cursor = min(i.cursor, len(value))
}

// SetFocused says whether it holds the focus, which is when it marks the
// hardware cursor's place.
func (i *Input) SetFocused(focused bool) { i.focused = focused }

// Invalidate drops cached rendering; it keeps none.
func (i *Input) Invalidate() {}

func (i *Input) keys() *Keybindings {
	if i.Keybindings == nil {
		i.Keybindings = NewKeybindings(TUIKeybindings, nil)
	}
	return i.Keybindings
}

// HandleKey edits the text, or submits or cancels.
func (i *Input) HandleKey(k Key) {
	if k.Name == "paste" {
		i.handlePaste(k.Paste)
		return
	}
	kb := i.keys()
	switch {
	case kb.Matches(k, "tui.select.cancel"):
		if i.OnEscape != nil {
			i.OnEscape()
		}
	case kb.Matches(k, "tui.editor.undo"):
		i.undo()
	case kb.Matches(k, "tui.input.submit"), k.Name == "ctrl+j":
		if i.OnSubmit != nil {
			i.OnSubmit(i.value)
		}
	case kb.Matches(k, "tui.editor.deleteCharBackward"):
		i.handleBackspace()
	case kb.Matches(k, "tui.editor.deleteCharForward"):
		i.handleForwardDelete()
	case kb.Matches(k, "tui.editor.deleteWordBackward"):
		i.deleteWordBackward()
	case kb.Matches(k, "tui.editor.deleteWordForward"):
		i.deleteWordForward()
	case kb.Matches(k, "tui.editor.deleteToLineStart"):
		i.deleteToLineStart()
	case kb.Matches(k, "tui.editor.deleteToLineEnd"):
		i.deleteToLineEnd()
	case kb.Matches(k, "tui.editor.yank"):
		i.yank()
	case kb.Matches(k, "tui.editor.yankPop"):
		i.yankPop()
	case kb.Matches(k, "tui.editor.cursorLeft"):
		i.lastAction = actionNone
		if i.cursor > 0 {
			segments := graphemeSegments(i.value[:i.cursor])
			i.cursor -= len(segments[len(segments)-1].text)
		}
	case kb.Matches(k, "tui.editor.cursorRight"):
		i.lastAction = actionNone
		if i.cursor < len(i.value) {
			i.cursor += len(graphemeSegments(i.value[i.cursor:])[0].text)
		}
	case kb.Matches(k, "tui.editor.cursorLineStart"):
		i.lastAction = actionNone
		i.cursor = 0
	case kb.Matches(k, "tui.editor.cursorLineEnd"):
		i.lastAction = actionNone
		i.cursor = len(i.value)
	case kb.Matches(k, "tui.editor.cursorWordLeft"):
		i.moveWordBackward()
	case kb.Matches(k, "tui.editor.cursorWordRight"):
		i.moveWordForward()
	case k.IsText():
		i.insert(string(k.Text))
	}
}

func (i *Input) insert(char string) {
	// Consecutive word characters undo together; a space starts a new step.
	if isWhitespace(char) || i.lastAction != actionTypeWord {
		i.pushUndo()
	}
	i.lastAction = actionTypeWord
	i.value = i.value[:i.cursor] + char + i.value[i.cursor:]
	i.cursor += len(char)
}

func (i *Input) handleBackspace() {
	i.lastAction = actionNone
	if i.cursor == 0 {
		return
	}
	i.pushUndo()
	segments := graphemeSegments(i.value[:i.cursor])
	width := len(segments[len(segments)-1].text)
	i.value = i.value[:i.cursor-width] + i.value[i.cursor:]
	i.cursor -= width
}

func (i *Input) handleForwardDelete() {
	i.lastAction = actionNone
	if i.cursor >= len(i.value) {
		return
	}
	i.pushUndo()
	width := len(graphemeSegments(i.value[i.cursor:])[0].text)
	i.value = i.value[:i.cursor] + i.value[i.cursor+width:]
}

func (i *Input) deleteToLineStart() {
	if i.cursor == 0 {
		return
	}
	i.pushUndo()
	i.ring.push(i.value[:i.cursor], true, i.lastAction == actionKill)
	i.lastAction = actionKill
	i.value = i.value[i.cursor:]
	i.cursor = 0
}

func (i *Input) deleteToLineEnd() {
	if i.cursor >= len(i.value) {
		return
	}
	i.pushUndo()
	i.ring.push(i.value[i.cursor:], false, i.lastAction == actionKill)
	i.lastAction = actionKill
	i.value = i.value[:i.cursor]
}

func (i *Input) deleteWordBackward() {
	if i.cursor == 0 {
		return
	}
	// Read before the move, which resets it.
	wasKill := i.lastAction == actionKill
	i.pushUndo()
	end := i.cursor
	i.moveWordBackward()
	start := i.cursor
	i.ring.push(i.value[start:end], true, wasKill)
	i.lastAction = actionKill
	i.value = i.value[:start] + i.value[end:]
}

func (i *Input) deleteWordForward() {
	if i.cursor >= len(i.value) {
		return
	}
	wasKill := i.lastAction == actionKill
	i.pushUndo()
	start := i.cursor
	i.moveWordForward()
	end := i.cursor
	i.cursor = start
	i.ring.push(i.value[start:end], false, wasKill)
	i.lastAction = actionKill
	i.value = i.value[:start] + i.value[end:]
}

func (i *Input) yank() {
	text, ok := i.ring.peek()
	if !ok {
		return
	}
	i.pushUndo()
	i.value = i.value[:i.cursor] + text + i.value[i.cursor:]
	i.cursor += len(text)
	i.lastAction = actionYank
}

func (i *Input) yankPop() {
	if i.lastAction != actionYank || len(i.ring.ring) <= 1 {
		return
	}
	i.pushUndo()
	previous, _ := i.ring.peek()
	i.value = i.value[:i.cursor-len(previous)] + i.value[i.cursor:]
	i.cursor -= len(previous)
	i.ring.rotate()
	text, _ := i.ring.peek()
	i.value = i.value[:i.cursor] + text + i.value[i.cursor:]
	i.cursor += len(text)
	i.lastAction = actionYank
}

func (i *Input) pushUndo() { i.undoStack = append(i.undoStack, inputState{i.value, i.cursor}) }

func (i *Input) undo() {
	if len(i.undoStack) == 0 {
		return
	}
	last := i.undoStack[len(i.undoStack)-1]
	i.undoStack = i.undoStack[:len(i.undoStack)-1]
	i.value, i.cursor = last.value, last.cursor
	i.lastAction = actionNone
}

func (i *Input) moveWordBackward() {
	if i.cursor == 0 {
		return
	}
	i.lastAction = actionNone
	i.cursor = findWordBackward(i.value, i.cursor, wordSegments(i.value[:i.cursor]))
}

func (i *Input) moveWordForward() {
	if i.cursor >= len(i.value) {
		return
	}
	i.lastAction = actionNone
	i.cursor = findWordForward(i.value, i.cursor, wordSegments(i.value[i.cursor:]))
}

func (i *Input) handlePaste(pasted string) {
	i.lastAction = actionNone
	i.pushUndo()
	// One line, whatever was pasted: a line break here would submit nothing and
	// only misalign the display.
	clean := strings.NewReplacer("\r\n", "", "\r", "", "\n", "", "\t", "    ").Replace(pasted)
	i.value = i.value[:i.cursor] + clean + i.value[i.cursor:]
	i.cursor += len(clean)
}

// shown is the text as it is drawn, and the cursor's offset in it.
func (i *Input) shown() (string, int) {
	if i.Mask == "" {
		return i.value, i.cursor
	}
	before := len(graphemeSegments(i.value[:i.cursor]))
	after := len(graphemeSegments(i.value[i.cursor:]))
	return strings.Repeat(i.Mask, before+after), before * len(i.Mask)
}

// Render draws one line: "> " and the part of the text around the cursor, the
// cursor in inverse video, padded to the width.
func (i *Input) Render(width int) []string {
	const prompt = "> "
	available := width - len(prompt)
	if available <= 0 {
		return []string{prompt}
	}
	value, cursor := i.shown()

	visible := ""
	display := cursor
	total := VisibleWidth(value)
	if total < available {
		visible = value
	} else {
		// Room for the cursor is kept when it sits past the end.
		scroll := available
		if cursor == len(value) {
			scroll = available - 1
		}
		cursorCol := VisibleWidth(value[:cursor])
		if scroll > 0 {
			half := scroll / 2
			start := 0
			switch {
			case cursorCol < half:
			case cursorCol > total-half:
				start = max(0, total-scroll)
			default:
				start = max(0, cursorCol-half)
			}
			visible = SliceByColumn(value, start, scroll, true)
			display = len(SliceByColumn(value, start, max(0, cursorCol-start), true))
		} else {
			visible, display = "", 0
		}
	}

	at := " "
	if segments := graphemeSegments(visible[display:]); len(segments) > 0 {
		at = segments[0].text
	}
	after := ""
	if display+len(at) <= len(visible) {
		after = visible[display+len(at):]
	}
	marker := ""
	if i.focused {
		marker = CursorMarker
	}
	line := visible[:display] + marker + "\x1b[7m" + at + "\x1b[27m" + after
	return []string{prompt + line + strings.Repeat(" ", max(0, available-VisibleWidth(line)))}
}

// SliceByColumn cuts a line to the columns from start for length, keeping the
// styles in force where it starts. Strict drops a wide character that would
// straddle the end — Pi's sliceByColumn.
func SliceByColumn(line string, start, length int, strict bool) string {
	if length <= 0 {
		return ""
	}
	end := start + length
	var result, pending strings.Builder
	col := 0
	for i := 0; i < len(line); {
		if code, ok := extractANSI(line, i); ok {
			switch {
			case col >= start && col < end:
				result.WriteString(code)
			case col < start:
				pending.WriteString(code)
			}
			i += len(code)
			continue
		}
		textEnd := i
		for textEnd < len(line) {
			if _, ok := extractANSI(line, textEnd); ok {
				break
			}
			textEnd++
		}
		done := false
		eachGrapheme(line[i:textEnd], func(cluster string, width int) bool {
			inRange := col >= start && col < end
			fits := !strict || col+width <= end
			if inRange && fits {
				if pending.Len() > 0 {
					result.WriteString(pending.String())
					pending.Reset()
				}
				result.WriteString(cluster)
			}
			col += width
			done = col >= end
			return !done
		})
		i = textEnd
		if done {
			break
		}
	}
	return result.String()
}
