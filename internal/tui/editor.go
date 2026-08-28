package tui

import "strings"

// Editor is the prompt being composed: a buffer, a cursor, and the memory a
// line editor is expected to have — deleted text that can be yanked back,
// undo, and the history of what was sent before.
//
// Pure: a keystroke's effect is a function of this state, which is what makes
// every behaviour here testable without a terminal.
type Editor struct {
	buffer []rune
	cursor int

	// killRing holds deleted stretches, newest first. Deleting twice in a row
	// grows the newest entry rather than starting one, because "delete three
	// words then yank" should paste three words, not one.
	killRing    []string
	lastKilled  bool
	yankedFrom  int
	yankedRange [2]int

	// undo holds prior states. Snapshots rather than inverse operations: an
	// editor this size does not need the cleverness, and a snapshot cannot be
	// wrong about how to invert something.
	undo []editorState

	// history is what was sent, oldest first. recalled is where the user has
	// browsed to; stash holds the line they left to go browsing, so coming
	// back down restores it rather than losing it.
	history  []string
	recalled int
	stash    string
}

type editorState struct {
	buffer string
	cursor int
}

// NewEditor returns an empty editor carrying the given history.
func NewEditor(history []string) *Editor {
	e := &Editor{history: append([]string(nil), history...)}
	e.recalled = len(e.history)
	return e
}

// Text is the buffer as it stands.
func (e *Editor) Text() string { return string(e.buffer) }

// Cursor is where the next insertion lands, as a rune offset.
func (e *Editor) Cursor() int { return e.cursor }

// Result is what one keystroke did, beyond editing.
type Result struct {
	// Submitted is the prompt, when the keystroke sent it.
	Submitted string
	// Submit reports that it was sent. A separate field because empty input
	// can be submitted and must be distinguishable from not submitting.
	Submit bool
	// Exit reports ctrl+d on an empty buffer.
	Exit bool
}

// Apply advances the editor by one keystroke.
func (e *Editor) Apply(k Key) Result {
	action := actionFor(k)

	// Anything but a repeated delete breaks the kill-accumulation chain, and
	// anything but a yank invalidates yank-pop's target.
	defer func() {
		switch action {
		case "editor.deleteWordBackward", "editor.deleteWordForward",
			"editor.deleteToLineStart", "editor.deleteToLineEnd",
			"editor.yank", "editor.yankPop":
		default:
			e.lastKilled = false
			e.yankedFrom = -1
		}
	}()

	switch action {
	case "insert":
		e.snapshotForText()
		e.insert(string(k.Text))
	case "input.newLine":
		e.snapshot()
		e.insert("\n")
	case "input.submit":
		text := string(e.buffer)
		e.remember(text)
		e.buffer, e.cursor = nil, 0
		e.undo = nil
		return Result{Submitted: text, Submit: true}
	case "app.exit":
		if len(e.buffer) == 0 {
			return Result{Exit: true}
		}
	case "editor.cursorLeft":
		if e.cursor > 0 {
			e.cursor--
		}
	case "editor.cursorRight":
		if e.cursor < len(e.buffer) {
			e.cursor++
		}
	case "editor.cursorWordLeft":
		e.cursor = e.wordLeft()
	case "editor.cursorWordRight":
		e.cursor = e.wordRight()
	case "editor.cursorLineStart":
		e.cursor = e.lineStart()
	case "editor.cursorLineEnd":
		e.cursor = e.lineEnd()
	case "editor.cursorUp":
		e.upOrHistoryBack()
	case "editor.cursorDown":
		e.downOrHistoryForward()
	case "editor.deleteCharBackward":
		if e.cursor > 0 {
			e.snapshot()
			e.deleteRange(e.cursor-1, e.cursor)
		}
	case "editor.deleteCharForward":
		// Ctrl+D doubles as end-of-input, and which it is depends on the
		// buffer: with nothing typed it is the exit a shell user means. The
		// delete key never exits — only the chord with that history does.
		if len(e.buffer) == 0 && k.Name == "ctrl+d" {
			return Result{Exit: true}
		}
		if e.cursor < len(e.buffer) {
			e.snapshot()
			e.deleteRange(e.cursor, e.cursor+1)
		}
	case "editor.deleteWordBackward":
		e.kill(e.wordLeft(), e.cursor, true)
	case "editor.deleteWordForward":
		e.kill(e.cursor, e.wordRight(), false)
	case "editor.deleteToLineStart":
		e.kill(e.lineStart(), e.cursor, true)
	case "editor.deleteToLineEnd":
		e.kill(e.cursor, e.lineEnd(), false)
	case "editor.yank":
		if len(e.killRing) > 0 {
			e.snapshot()
			e.yankedFrom = 0
			start := e.cursor
			e.insert(e.killRing[0])
			e.yankedRange = [2]int{start, e.cursor}
		}
	case "editor.yankPop":
		// Only straight after a yank: pop replaces what the yank pasted, and
		// with nothing just pasted there is nothing to replace.
		if e.yankedFrom >= 0 && len(e.killRing) > 1 {
			e.yankedFrom = (e.yankedFrom + 1) % len(e.killRing)
			e.deleteRange(e.yankedRange[0], e.yankedRange[1])
			start := e.cursor
			e.insert(e.killRing[e.yankedFrom])
			e.yankedRange = [2]int{start, e.cursor}
		}
	case "editor.undo":
		if n := len(e.undo); n > 0 {
			state := e.undo[n-1]
			e.undo = e.undo[:n-1]
			e.buffer = []rune(state.buffer)
			e.cursor = state.cursor
		}
	}
	return Result{}
}

func (e *Editor) insert(text string) {
	inserted := []rune(text)
	e.buffer = append(e.buffer[:e.cursor], append(inserted, e.buffer[e.cursor:]...)...)
	e.cursor += len(inserted)
}

func (e *Editor) deleteRange(from, to int) {
	if from < 0 {
		from = 0
	}
	if to > len(e.buffer) {
		to = len(e.buffer)
	}
	if from >= to {
		return
	}
	e.buffer = append(e.buffer[:from], e.buffer[to:]...)
	if e.cursor > to {
		e.cursor -= to - from
	} else if e.cursor > from {
		e.cursor = from
	}
}

// kill deletes a range and remembers it. Consecutive kills accumulate into one
// ring entry — backward kills prepend, forward kills append — so "delete three
// words then yank" pastes three words.
func (e *Editor) kill(from, to int, backward bool) {
	if from >= to {
		return
	}
	e.snapshot()
	text := string(e.buffer[from:to])
	if e.lastKilled && len(e.killRing) > 0 {
		if backward {
			e.killRing[0] = text + e.killRing[0]
		} else {
			e.killRing[0] += text
		}
	} else {
		e.killRing = append([]string{text}, e.killRing...)
		if len(e.killRing) > 32 {
			e.killRing = e.killRing[:32]
		}
	}
	e.deleteRange(from, to)
	e.lastKilled = true
}

// snapshot records the state for undo.
func (e *Editor) snapshot() {
	e.undo = append(e.undo, editorState{buffer: string(e.buffer), cursor: e.cursor})
	if len(e.undo) > 200 {
		e.undo = e.undo[1:]
	}
}

// snapshotForText groups consecutive typing into one undo step: undoing a
// sentence letter by letter is two hundred keystrokes of undo nobody wants.
func (e *Editor) snapshotForText() {
	if n := len(e.undo); n > 0 {
		last := e.undo[n-1]
		// Still typing at the point the last snapshot expected: extend it.
		if e.cursor == len([]rune(last.buffer))+(e.cursor-last.cursor) &&
			e.cursor >= last.cursor && e.cursor-last.cursor < 32 &&
			strings.HasPrefix(string(e.buffer), last.buffer[:min(len(last.buffer), len(string(e.buffer)))]) {
			return
		}
	}
	e.snapshot()
}

func (e *Editor) lineStart() int {
	at := e.cursor
	for at > 0 && e.buffer[at-1] != '\n' {
		at--
	}
	return at
}

func (e *Editor) lineEnd() int {
	at := e.cursor
	for at < len(e.buffer) && e.buffer[at] != '\n' {
		at++
	}
	return at
}

func (e *Editor) wordLeft() int {
	at := e.cursor
	for at > 0 && isWordGap(e.buffer[at-1]) {
		at--
	}
	for at > 0 && !isWordGap(e.buffer[at-1]) {
		at--
	}
	return at
}

func (e *Editor) wordRight() int {
	at := e.cursor
	for at < len(e.buffer) && isWordGap(e.buffer[at]) {
		at++
	}
	for at < len(e.buffer) && !isWordGap(e.buffer[at]) {
		at++
	}
	return at
}

func isWordGap(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }

// upOrHistoryBack moves up a line, or into history from the first line.
func (e *Editor) upOrHistoryBack() {
	if e.lineStart() > 0 {
		e.moveVertically(-1)
		return
	}
	if e.recalled == 0 {
		return
	}
	if e.recalled == len(e.history) {
		// Leaving the line being written: kept, so coming back restores it.
		e.stash = string(e.buffer)
	}
	e.recalled--
	e.buffer = []rune(e.history[e.recalled])
	e.cursor = len(e.buffer)
}

// downOrHistoryForward moves down a line, or forward through history from the
// last one, restoring the stashed line at the end.
func (e *Editor) downOrHistoryForward() {
	if e.lineEnd() < len(e.buffer) {
		e.moveVertically(1)
		return
	}
	if e.recalled >= len(e.history) {
		return
	}
	e.recalled++
	if e.recalled == len(e.history) {
		e.buffer = []rune(e.stash)
	} else {
		e.buffer = []rune(e.history[e.recalled])
	}
	e.cursor = len(e.buffer)
}

// moveVertically keeps the column while changing line, clamped to the shorter
// line's end.
func (e *Editor) moveVertically(direction int) {
	column := e.cursor - e.lineStart()
	if direction < 0 {
		previousEnd := e.lineStart() - 1
		start := previousEnd
		for start > 0 && e.buffer[start-1] != '\n' {
			start--
		}
		e.cursor = min(start+column, previousEnd)
		return
	}
	nextStart := e.lineEnd() + 1
	end := nextStart
	for end < len(e.buffer) && e.buffer[end] != '\n' {
		end++
	}
	e.cursor = min(nextStart+column, end)
}

// remember appends to history, skipping empties and immediate repeats — the
// second identical prompt in a row is one entry, as every shell does it.
func (e *Editor) remember(text string) {
	if strings.TrimSpace(text) != "" &&
		(len(e.history) == 0 || e.history[len(e.history)-1] != text) {
		e.history = append(e.history, text)
	}
	e.recalled = len(e.history)
	e.stash = ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
