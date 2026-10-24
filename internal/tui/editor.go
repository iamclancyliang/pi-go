package tui

import (
	"context"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// EditorHost is what the editor needs from the interface around it: a frame
// when its look changes outside a keystroke, and the terminal's height, which
// bounds how many lines it shows.
//
// A host that also has Do(func()) — the interface's UI does — runs
// completion off its loop: suggestions are computed on another goroutine and
// handed back through Do, so a slow directory walk never stalls typing.
// Without it, completion runs inline.
type EditorHost interface {
	RequestRender()
	Rows() int
}

// loopHost is a host that can run work on its loop.
type loopHost interface {
	Do(func())
}

// EditorTheme styles the editor.
type EditorTheme struct {
	// BorderColor styles the rules above and below the text.
	BorderColor Style
	// SelectList styles the completion list.
	SelectList SelectListTheme
}

// EditorOptions configure an editor.
type EditorOptions struct {
	// PaddingX is the blank columns either side of the text.
	PaddingX int
	// AutocompleteMaxVisible is how many completions show at once, from 3
	// to 20; zero means 5.
	AutocompleteMaxVisible int
}

// Editor is the prompt editor of the interface — Pi's Editor component: a
// multi-line buffer between two rules, wrapped at word boundaries, scrolled
// to keep the cursor in view, with Emacs-style kill and yank, fish-style
// undo, prompt history, jumping to a character, and large pastes folded into
// markers.
//
// Columns are byte offsets into a line. Every edit moves by whole grapheme
// clusters, and a paste marker counts as one.
type Editor struct {
	host     EditorHost
	paddingX int

	// BorderColor styles the rules; it may be changed at any time, as the
	// interactive mode does to show the thinking level.
	BorderColor Style
	// Keybindings are the keys the editor answers to; nil is Pi's defaults.
	Keybindings *Keybindings
	// OnSubmit receives the text when it is submitted, paste markers
	// expanded and surrounding whitespace trimmed.
	OnSubmit func(text string)
	// OnChange receives the text after every change.
	OnChange func(text string)
	// DisableSubmit makes the submit key do nothing.
	DisableSubmit bool

	state   editorState
	focused bool

	// lastWidth is the width the text was last wrapped at; vertical movement
	// moves through the visual lines of that layout.
	lastWidth    int
	scrollOffset int

	pastes pasteRegistry

	history      []string // newest first
	historyIndex int      // -1 when not browsing
	historyDraft *editorState

	killRing   killRing
	lastAction editAction
	jumpMode   int // 0, or the direction of a pending jump: 1 forward, -1 backward

	// preferredCol is the sticky column of vertical movement, in UTF-16 code
	// units from the start of a visual line, as Pi keeps it.
	preferredCol    int
	hasPreferredCol bool
	// snappedFrom is where the cursor would have landed, in code units from
	// the start of its line, when it was moved back to the start of a paste
	// marker it fell inside; the next vertical move starts from there.
	snappedFrom    int
	hasSnappedFrom bool

	undoStack []editorSnapshot

	ac autocompleteState
}

// autocompleteState is the editor's completion: the provider, the list on
// show, and the request in flight.
type autocompleteState struct {
	provider        AutocompleteProvider
	triggers        []string
	triggerPattern  *regexp.Regexp
	debouncePattern *regexp.Regexp
	maxVisible      int
	theme           SelectListTheme

	mode   int // acNone, acRegular, or acForce
	list   *SelectList
	prefix string

	// startToken invalidates a request not yet started (still debouncing);
	// requestID one in flight; cancel stops it.
	startToken int
	requestID  int
	cancel     context.CancelFunc
	debounce   *time.Timer
}

const (
	acNone = iota
	acRegular
	acForce
)

// attachmentDebounce is how long typing after @ or another trigger pauses
// before completion runs, so a path search is not started per keystroke.
const attachmentDebounce = 20 * time.Millisecond

type editorState struct {
	lines      []string
	cursorLine int
	cursorCol  int
}

func (s editorState) clone() editorState {
	s.lines = slices.Clone(s.lines)
	return s
}

type editorSnapshot struct {
	state  editorState
	pastes pasteRegistry
}

type editAction int

const (
	actionNone editAction = iota
	actionKill
	actionYank
	actionTypeWord
)

// NewEditor is an empty editor. host may be nil, for an editor drawn outside
// an interface; it is then taken to be on a 24-row terminal.
func NewEditor(host EditorHost, theme EditorTheme, options EditorOptions) *Editor {
	e := &Editor{
		host:         host,
		paddingX:     max(0, options.PaddingX),
		BorderColor:  theme.BorderColor,
		state:        editorState{lines: []string{""}},
		lastWidth:    80,
		historyIndex: -1,
		pastes:       newPasteRegistry(),
	}
	e.ac.theme = theme.SelectList
	e.ac.maxVisible = clampMaxVisible(options.AutocompleteMaxVisible)
	e.setTriggerCharacters(nil)
	return e
}

func clampMaxVisible(n int) int {
	if n == 0 {
		return 5
	}
	return max(3, min(20, n))
}

func (e *Editor) keys() *Keybindings {
	if e.Keybindings == nil {
		e.Keybindings = NewKeybindings(TUIKeybindings, nil)
	}
	return e.Keybindings
}

func (e *Editor) rows() int {
	if e.host == nil {
		return 24
	}
	return e.host.Rows()
}

// maxVisibleLines is how many lines of text are shown before the editor
// scrolls: 30% of the terminal, and never fewer than five.
func (e *Editor) maxVisibleLines() int { return max(5, e.rows()*3/10) }

// PaddingX is the blank columns either side of the text.
func (e *Editor) PaddingX() int { return e.paddingX }

// SetPaddingX changes the blank columns either side of the text.
func (e *Editor) SetPaddingX(padding int) {
	padding = max(0, padding)
	if padding != e.paddingX {
		e.paddingX = padding
		if e.host != nil {
			e.host.RequestRender()
		}
	}
}

// SetFocused is told when the editor gains or loses the keyboard; it shows
// the hardware cursor's position only while it has it.
func (e *Editor) SetFocused(focused bool) { e.focused = focused }

// Invalidate drops cached rendering; the editor keeps none.
func (e *Editor) Invalidate() {}

// Text is the text as typed, paste markers and all.
func (e *Editor) Text() string { return strings.Join(e.state.lines, "\n") }

// ExpandedText is the text with every paste marker replaced by what was
// pasted — what an external editor should be given.
func (e *Editor) ExpandedText() string { return e.expandPasteMarkers(e.Text()) }

// Lines are the text's lines.
func (e *Editor) Lines() []string { return slices.Clone(e.state.lines) }

// Cursor is the cursor's line and its byte offset in that line.
func (e *Editor) Cursor() (line, col int) { return e.state.cursorLine, e.state.cursorCol }

// SetText replaces the text, leaving the cursor at its end. The replacement
// can be undone.
func (e *Editor) SetText(text string) {
	e.cancelAutocomplete()
	e.lastAction = actionNone
	e.exitHistoryBrowsing()
	normalized := normalizeText(text)
	if e.Text() != normalized {
		e.pushUndoSnapshot()
	}
	e.pastes = newPasteRegistry()
	e.setTextInternal(normalized, false)
}

// InsertTextAtCursor inserts text as one undoable step.
func (e *Editor) InsertTextAtCursor(text string) {
	if text == "" {
		return
	}
	e.cancelAutocomplete()
	e.pushUndoSnapshot()
	e.lastAction = actionNone
	e.exitHistoryBrowsing()
	e.insertTextAtCursorInternal(text)
}

// AddToHistory remembers a submitted prompt for the up arrow. Blank prompts
// and a repeat of the newest entry are not added, and the oldest entries go
// past a hundred.
func (e *Editor) AddToHistory(text string) {
	trimmed := strings.TrimFunc(text, jsSpace)
	if trimmed == "" || (len(e.history) > 0 && e.history[0] == trimmed) {
		return
	}
	e.history = append([]string{trimmed}, e.history...)
	if len(e.history) > 100 {
		e.history = e.history[:100]
	}
}

func (e *Editor) changed() {
	if e.OnChange != nil {
		e.OnChange(e.Text())
	}
}

func (e *Editor) currentLine() string { return e.state.lines[e.state.cursorLine] }

func (e *Editor) setLine(s string) { e.state.lines[e.state.cursorLine] = s }

// setCursorCol moves the cursor within its line, which ends any sticky
// column.
func (e *Editor) setCursorCol(col int) {
	e.state.cursorCol = col
	e.hasPreferredCol = false
	e.hasSnappedFrom = false
}

func (e *Editor) graphemes(text string) []segment {
	return withMarkers(text, graphemeSegments(text), e.pastes.valid())
}

func (e *Editor) words(text string) []segment {
	return withMarkers(text, wordSegments(text), e.pastes.valid())
}

// HandleKey applies one keystroke.
func (e *Editor) HandleKey(k Key) {
	kb := e.keys()

	if e.jumpMode != 0 {
		// The jump key again cancels; a character is the target; anything
		// else cancels and is then handled as itself.
		if kb.Matches(k, "tui.editor.jumpForward") || kb.Matches(k, "tui.editor.jumpBackward") {
			e.jumpMode = 0
			return
		}
		if k.IsText() {
			direction := e.jumpMode
			e.jumpMode = 0
			e.jumpToChar(string(k.Text), direction)
			return
		}
		e.jumpMode = 0
	}

	if k.Name == "paste" {
		if k.Paste != "" {
			e.handlePaste(k.Paste)
		}
		return
	}

	if kb.Matches(k, "tui.input.copy") {
		// Left to the interactive mode, which clears or exits.
		return
	}
	if kb.Matches(k, "tui.editor.undo") {
		e.undo()
		return
	}
	if e.ac.mode != acNone && e.ac.list != nil && e.handleAutocompleteKey(k) {
		return
	}

	switch {
	case kb.Matches(k, "tui.input.tab"):
		if e.ac.mode == acNone {
			e.handleTabCompletion()
		}
	case kb.Matches(k, "tui.editor.deleteToLineEnd"):
		e.deleteToEndOfLine()
	case kb.Matches(k, "tui.editor.deleteToLineStart"):
		e.deleteToStartOfLine()
	case kb.Matches(k, "tui.editor.deleteWordBackward"):
		e.deleteWordBackward()
	case kb.Matches(k, "tui.editor.deleteWordForward"):
		e.deleteWordForward()
	case kb.Matches(k, "tui.editor.deleteCharBackward") || KeyMatches(k, "shift+backspace"):
		e.handleBackspace()
	case kb.Matches(k, "tui.editor.deleteCharForward") || KeyMatches(k, "shift+delete"):
		e.handleForwardDelete()
	case kb.Matches(k, "tui.editor.yank"):
		e.yank()
	case kb.Matches(k, "tui.editor.yankPop"):
		e.yankPop()
	case kb.Matches(k, "tui.editor.historyPrevious"):
		e.cancelAutocomplete()
		e.navigateHistory(-1)
	case kb.Matches(k, "tui.editor.historyNext"):
		e.cancelAutocomplete()
		e.navigateHistory(1)
	case kb.Matches(k, "tui.editor.cursorLineStart"):
		e.lastAction = actionNone
		e.setCursorCol(0)
	case kb.Matches(k, "tui.editor.cursorLineEnd"):
		e.lastAction = actionNone
		e.setCursorCol(len(e.currentLine()))
	case kb.Matches(k, "tui.editor.cursorWordLeft"):
		e.moveWordBackward()
	case kb.Matches(k, "tui.editor.cursorWordRight"):
		e.moveWordForward()
	case kb.Matches(k, "tui.input.newLine") || isLegacyNewline(k):
		if e.shouldSubmitOnBackslashEnter(k) {
			e.handleBackspace()
			e.submitValue()
			return
		}
		e.addNewLine()
	case kb.Matches(k, "tui.input.submit"):
		if e.DisableSubmit {
			return
		}
		// A terminal that cannot send shift+enter can still start a new line:
		// a backslash before the cursor turns enter into one.
		if line := e.currentLine(); e.state.cursorCol > 0 && line[e.state.cursorCol-1] == '\\' {
			e.handleBackspace()
			e.addNewLine()
			return
		}
		e.submitValue()
	case kb.Matches(k, "tui.editor.cursorUp"):
		switch {
		case e.isOnFirstVisualLine() && (e.isEmpty() || e.historyIndex > -1 || e.state.cursorCol == 0):
			e.navigateHistory(-1)
		case e.isOnFirstVisualLine():
			// Already on top: go to the line's start first, so a second
			// press is the one that reaches into history.
			e.lastAction = actionNone
			e.setCursorCol(0)
		default:
			e.moveCursor(-1, 0)
		}
	case kb.Matches(k, "tui.editor.cursorDown"):
		switch {
		case e.historyIndex > -1 && e.isOnLastVisualLine():
			e.navigateHistory(1)
		case e.isOnLastVisualLine():
			e.lastAction = actionNone
			e.setCursorCol(len(e.currentLine()))
		default:
			e.moveCursor(1, 0)
		}
	case kb.Matches(k, "tui.editor.cursorRight"):
		e.moveCursor(0, 1)
	case kb.Matches(k, "tui.editor.cursorLeft"):
		e.moveCursor(0, -1)
	case kb.Matches(k, "tui.editor.pageUp"):
		e.pageScroll(-1)
	case kb.Matches(k, "tui.editor.pageDown"):
		e.pageScroll(1)
	case kb.Matches(k, "tui.editor.jumpForward"):
		e.jumpMode = 1
	case kb.Matches(k, "tui.editor.jumpBackward"):
		e.jumpMode = -1
	case KeyMatches(k, "shift+space"):
		e.insertCharacter(" ", false)
	case k.IsText():
		e.insertCharacter(string(k.Text), false)
	}
}

// isLegacyNewline reports the bytes Pi's editor takes for a new line whatever
// the bindings say: a bare line feed, alt+enter's ESC CR, and the shifted-F3
// code some terminals send for shift+enter.
func isLegacyNewline(k Key) bool {
	if KeyMatches(k, "alt+enter") {
		return true
	}
	if k.Name == "ctrl+j" && slices.Contains(k.Also, "enter") {
		return true
	}
	return false
}

// shouldSubmitOnBackslashEnter: with enter bound to new lines and
// shift+enter to submit, the backslash workaround runs the other way.
func (e *Editor) shouldSubmitOnBackslashEnter(k Key) bool {
	if e.DisableSubmit || !KeyMatches(k, "enter") {
		return false
	}
	submit := e.keys().Keys("tui.input.submit")
	if !slices.Contains(submit, "shift+enter") && !slices.Contains(submit, "shift+return") {
		return false
	}
	line := e.currentLine()
	return e.state.cursorCol > 0 && line[e.state.cursorCol-1] == '\\'
}

// --- history ---

func (e *Editor) isEmpty() bool { return len(e.state.lines) == 1 && e.state.lines[0] == "" }

func (e *Editor) isOnFirstVisualLine() bool {
	return e.currentVisualLine(e.visualLineMap(e.lastWidth)) == 0
}

func (e *Editor) isOnLastVisualLine() bool {
	vls := e.visualLineMap(e.lastWidth)
	return e.currentVisualLine(vls) == len(vls)-1
}

// navigateHistory moves through history: -1 to older entries, 1 back toward
// the draft, which is restored as it was left.
func (e *Editor) navigateHistory(direction int) {
	e.lastAction = actionNone
	if len(e.history) == 0 {
		return
	}
	index := e.historyIndex - direction
	if index < -1 || index >= len(e.history) {
		return
	}
	if e.historyIndex == -1 && index >= 0 {
		e.pushUndoSnapshot()
		draft := e.state.clone()
		e.historyDraft = &draft
	}
	e.historyIndex = index
	if index == -1 {
		draft := e.historyDraft
		e.historyDraft = nil
		if draft != nil {
			e.state = *draft
			e.hasPreferredCol = false
			e.hasSnappedFrom = false
			e.scrollOffset = 0
			e.changed()
		} else {
			e.setTextInternal("", false)
		}
		return
	}
	// Going back in time puts the cursor at the entry's start, coming
	// forward at its end, so the next press in the same direction moves on.
	e.setTextInternal(e.history[index], direction == -1)
}

func (e *Editor) exitHistoryBrowsing() {
	e.historyIndex = -1
	e.historyDraft = nil
}

func (e *Editor) setTextInternal(text string, cursorAtStart bool) {
	e.state.lines = strings.Split(text, "\n")
	if cursorAtStart {
		e.state.cursorLine = 0
		e.setCursorCol(0)
	} else {
		e.state.cursorLine = len(e.state.lines) - 1
		e.setCursorCol(len(e.state.lines[e.state.cursorLine]))
	}
	e.scrollOffset = 0
	e.changed()
}

// --- rendering ---

// layoutLine is one visual line to draw, with the cursor's byte offset in it
// when it holds the cursor.
type layoutLine struct {
	text      string
	hasCursor bool
	cursorPos int
}

// Render draws the text between two rules, scrolled to keep the cursor in
// view; a rule shows how many lines are out of sight past it.
func (e *Editor) Render(width int) []string {
	paddingX := min(e.paddingX, max(0, (width-1)/2))
	contentWidth := max(1, width-paddingX*2)
	// Without padding, one column is kept free for a cursor at a line's end;
	// with it, the cursor may stand in the padding.
	layoutWidth := contentWidth
	if paddingX == 0 {
		layoutWidth = max(1, contentWidth-1)
	}
	e.lastWidth = layoutWidth

	layout := e.layoutText(layoutWidth)
	maxVisible := e.maxVisibleLines()

	cursorIndex := 0
	for i, l := range layout {
		if l.hasCursor {
			cursorIndex = i
			break
		}
	}
	if cursorIndex < e.scrollOffset {
		e.scrollOffset = cursorIndex
	} else if cursorIndex >= e.scrollOffset+maxVisible {
		e.scrollOffset = cursorIndex - maxVisible + 1
	}
	e.scrollOffset = max(0, min(e.scrollOffset, max(0, len(layout)-maxVisible)))
	visible := layout[e.scrollOffset:min(len(layout), e.scrollOffset+maxVisible)]

	padding := strings.Repeat(" ", paddingX)
	var out []string
	if e.scrollOffset > 0 {
		out = append(out, e.BorderColor.apply(scrollBorder("↑", e.scrollOffset, width)))
	} else {
		out = append(out, e.BorderColor.apply(strings.Repeat("─", width)))
	}

	for _, l := range visible {
		text := l.text
		lineWidth := VisibleWidth(l.text)
		cursorInPadding := false
		if l.hasCursor {
			before, after := text[:l.cursorPos], text[l.cursorPos:]
			marker := ""
			if e.focused {
				// Where the hardware cursor goes, so an input method's
				// candidate window opens at the text.
				marker = CursorMarker
			}
			if after != "" {
				first := e.graphemes(after)[0].text
				text = before + marker + "\x1b[7m" + first + "\x1b[0m" + after[len(first):]
			} else {
				text = before + marker + "\x1b[7m \x1b[0m"
				lineWidth++
				cursorInPadding = lineWidth > contentWidth && paddingX > 0
			}
		}
		right := padding
		if cursorInPadding {
			right = padding[1:]
		}
		out = append(out, padding+text+strings.Repeat(" ", max(0, contentWidth-lineWidth))+right)
	}

	if below := len(layout) - (e.scrollOffset + len(visible)); below > 0 {
		out = append(out, e.BorderColor.apply(scrollBorder("↓", below, width)))
	} else {
		out = append(out, e.BorderColor.apply(strings.Repeat("─", width)))
	}

	if e.ac.mode != acNone && e.ac.list != nil {
		for _, l := range e.ac.list.Render(contentWidth) {
			out = append(out, padding+l+strings.Repeat(" ", max(0, contentWidth-VisibleWidth(l)))+padding)
		}
	}
	return out
}

// scrollBorder is a rule that says how many lines are hidden past it,
// "─── ↑ 3 more ───", cut to fit a narrow width.
func scrollBorder(arrow string, hidden, width int) string {
	width = max(0, width)
	indicator := "─── " + arrow + " " + strconv.Itoa(hidden) + " more "
	if rest := width - VisibleWidth(indicator); rest >= 0 {
		return indicator + strings.Repeat("─", rest)
	}
	ellipsis := "..."[:min(3, width)]
	var cut strings.Builder
	room := width - len(ellipsis)
	eachGrapheme(indicator, func(cluster string, w int) bool {
		if w > room {
			return false
		}
		room -= w
		cut.WriteString(cluster)
		return true
	})
	return cut.String() + ellipsis
}

func (e *Editor) layoutText(width int) []layoutLine {
	if e.isEmpty() {
		return []layoutLine{{hasCursor: true}}
	}
	var out []layoutLine
	for i, line := range e.state.lines {
		current := i == e.state.cursorLine
		if VisibleWidth(line) <= width {
			out = append(out, layoutLine{text: line, hasCursor: current, cursorPos: e.state.cursorCol})
			continue
		}
		chunks := wordWrapLine(line, width, e.graphemes(line))
		for j, c := range chunks {
			cursor := e.state.cursorCol
			l := layoutLine{text: c.text}
			if current {
				if j == len(chunks)-1 {
					l.hasCursor = cursor >= c.start
				} else {
					l.hasCursor = cursor >= c.start && cursor < c.end
				}
				if l.hasCursor {
					l.cursorPos = min(cursor-c.start, len(c.text))
				}
			}
			out = append(out, l)
		}
	}
	return out
}

// --- visual lines and vertical movement ---

// visualLine is one line as wrapped at a width: a range of a logical line, in
// bytes and in the UTF-16 units the sticky column is measured in.
type visualLine struct {
	logical       int
	start, length int
	startU, lenU  int
}

func (e *Editor) visualLineMap(width int) []visualLine {
	var out []visualLine
	for i, line := range e.state.lines {
		switch {
		case line == "":
			out = append(out, visualLine{logical: i})
		case VisibleWidth(line) <= width:
			out = append(out, visualLine{logical: i, length: len(line), lenU: utf16Len(line)})
		default:
			for _, c := range wordWrapLine(line, width, e.graphemes(line)) {
				out = append(out, visualLine{
					logical: i, start: c.start, length: c.end - c.start,
					startU: utf16Len(line[:c.start]), lenU: utf16Len(c.text),
				})
			}
		}
	}
	return out
}

func lastSegmentOfLine(vls []visualLine, i int) bool {
	return i == len(vls)-1 || vls[i+1].logical != vls[i].logical
}

// visualLineAt is the visual line holding a position given in code units.
// The end of a logical line belongs to its last visual line.
func visualLineAt(vls []visualLine, line, colU int) int {
	for i, vl := range vls {
		if vl.logical != line {
			continue
		}
		offset := colU - vl.startU
		if offset >= 0 && (offset < vl.lenU || (lastSegmentOfLine(vls, i) && offset == vl.lenU)) {
			return i
		}
	}
	return len(vls) - 1
}

func (e *Editor) cursorUnits() int { return utf16Len(e.currentLine()[:e.state.cursorCol]) }

func (e *Editor) currentVisualLine(vls []visualLine) int {
	return visualLineAt(vls, e.state.cursorLine, e.cursorUnits())
}

// moveToVisualLine moves the cursor to another visual line, keeping its
// column by the sticky-column rules, and never leaves it inside a paste
// marker or other multi-unit cluster: it lands on the cluster's start.
func (e *Editor) moveToVisualLine(vls []visualLine, from, to int) {
	if from < 0 || from >= len(vls) || to < 0 || to >= len(vls) {
		return
	}
	current, target := vls[from], vls[to]

	var currentCol int
	if e.hasSnappedFrom {
		i := visualLineAt(vls, current.logical, e.snappedFrom)
		currentCol = e.snappedFrom - vls[i].startU
	} else {
		currentCol = e.cursorUnits() - current.startU
	}

	// A visual line that is not its logical line's last ends where the next
	// begins, so the column before that is the last one on it.
	sourceMax := current.lenU
	if !lastSegmentOfLine(vls, from) {
		sourceMax = max(0, current.lenU-1)
	}
	targetMax := target.lenU
	if !lastSegmentOfLine(vls, to) {
		targetMax = max(0, target.lenU-1)
	}
	col := e.verticalMoveColumn(currentCol, sourceMax, targetMax)

	e.state.cursorLine = target.logical
	line := e.state.lines[target.logical]
	cursorU := min(target.startU+col, utf16Len(line))

	unit := 0
	for _, seg := range e.graphemes(line) {
		segU := utf16Len(seg.text)
		if unit > cursorU {
			break
		}
		if segU > 1 && cursorU < unit+segU {
			if unit < target.startU && to > from {
				// The cluster began on an earlier visual line, which the
				// cursor has already passed on its way down: skip the
				// lines it continues on and land on the first past it.
				end := unit + segU
				next := to + 1
				for next < len(vls) && vls[next].logical == target.logical && vls[next].startU < end {
					next++
				}
				if next < len(vls) {
					e.moveToVisualLine(vls, from, next)
					return
				}
			}
			e.snappedFrom, e.hasSnappedFrom = cursorU, true
			e.state.cursorCol = seg.index
			return
		}
		unit += segU
	}
	e.state.cursorCol = utf16Offset(line, cursorU)
	e.hasSnappedFrom = false
}

// verticalMoveColumn decides the column a vertical move lands on, and what
// it leaves as the sticky column (P: a sticky column is set; S: the cursor
// is before the end of its line; T: the target is shorter than the current
// column; U: the target is shorter than the sticky column):
//
//	P S T U | sticky after | lands on
//	0 * 0 - | none         | current column
//	0 * 1 - | current      | target's end
//	1 0 0 0 | none         | sticky column
//	1 0 0 1 | kept         | target's end
//	1 0 1 - | kept         | target's end
//	1 1 0 - | none         | current column
//	1 1 1 - | current      | target's end
func (e *Editor) verticalMoveColumn(currentCol, sourceMax, targetMax int) int {
	inMiddle := currentCol < sourceMax
	tooShort := targetMax < currentCol
	if !e.hasPreferredCol || inMiddle {
		if tooShort {
			e.preferredCol, e.hasPreferredCol = currentCol, true
			return targetMax
		}
		e.hasPreferredCol = false
		return currentCol
	}
	if tooShort || targetMax < e.preferredCol {
		return targetMax
	}
	e.hasPreferredCol = false
	return e.preferredCol
}

func (e *Editor) moveCursor(deltaLine, deltaCol int) {
	e.lastAction = actionNone
	vls := e.visualLineMap(e.lastWidth)
	current := e.currentVisualLine(vls)

	if deltaLine != 0 {
		if target := current + deltaLine; target >= 0 && target < len(vls) {
			e.moveToVisualLine(vls, current, target)
		}
	}

	if deltaCol > 0 {
		line := e.currentLine()
		switch {
		case e.state.cursorCol < len(line):
			e.setCursorCol(e.state.cursorCol + len(e.graphemes(line[e.state.cursorCol:])[0].text))
		case e.state.cursorLine < len(e.state.lines)-1:
			e.state.cursorLine++
			e.setCursorCol(0)
		default:
			// Nowhere to go, but the column is remembered, so moving up from
			// the end of the text lands as far along as the cursor stands.
			if current < len(vls) {
				e.preferredCol, e.hasPreferredCol = e.cursorUnits()-vls[current].startU, true
			}
		}
	} else if deltaCol < 0 {
		line := e.currentLine()
		switch {
		case e.state.cursorCol > 0:
			segs := e.graphemes(line[:e.state.cursorCol])
			e.setCursorCol(e.state.cursorCol - len(segs[len(segs)-1].text))
		case e.state.cursorLine > 0:
			e.state.cursorLine--
			e.setCursorCol(len(e.currentLine()))
		}
	}

	// The text before the cursor changed, so an open list is stale: ask
	// again, which refreshes it or closes it.
	if e.ac.mode != acNone {
		e.updateAutocomplete()
	}
}

// pageScroll moves the cursor a page — the editor's visible height — up or
// down.
func (e *Editor) pageScroll(direction int) {
	e.lastAction = actionNone
	vls := e.visualLineMap(e.lastWidth)
	current := e.currentVisualLine(vls)
	target := max(0, min(len(vls)-1, current+direction*e.maxVisibleLines()))
	e.moveToVisualLine(vls, current, target)
}

func (e *Editor) moveWordBackward() {
	e.lastAction = actionNone
	if e.state.cursorCol == 0 {
		if e.state.cursorLine > 0 {
			e.state.cursorLine--
			e.setCursorCol(len(e.currentLine()))
		}
		return
	}
	line := e.currentLine()
	e.setCursorCol(findWordBackward(line, e.state.cursorCol, e.words(line[:e.state.cursorCol])))
}

func (e *Editor) moveWordForward() {
	e.lastAction = actionNone
	line := e.currentLine()
	if e.state.cursorCol >= len(line) {
		if e.state.cursorLine < len(e.state.lines)-1 {
			e.state.cursorLine++
			e.setCursorCol(0)
		}
		return
	}
	e.setCursorCol(findWordForward(line, e.state.cursorCol, e.words(line[e.state.cursorCol:])))
}

// jumpToChar moves to the next occurrence of char after the cursor, or the
// previous one before it, searching across lines. Case matters; with no
// occurrence the cursor stays.
func (e *Editor) jumpToChar(char string, direction int) {
	e.lastAction = actionNone
	for i := e.state.cursorLine; i >= 0 && i < len(e.state.lines); i += direction {
		line := e.state.lines[i]
		index := -1
		if direction > 0 {
			from := 0
			if i == e.state.cursorLine {
				// From the character after the cursor's.
				from = len(line) + 1
				if e.state.cursorCol < len(line) {
					from = e.state.cursorCol + runeLen(line[e.state.cursorCol:])
				}
			}
			if from <= len(line) {
				if j := strings.Index(line[from:], char); j >= 0 {
					index = from + j
				}
			}
		} else {
			upTo := len(line)
			if i == e.state.cursorLine {
				// Matches starting before the cursor — or at the very start
				// of the line when the cursor is there, since Pi's
				// lastIndexOf clamps its start to zero.
				upTo = min(max(e.state.cursorCol-1, 0)+len(char), len(line))
			}
			index = strings.LastIndex(line[:upTo], char)
		}
		if index >= 0 {
			e.state.cursorLine = i
			e.setCursorCol(index)
			return
		}
	}
}

// runeLen is the byte length of s's first character.
func runeLen(s string) int {
	for i := range s {
		if i > 0 {
			return i
		}
	}
	return len(s)
}

// --- editing ---

func normalizeText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.ReplaceAll(text, "\t", "    ")
}

func (e *Editor) insertTextAtCursorInternal(text string) {
	if text == "" {
		return
	}
	inserted := strings.Split(normalizeText(text), "\n")
	line := e.currentLine()
	before, after := line[:e.state.cursorCol], line[e.state.cursorCol:]
	if len(inserted) == 1 {
		e.setLine(before + inserted[0] + after)
		e.setCursorCol(e.state.cursorCol + len(inserted[0]))
	} else {
		last := inserted[len(inserted)-1]
		lines := slices.Clone(e.state.lines[:e.state.cursorLine])
		lines = append(lines, before+inserted[0])
		lines = append(lines, inserted[1:len(inserted)-1]...)
		lines = append(lines, last+after)
		lines = append(lines, e.state.lines[e.state.cursorLine+1:]...)
		e.state.lines = lines
		e.state.cursorLine += len(inserted) - 1
		e.setCursorCol(len(last))
	}
	e.changed()
}

// insertCharacter types one character. Undo is fish's: a run of word
// characters is one step, and each whitespace character starts a new one —
// taken before it, so undoing removes the space and the word after it
// together.
func (e *Editor) insertCharacter(char string, skipUndoCoalescing bool) {
	e.exitHistoryBrowsing()
	if !skipUndoCoalescing {
		if isWhitespace(char) || e.lastAction != actionTypeWord {
			e.pushUndoSnapshot()
		}
		e.lastAction = actionTypeWord
	}
	line := e.currentLine()
	e.setLine(line[:e.state.cursorCol] + char + line[e.state.cursorCol:])
	e.setCursorCol(e.state.cursorCol + len(char))
	e.changed()

	if e.ac.mode != acNone {
		e.updateAutocomplete()
		return
	}
	before := e.currentLine()[:e.state.cursorCol]
	switch {
	case char == "/" && e.isAtStartOfMessage():
		// A slash command at the start of the message.
		e.requestAutocomplete(false, false)
	case slices.Contains(e.ac.triggers, char):
		// A trigger character opens completion at the start of a word.
		if len(before) == len(char) || before[len(before)-len(char)-1] == ' ' || before[len(before)-len(char)-1] == '\t' {
			e.requestAutocomplete(false, false)
		}
	case len(char) == 1 && isCompletionChar(char[0]):
		if e.isInSlashCommandContext(before) || e.ac.triggerPattern.MatchString(before) {
			e.requestAutocomplete(false, false)
		}
	}
}

// isCompletionChar is a character that continues a command name, path or
// mention being completed.
func isCompletionChar(b byte) bool {
	return b == '.' || b == '-' || b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

var csiUCtrlLetter = regexp.MustCompile(`\x1b\[(\d+);5u`)

// handlePaste inserts pasted text as one undoable step. Control characters
// other than line breaks are dropped, and a paste of more than ten lines or
// a thousand characters is held aside behind a marker, expanded again on
// submit.
func (e *Editor) handlePaste(pasted string) {
	e.cancelAutocomplete()
	e.exitHistoryBrowsing()
	e.lastAction = actionNone
	e.pushUndoSnapshot()

	// Some terminals — tmux popups among them — re-encode control bytes
	// inside a paste as CSI u ctrl+letter; decode those back, or the filter
	// below would keep their printable tail.
	pasted = csiUCtrlLetter.ReplaceAllStringFunc(pasted, func(m string) string {
		cp, _ := strconv.Atoi(csiUCtrlLetter.FindStringSubmatch(m)[1])
		switch {
		case cp >= 97 && cp <= 122:
			return string(rune(cp - 96))
		case cp >= 65 && cp <= 90:
			return string(rune(cp - 64))
		}
		return m
	})

	filtered := strings.Map(func(r rune) rune {
		if r == '\n' || r >= 32 {
			return r
		}
		return -1
	}, normalizeText(pasted))

	// A pasted path after a word gets a space before it.
	if strings.HasPrefix(filtered, "/") || strings.HasPrefix(filtered, "~") || strings.HasPrefix(filtered, ".") {
		if c := e.state.cursorCol; c > 0 && isASCIIWordByte(e.currentLine()[c-1]) {
			filtered = " " + filtered
		}
	}

	lines := strings.Count(filtered, "\n") + 1
	chars := utf16Len(filtered)
	if lines > 10 || chars > 1000 {
		e.pastes.counter++
		id := e.pastes.counter
		e.pastes.set(id, filtered)
		marker := "[paste #" + strconv.Itoa(id) + " " + strconv.Itoa(chars) + " chars]"
		if lines > 10 {
			marker = "[paste #" + strconv.Itoa(id) + " +" + strconv.Itoa(lines) + " lines]"
		}
		e.insertTextAtCursorInternal(marker)
		return
	}
	e.insertTextAtCursorInternal(filtered)
}

func isASCIIWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func (e *Editor) addNewLine() {
	e.cancelAutocomplete()
	e.exitHistoryBrowsing()
	e.lastAction = actionNone
	e.pushUndoSnapshot()
	line := e.currentLine()
	e.setLine(line[:e.state.cursorCol])
	e.state.lines = slices.Insert(e.state.lines, e.state.cursorLine+1, line[e.state.cursorCol:])
	e.state.cursorLine++
	e.setCursorCol(0)
	e.changed()
}

func (e *Editor) submitValue() {
	e.cancelAutocomplete()
	result := strings.TrimFunc(e.expandPasteMarkers(e.Text()), jsSpace)
	e.state = editorState{lines: []string{""}}
	e.pastes = newPasteRegistry()
	e.exitHistoryBrowsing()
	e.scrollOffset = 0
	e.undoStack = nil
	e.lastAction = actionNone
	if e.OnChange != nil {
		e.OnChange("")
	}
	if e.OnSubmit != nil {
		e.OnSubmit(result)
	}
}

// mergeWithPrevious joins the cursor's line onto the one above.
func (e *Editor) mergeWithPrevious() {
	previous := e.state.lines[e.state.cursorLine-1]
	e.state.lines[e.state.cursorLine-1] = previous + e.currentLine()
	e.state.lines = slices.Delete(e.state.lines, e.state.cursorLine, e.state.cursorLine+1)
	e.state.cursorLine--
	e.setCursorCol(len(previous))
}

// mergeWithNext joins the line below onto the cursor's.
func (e *Editor) mergeWithNext() {
	e.setLine(e.currentLine() + e.state.lines[e.state.cursorLine+1])
	e.state.lines = slices.Delete(e.state.lines, e.state.cursorLine+1, e.state.cursorLine+2)
}

func (e *Editor) handleBackspace() {
	e.exitHistoryBrowsing()
	e.lastAction = actionNone
	switch {
	case e.state.cursorCol > 0:
		e.pushUndoSnapshot()
		segs := e.graphemes(e.currentLine()[:e.state.cursorCol])
		last := segs[len(segs)-1].text
		if m := pasteMarkerSingle.FindStringSubmatch(last); m != nil {
			id, _ := strconv.Atoi(m[1])
			e.removePaste(id)
		}
		line := e.currentLine()
		e.setLine(line[:e.state.cursorCol-len(last)] + line[e.state.cursorCol:])
		e.setCursorCol(e.state.cursorCol - len(last))
	case e.state.cursorLine > 0:
		e.pushUndoSnapshot()
		e.mergeWithPrevious()
	}
	e.changed()
	e.refreshAutocompleteAfterDelete()
}

// refreshAutocompleteAfterDelete updates an open list, or opens one again
// when deleting has brought the cursor back into something completable.
func (e *Editor) refreshAutocompleteAfterDelete() {
	if e.ac.mode != acNone {
		e.updateAutocomplete()
		return
	}
	before := e.currentLine()[:e.state.cursorCol]
	if e.isInSlashCommandContext(before) || e.ac.triggerPattern.MatchString(before) {
		e.requestAutocomplete(false, false)
	}
}

// removePaste forgets a deleted marker's paste and renumbers the pastes
// after it, in the registry and in the text, so the ids stay 1..n.
func (e *Editor) removePaste(id int) {
	e.pastes.delete(id)
	e.pastes.counter--
	var higher []int
	for _, other := range e.pastes.order {
		if other > id {
			higher = append(higher, other)
		}
	}
	slices.Sort(higher)
	for _, other := range higher {
		content := e.pastes.byID[other]
		e.pastes.delete(other)
		e.pastes.set(other-1, content)
	}
	for i, line := range e.state.lines {
		e.state.lines[i] = pasteMarker.ReplaceAllStringFunc(line, func(m string) string {
			groups := pasteMarker.FindStringSubmatch(m)
			x, _ := strconv.Atoi(groups[1])
			if x <= id {
				return m
			}
			return "[paste #" + strconv.Itoa(x-1) + groups[2] + "]"
		})
	}
}

func (e *Editor) handleForwardDelete() {
	e.exitHistoryBrowsing()
	e.lastAction = actionNone
	line := e.currentLine()
	switch {
	case e.state.cursorCol < len(line):
		e.pushUndoSnapshot()
		first := e.graphemes(line[e.state.cursorCol:])[0].text
		e.setLine(line[:e.state.cursorCol] + line[e.state.cursorCol+len(first):])
	case e.state.cursorLine < len(e.state.lines)-1:
		e.pushUndoSnapshot()
		e.mergeWithNext()
	}
	e.changed()
	e.refreshAutocompleteAfterDelete()
}

// kill records deleted text in the kill ring. Kills in a row accumulate into
// one entry — backward ones in front, forward ones behind — so the text
// comes back whole with one yank.
func (e *Editor) kill(text string, backward bool) {
	e.killRing.push(text, backward, e.lastAction == actionKill)
	e.lastAction = actionKill
}

func (e *Editor) deleteToStartOfLine() {
	e.exitHistoryBrowsing()
	line := e.currentLine()
	switch {
	case e.state.cursorCol > 0:
		e.pushUndoSnapshot()
		e.kill(line[:e.state.cursorCol], true)
		e.setLine(line[e.state.cursorCol:])
		e.setCursorCol(0)
	case e.state.cursorLine > 0:
		e.pushUndoSnapshot()
		e.kill("\n", true)
		e.mergeWithPrevious()
	}
	e.changed()
}

func (e *Editor) deleteToEndOfLine() {
	e.exitHistoryBrowsing()
	line := e.currentLine()
	switch {
	case e.state.cursorCol < len(line):
		e.pushUndoSnapshot()
		e.kill(line[e.state.cursorCol:], false)
		e.setLine(line[:e.state.cursorCol])
	case e.state.cursorLine < len(e.state.lines)-1:
		e.pushUndoSnapshot()
		e.kill("\n", false)
		e.mergeWithNext()
	}
	e.changed()
}

func (e *Editor) deleteWordBackward() {
	e.exitHistoryBrowsing()
	line := e.currentLine()
	if e.state.cursorCol == 0 {
		if e.state.cursorLine > 0 {
			e.pushUndoSnapshot()
			e.kill("\n", true)
			e.mergeWithPrevious()
		}
	} else {
		e.pushUndoSnapshot()
		wasKill := e.lastAction == actionKill
		from := findWordBackward(line, e.state.cursorCol, e.words(line[:e.state.cursorCol]))
		e.killRing.push(line[from:e.state.cursorCol], true, wasKill)
		e.lastAction = actionKill
		e.setLine(line[:from] + line[e.state.cursorCol:])
		e.setCursorCol(from)
	}
	e.changed()
}

func (e *Editor) deleteWordForward() {
	e.exitHistoryBrowsing()
	line := e.currentLine()
	if e.state.cursorCol >= len(line) {
		if e.state.cursorLine < len(e.state.lines)-1 {
			e.pushUndoSnapshot()
			e.kill("\n", false)
			e.mergeWithNext()
		}
	} else {
		e.pushUndoSnapshot()
		wasKill := e.lastAction == actionKill
		to := findWordForward(line, e.state.cursorCol, e.words(line[e.state.cursorCol:]))
		e.killRing.push(line[e.state.cursorCol:to], false, wasKill)
		e.lastAction = actionKill
		e.setLine(line[:e.state.cursorCol] + line[to:])
	}
	e.changed()
}

// yank inserts the newest kill.
func (e *Editor) yank() {
	text, ok := e.killRing.peek()
	if !ok {
		return
	}
	e.pushUndoSnapshot()
	e.insertYankedText(text)
	e.lastAction = actionYank
}

// yankPop, straight after a yank, replaces what was yanked with the kill
// before it, cycling through the ring.
func (e *Editor) yankPop() {
	if e.lastAction != actionYank || len(e.killRing.ring) <= 1 {
		return
	}
	e.pushUndoSnapshot()
	e.deleteYankedText()
	e.killRing.rotate()
	text, _ := e.killRing.peek()
	e.insertYankedText(text)
	e.lastAction = actionYank
}

func (e *Editor) insertYankedText(text string) {
	e.exitHistoryBrowsing()
	e.insertTextAtCursorInternal(text)
}

func (e *Editor) deleteYankedText() {
	yanked, ok := e.killRing.peek()
	if !ok {
		return
	}
	yankedLines := strings.Split(yanked, "\n")
	if len(yankedLines) == 1 {
		line := e.currentLine()
		e.setLine(line[:e.state.cursorCol-len(yanked)] + line[e.state.cursorCol:])
		e.setCursorCol(e.state.cursorCol - len(yanked))
	} else {
		startLine := e.state.cursorLine - (len(yankedLines) - 1)
		startCol := len(e.state.lines[startLine]) - len(yankedLines[0])
		after := e.currentLine()[e.state.cursorCol:]
		before := e.state.lines[startLine][:startCol]
		e.state.lines = slices.Replace(e.state.lines, startLine, startLine+len(yankedLines), before+after)
		e.state.cursorLine = startLine
		e.setCursorCol(startCol)
	}
	e.changed()
}

// --- undo ---

func (e *Editor) pushUndoSnapshot() {
	e.undoStack = append(e.undoStack, editorSnapshot{state: e.state.clone(), pastes: e.pastes.clone()})
}

func (e *Editor) undo() {
	e.exitHistoryBrowsing()
	n := len(e.undoStack)
	if n == 0 {
		return
	}
	snapshot := e.undoStack[n-1]
	e.undoStack = e.undoStack[:n-1]
	e.state = snapshot.state
	e.pastes = snapshot.pastes
	e.lastAction = actionNone
	e.hasPreferredCol = false
	e.changed()
}

// --- paste registry and kill ring ---

// pasteRegistry holds the text behind each paste marker, remembering the
// order ids were added in, which is the order markers are expanded in.
type pasteRegistry struct {
	order   []int
	byID    map[int]string
	counter int
}

func newPasteRegistry() pasteRegistry { return pasteRegistry{byID: map[int]string{}} }

func (p *pasteRegistry) set(id int, content string) {
	if _, ok := p.byID[id]; !ok {
		p.order = append(p.order, id)
	}
	p.byID[id] = content
}

func (p *pasteRegistry) delete(id int) {
	if _, ok := p.byID[id]; !ok {
		return
	}
	delete(p.byID, id)
	p.order = slices.DeleteFunc(p.order, func(other int) bool { return other == id })
}

func (p pasteRegistry) valid() map[int]bool {
	valid := make(map[int]bool, len(p.byID))
	for id := range p.byID {
		valid[id] = true
	}
	return valid
}

func (p pasteRegistry) clone() pasteRegistry {
	return pasteRegistry{order: slices.Clone(p.order), byID: maps.Clone(p.byID), counter: p.counter}
}

func (e *Editor) expandPasteMarkers(text string) string {
	for _, id := range e.pastes.order {
		re := regexp.MustCompile(`\[paste #` + strconv.Itoa(id) + `( (\+\d+ lines|\d+ chars))?\]`)
		text = re.ReplaceAllLiteralString(text, e.pastes.byID[id])
	}
	return text
}

// killRing holds killed text, newest last.
type killRing struct{ ring []string }

func (k *killRing) push(text string, prepend, accumulate bool) {
	if text == "" {
		return
	}
	if accumulate && len(k.ring) > 0 {
		last := k.ring[len(k.ring)-1]
		if prepend {
			k.ring[len(k.ring)-1] = text + last
		} else {
			k.ring[len(k.ring)-1] = last + text
		}
		return
	}
	k.ring = append(k.ring, text)
}

func (k *killRing) peek() (string, bool) {
	if len(k.ring) == 0 {
		return "", false
	}
	return k.ring[len(k.ring)-1], true
}

// rotate moves the newest entry to the oldest position.
func (k *killRing) rotate() {
	if len(k.ring) > 1 {
		last := k.ring[len(k.ring)-1]
		k.ring = append([]string{last}, k.ring[:len(k.ring)-1]...)
	}
}

// --- autocomplete ---

// SetAutocompleteProvider sets where completions come from, closing any list
// on show.
func (e *Editor) SetAutocompleteProvider(provider AutocompleteProvider) {
	e.cancelAutocomplete()
	e.ac.provider = provider
	var triggers []string
	if t, ok := provider.(TriggerCharacterProvider); ok {
		triggers = t.TriggerCharacters()
	}
	e.setTriggerCharacters(triggers)
}

// IsShowingAutocomplete reports whether the completion list is open.
func (e *Editor) IsShowingAutocomplete() bool { return e.ac.mode != acNone }

// AutocompleteMaxVisible is how many completions show at once.
func (e *Editor) AutocompleteMaxVisible() int { return e.ac.maxVisible }

// SetAutocompleteMaxVisible changes how many completions show at once,
// clamped to 3..20.
func (e *Editor) SetAutocompleteMaxVisible(n int) {
	if n = max(3, min(20, n)); n != e.ac.maxVisible {
		e.ac.maxVisible = n
		e.requestRender()
	}
}

func (e *Editor) requestRender() {
	if e.host != nil {
		e.host.RequestRender()
	}
}

// setTriggerCharacters is @ and # plus the provider's own: single characters
// other than the slash and whitespace.
func (e *Editor) setTriggerCharacters(extra []string) {
	triggers := []string{"@", "#"}
	for _, c := range extra {
		if len(utf16.Encode([]rune(c))) != 1 || c == "/" || isWhitespace(c) || slices.Contains(triggers, c) {
			continue
		}
		triggers = append(triggers, c)
	}
	e.ac.triggers = triggers

	var class, others strings.Builder
	for _, t := range triggers {
		class.WriteString(regexp.QuoteMeta(t))
		if t != "@" {
			others.WriteString(regexp.QuoteMeta(t))
		}
	}
	// A trigger at the start of a word, followed by the word so far.
	e.ac.triggerPattern = regexp.MustCompile(`(?:^|\s)[` + class.String() + `][^\s]*$`)
	// What is worth pausing on before searching: an @-mention, quoted or
	// not, or a word after another trigger.
	e.ac.debouncePattern = regexp.MustCompile(`(?:^|[ \t])(?:@(?:"[^"]*|[^\s]*)|[` + others.String() + `][^\s]*)$`)
}

// isAtStartOfMessage: a slash command can begin only on the first line,
// with nothing but whitespace before it.
func (e *Editor) isAtStartOfMessage() bool {
	if e.state.cursorLine != 0 {
		return false
	}
	before := strings.TrimFunc(e.currentLine()[:e.state.cursorCol], jsSpace)
	return before == "" || before == "/"
}

func (e *Editor) isInSlashCommandContext(before string) bool {
	return e.state.cursorLine == 0 && strings.HasPrefix(strings.TrimLeftFunc(before, jsSpace), "/")
}

// handleAutocompleteKey handles a key while the list is open, reporting
// whether it was taken. Enter on a slash command applies it and goes on to
// submit.
func (e *Editor) handleAutocompleteKey(k Key) bool {
	kb := e.keys()
	switch {
	case kb.Matches(k, "tui.select.cancel"):
		e.cancelAutocomplete()
		return true
	case kb.Matches(k, "tui.select.up"), kb.Matches(k, "tui.select.down"):
		e.ac.list.HandleKey(k)
		return true
	case kb.Matches(k, "tui.input.tab"):
		if e.applySelectedCompletion() {
			e.cancelAutocomplete()
			e.changed()
		}
		return true
	case kb.Matches(k, "tui.select.confirm"):
		if !e.applySelectedCompletion() {
			return false
		}
		slash := strings.HasPrefix(e.ac.prefix, "/")
		e.cancelAutocomplete()
		if slash {
			return false
		}
		e.changed()
		return true
	}
	return false
}

// applySelectedCompletion puts the selected completion into the text as one
// undoable step.
func (e *Editor) applySelectedCompletion() bool {
	selected, ok := e.ac.list.SelectedItem()
	if !ok || e.ac.provider == nil {
		return false
	}
	e.applyCompletion(AutocompleteItem(selected), e.ac.prefix)
	return true
}

func (e *Editor) applyCompletion(item AutocompleteItem, prefix string) {
	e.pushUndoSnapshot()
	e.lastAction = actionNone
	lines, line, col := e.ac.provider.ApplyCompletion(slices.Clone(e.state.lines), e.state.cursorLine, e.state.cursorCol, item, prefix)
	e.state.lines = lines
	e.state.cursorLine = line
	e.setCursorCol(col)
}

// handleTabCompletion: tab in a slash command's name completes the command;
// anywhere else it completes a path.
func (e *Editor) handleTabCompletion() {
	if e.ac.provider == nil {
		return
	}
	before := e.currentLine()[:e.state.cursorCol]
	if e.isInSlashCommandContext(before) && !strings.Contains(strings.TrimLeftFunc(before, jsSpace), " ") {
		e.requestAutocomplete(false, true)
	} else {
		e.requestAutocomplete(true, true)
	}
}

func (e *Editor) updateAutocomplete() {
	if e.ac.mode == acNone || e.ac.provider == nil {
		return
	}
	e.requestAutocomplete(e.ac.mode == acForce, false)
}

// requestAutocomplete asks the provider for completions where the cursor
// is, replacing any request still pending. force is a tab's request for
// paths; explicitTab marks a request the user made rather than typing.
func (e *Editor) requestAutocomplete(force, explicitTab bool) {
	if e.ac.provider == nil {
		return
	}
	if force {
		if gate, ok := e.ac.provider.(FileCompletionGate); ok &&
			!gate.ShouldTriggerFileCompletion(e.state.lines, e.state.cursorLine, e.state.cursorCol) {
			return
		}
	}
	e.cancelAutocompleteRequest()
	token := e.ac.startToken

	host, async := e.host.(loopHost)
	if !async {
		e.startAutocompleteRequest(force, explicitTab)
		return
	}
	if !explicitTab && !force && e.ac.debouncePattern.MatchString(e.currentLine()[:e.state.cursorCol]) {
		e.ac.debounce = time.AfterFunc(attachmentDebounce, func() {
			host.Do(func() {
				if token == e.ac.startToken {
					e.ac.debounce = nil
					e.startAutocompleteRequest(force, explicitTab)
				}
			})
		})
		return
	}
	e.startAutocompleteRequest(force, explicitTab)
}

func (e *Editor) startAutocompleteRequest(force, explicitTab bool) {
	ctx, cancel := context.WithCancel(context.Background())
	e.ac.cancel = cancel
	e.ac.requestID++
	id := e.ac.requestID
	text, line, col := e.Text(), e.state.cursorLine, e.state.cursorCol
	lines := slices.Clone(e.state.lines)
	provider := e.ac.provider

	finish := func(s *AutocompleteSuggestions) {
		// Typing, moving or another request since makes the answer stale.
		if ctx.Err() != nil || id != e.ac.requestID || e.Text() != text ||
			e.state.cursorLine != line || e.state.cursorCol != col {
			return
		}
		e.ac.cancel = nil
		cancel()
		e.finishAutocomplete(s, force, explicitTab)
	}
	if host, ok := e.host.(loopHost); ok {
		go func() {
			s := provider.Suggestions(ctx, lines, line, col, force)
			host.Do(func() { finish(s) })
		}()
		return
	}
	finish(provider.Suggestions(ctx, lines, line, col, force))
}

func (e *Editor) finishAutocomplete(s *AutocompleteSuggestions, force, explicitTab bool) {
	defer e.requestRender()
	if s == nil || len(s.Items) == 0 {
		e.cancelAutocomplete()
		return
	}
	if force && explicitTab && len(s.Items) == 1 {
		// A tab with one answer takes it without showing a list.
		e.applyCompletion(s.Items[0], s.Prefix)
		e.changed()
		return
	}
	e.ac.prefix = s.Prefix
	items := make([]SelectItem, len(s.Items))
	for i, item := range s.Items {
		items[i] = SelectItem(item)
	}
	var layout SelectListLayout
	if strings.HasPrefix(s.Prefix, "/") {
		layout = SelectListLayout{MinPrimaryColumnWidth: 12, MaxPrimaryColumnWidth: 32}
	}
	e.ac.list = NewSelectList(items, e.ac.maxVisible, e.ac.theme, layout)
	e.ac.list.Keybindings = e.Keybindings
	if best := bestMatchIndex(s.Items, s.Prefix); best >= 0 {
		e.ac.list.SetSelectedIndex(best)
	}
	if force {
		e.ac.mode = acForce
	} else {
		e.ac.mode = acRegular
	}
}

// bestMatchIndex is the item to select first: one whose value is exactly
// the prefix, else the first that starts with it, else none.
func bestMatchIndex(items []AutocompleteItem, prefix string) int {
	if prefix == "" {
		return -1
	}
	first := -1
	for i, item := range items {
		if item.Value == prefix {
			return i
		}
		if first < 0 && strings.HasPrefix(item.Value, prefix) {
			first = i
		}
	}
	return first
}

func (e *Editor) cancelAutocompleteRequest() {
	e.ac.startToken++
	if e.ac.debounce != nil {
		e.ac.debounce.Stop()
		e.ac.debounce = nil
	}
	if e.ac.cancel != nil {
		e.ac.cancel()
		e.ac.cancel = nil
	}
}

func (e *Editor) cancelAutocomplete() {
	e.cancelAutocompleteRequest()
	e.ac.mode = acNone
	e.ac.list = nil
	e.ac.prefix = ""
}
