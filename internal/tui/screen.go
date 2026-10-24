package tui

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// lineReset ends every rendered line: a style reset and a closed hyperlink,
// so nothing a line opened runs on into the next — Pi's SEGMENT_RESET.
const lineReset = "\x1b[0m\x1b]8;;\x07"

// Synchronized output: the terminal holds the frame until the end marker, so
// a frame is never seen half drawn.
const (
	syncBegin = "\x1b[?2026h"
	syncEnd   = "\x1b[?2026l"
)

// mainScreen draws frames into the terminal's main screen, leaving what
// scrolls away in its scrollback — Pi's regular mode (tui-main-screen.ts).
//
// It is pure over its writer: each frame is the lines to show and the
// terminal's size, and what it writes is decided by the frame before. Which
// of five ways a frame is drawn is recorded in docs/product/
// pi-feature-inventory.md §11.4; the code follows that order.
type mainScreen struct {
	out io.Writer

	// clearOnShrink redraws everything when the content shrinks, clearing the
	// rows left behind. Off by default, as in Pi.
	clearOnShrink bool
	// showCursor shows the hardware cursor where the focused component put
	// its marker. It is positioned either way, for input methods.
	showCursor bool
	// keepOnHeightChange skips the full redraw a height change would cause.
	// Termux changes height whenever its soft keyboard shows, and a redraw
	// there replays the whole history on every toggle.
	keepOnHeightChange bool

	previous            []string
	previousWidth       int
	previousHeight      int
	hardwareCursorRow   int
	maxLinesRendered    int
	previousViewportTop int
	fullRedraws         int
}

// reset forgets what was drawn, so the next frame redraws everything.
func (s *mainScreen) reset() {
	s.previous = nil
	s.previousWidth, s.previousHeight = -1, -1
	s.hardwareCursorRow, s.maxLinesRendered, s.previousViewportTop = 0, 0, 0
}

// errLineTooWide is a component rendering a line wider than the terminal.
// The frame is refused: drawn, the terminal would wrap it, and every row
// after it would be one off from where the renderer believes it is.
type errLineTooWide struct {
	index, width, terminal int
	line                   string
}

func (e *errLineTooWide) Error() string {
	return fmt.Sprintf("tui: rendered line %d is %d cells wide on a %d-cell terminal; a component did not fit its output to the width: %q",
		e.index, e.width, e.terminal, StripSequences(e.line))
}

// frame draws lines on a terminal of the given size.
func (s *mainScreen) frame(lines []string, width, height int) error {
	widthChanged := s.previousWidth != 0 && s.previousWidth != width
	heightChanged := s.previousHeight != 0 && s.previousHeight != height
	bufferLength := height
	if s.previousHeight > 0 {
		bufferLength = s.previousViewportTop + s.previousHeight
	}
	prevViewportTop := s.previousViewportTop
	if heightChanged {
		prevViewportTop = max(0, bufferLength-height)
	}
	viewportTop := prevViewportTop
	hardwareRow := s.hardwareCursorRow
	lineDiff := func(target int) int {
		return (target - viewportTop) - (hardwareRow - prevViewportTop)
	}

	lines = append([]string(nil), lines...)
	cursorRow, cursorCol, hasCursor := extractCursor(lines, height)
	for i, line := range lines {
		lines[i] = normalizeOutput(line) + lineReset
	}
	// Only lines about to be written are measured, as in Pi: a long
	// transcript re-measured whole on every frame would make each keystroke
	// cost the length of the conversation.
	fits := func(from, to int) error {
		for i := from; i <= to && i < len(lines); i++ {
			if w := VisibleWidth(lines[i]); w > width {
				return &errLineTooWide{index: i, width: w, terminal: width, line: lines[i]}
			}
		}
		return nil
	}

	full := func(clear bool) error {
		if err := fits(0, len(lines)-1); err != nil {
			return err
		}
		s.fullRedraws++
		var b strings.Builder
		b.WriteString(syncBegin)
		if clear {
			// Screen, home, then scrollback: a width change rewraps
			// everything, and what scrolled away was wrapped for the old width.
			b.WriteString("\x1b[2J\x1b[H\x1b[3J")
		}
		for i, line := range lines {
			if i > 0 {
				b.WriteString("\r\n")
			}
			b.WriteString(line)
		}
		b.WriteString(syncEnd)
		s.write(b.String())
		s.hardwareCursorRow = max(0, len(lines)-1)
		if clear {
			s.maxLinesRendered = len(lines)
		} else {
			s.maxLinesRendered = max(s.maxLinesRendered, len(lines))
		}
		s.previousViewportTop = max(0, max(height, len(lines))-height)
		s.placeCursor(cursorRow, cursorCol, hasCursor, len(lines))
		s.previous, s.previousWidth, s.previousHeight = lines, width, height
		return nil
	}

	switch {
	case len(s.previous) == 0 && !widthChanged && !heightChanged:
		return full(false) // first frame: the screen below the prompt is assumed clean
	case widthChanged:
		return full(true)
	case heightChanged && !s.keepOnHeightChange:
		return full(true)
	case s.clearOnShrink && len(lines) < s.maxLinesRendered:
		return full(true)
	}

	first, last := -1, -1
	for i := 0; i < max(len(lines), len(s.previous)); i++ {
		var was, now string
		if i < len(s.previous) {
			was = s.previous[i]
		}
		if i < len(lines) {
			now = lines[i]
		}
		if was != now {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	appended := len(lines) > len(s.previous)
	if appended {
		if first < 0 {
			first = len(s.previous)
		}
		last = len(lines) - 1
	}
	appendStart := appended && first == len(s.previous) && first > 0

	if first < 0 {
		// Nothing changed; the cursor may still have moved.
		s.placeCursor(cursorRow, cursorCol, hasCursor, len(lines))
		s.previousViewportTop, s.previousHeight = prevViewportTop, height
		return nil
	}

	if first >= len(lines) {
		// Only lines removed from the end: clear them where they are.
		if len(s.previous) > len(lines) {
			target := max(0, len(lines)-1)
			if target < prevViewportTop {
				return full(true)
			}
			extra := len(s.previous) - len(lines)
			if extra > height {
				return full(true)
			}
			var b strings.Builder
			b.WriteString(syncBegin)
			b.WriteString(moveRows(lineDiff(target)))
			b.WriteString("\r")
			offset := 1
			if len(lines) == 0 {
				offset = 0
			}
			if offset > 0 {
				b.WriteString("\x1b[1B")
			}
			for i := 0; i < extra; i++ {
				b.WriteString("\r\x1b[2K")
				if i < extra-1 {
					b.WriteString("\x1b[1B")
				}
			}
			if back := extra - 1 + offset; back > 0 {
				b.WriteString("\x1b[" + strconv.Itoa(back) + "A")
			}
			b.WriteString(syncEnd)
			s.write(b.String())
			s.hardwareCursorRow = target
		}
		s.placeCursor(cursorRow, cursorCol, hasCursor, len(lines))
		s.previous, s.previousWidth, s.previousHeight = lines, width, height
		s.previousViewportTop = prevViewportTop
		return nil
	}

	if first < prevViewportTop {
		// What changed has scrolled out of reach; only a redraw can fix it.
		return full(true)
	}
	end := min(last, len(lines)-1)
	if err := fits(first, end); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString(syncBegin)
	moveTarget := first
	if appendStart {
		moveTarget = first - 1
	}
	if bottom := prevViewportTop + height - 1; moveTarget > bottom {
		// The change is below the screen: move to the bottom and scroll down
		// to it with newlines, which pushes the top into scrollback.
		screenRow := max(0, min(height-1, hardwareRow-prevViewportTop))
		if down := height - 1 - screenRow; down > 0 {
			b.WriteString("\x1b[" + strconv.Itoa(down) + "B")
		}
		scroll := moveTarget - bottom
		b.WriteString(strings.Repeat("\r\n", scroll))
		prevViewportTop += scroll
		viewportTop += scroll
		hardwareRow = moveTarget
	}
	b.WriteString(moveRows(lineDiff(moveTarget)))
	if appendStart {
		b.WriteString("\r\n")
	} else {
		b.WriteString("\r")
	}
	// Only the changed range, not everything after it: a spinner's frame
	// rewrites one line, not the screen.
	for i := first; i <= end; i++ {
		if i > first {
			b.WriteString("\r\n")
		}
		b.WriteString("\x1b[2K")
		b.WriteString(lines[i])
	}
	finalRow := end

	if len(s.previous) > len(lines) {
		// The content also shrank: clear the rows below the new end.
		if end < len(lines)-1 {
			b.WriteString(moveRows(len(lines) - 1 - end))
			finalRow = len(lines) - 1
		}
		extra := len(s.previous) - len(lines)
		for i := 0; i < extra; i++ {
			b.WriteString("\r\n\x1b[2K")
		}
		b.WriteString("\x1b[" + strconv.Itoa(extra) + "A")
	}
	b.WriteString(syncEnd)
	s.write(b.String())

	s.hardwareCursorRow = finalRow
	s.maxLinesRendered = max(s.maxLinesRendered, len(lines))
	s.previousViewportTop = max(prevViewportTop, finalRow-height+1)
	s.placeCursor(cursorRow, cursorCol, hasCursor, len(lines))
	s.previous, s.previousWidth, s.previousHeight = lines, width, height
	return nil
}

// placeCursor moves the hardware cursor to the focused component's marker,
// or hides it when there is none.
func (s *mainScreen) placeCursor(row, col int, has bool, total int) {
	if !has || total <= 0 {
		s.write("\x1b[?25l")
		return
	}
	target := max(0, min(row, total-1))
	seq := moveRows(target-s.hardwareCursorRow) + "\x1b[" + strconv.Itoa(max(0, col)+1) + "G"
	s.hardwareCursorRow = target
	if s.showCursor {
		seq += "\x1b[?25h"
	} else {
		seq += "\x1b[?25l"
	}
	s.write(seq)
}

// leave moves below what was drawn, so the shell's prompt starts on a fresh
// line after the interface stops.
func (s *mainScreen) leave() {
	if len(s.previous) == 0 {
		return
	}
	s.write(" " + moveRows(len(s.previous)-s.hardwareCursorRow) + "\r\n")
}

func (s *mainScreen) write(seq string) {
	if seq != "" {
		_, _ = io.WriteString(s.out, seq)
	}
}

func moveRows(n int) string {
	switch {
	case n > 0:
		return "\x1b[" + strconv.Itoa(n) + "B"
	case n < 0:
		return "\x1b[" + strconv.Itoa(-n) + "A"
	}
	return ""
}

// extractCursor finds the cursor marker in the visible lines — the bottom
// height of them, searched from the bottom — removes it, and reports where it
// was.
func extractCursor(lines []string, height int) (row, col int, ok bool) {
	top := max(0, len(lines)-height)
	for row = len(lines) - 1; row >= top; row-- {
		if i := strings.Index(lines[row], CursorMarker); i >= 0 {
			col = VisibleWidth(lines[row][:i])
			lines[row] = lines[row][:i] + lines[row][i+len(CursorMarker):]
			return row, col, true
		}
	}
	return 0, 0, false
}
