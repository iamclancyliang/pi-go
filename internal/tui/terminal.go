package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// Prompter reads prompts from a real terminal, with editing.
//
// One instance per session, because the editor's history and kill ring are the
// session's: a prompter rebuilt per prompt would forget both between
// questions.
type Prompter struct {
	tty    *os.File
	editor *Editor
}

// NewPrompter opens the controlling terminal.
//
// The terminal, not stdin: stdin is the conversation's input and may be
// redirected, while editing happens where the user actually is.
func NewPrompter(history []string) (*Prompter, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("tui: no terminal to edit in: %w", err)
	}
	return &Prompter{tty: tty, editor: NewEditor(history)}, nil
}

// Close releases the terminal.
func (p *Prompter) Close() error { return p.tty.Close() }

// ReadLine edits one prompt to completion.
//
// Raw mode is entered per prompt and left before returning, so everything
// printed between prompts — answers, tool output, errors — goes to a terminal
// in its ordinary state. A terminal left raw by a crash mangles every line
// after it; the deferred restore runs on every path out, including panic.
func (p *Prompter) ReadLine(prompt string) (line string, ok bool, err error) {
	previous, err := term.MakeRaw(int(p.tty.Fd()))
	if err != nil {
		return "", false, fmt.Errorf("tui: %w", err)
	}
	defer term.Restore(int(p.tty.Fd()), previous)

	width, _, err := term.GetSize(int(p.tty.Fd()))
	if err != nil || width <= 0 {
		width = 80
	}

	var pending []byte
	buf := make([]byte, 256)
	rows := p.draw(prompt, width, 0)
	for {
		n, readErr := p.tty.Read(buf)
		if readErr != nil {
			return "", false, fmt.Errorf("tui: %w", readErr)
		}
		pending = append(pending, buf[:n]...)
		keys, rest := Decode(pending)
		if len(rest) > 0 && len(keys) == 0 && len(rest) == 1 && rest[0] == 0x1b {
			// A lone ESC: wait briefly for the rest of a sequence. If nothing
			// follows, it really was the escape key — which this editor leaves
			// unbound, as the plain prompt has nothing to cancel into.
			p.tty.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
			n, timedOut := p.tty.Read(buf)
			p.tty.SetReadDeadline(time.Time{})
			if timedOut == nil && n > 0 {
				pending = append(rest, buf[:n]...)
				keys, rest = Decode(pending)
			} else {
				rest = nil
			}
		}
		pending = rest

		for _, key := range keys {
			result := p.editor.Apply(key)
			if result.Submit {
				p.draw(prompt, width, rows)
				fmt.Fprint(p.tty, "\r\n")
				return result.Submitted, true, nil
			}
			if result.Exit {
				fmt.Fprint(p.tty, "\r\n")
				return "", false, nil
			}
		}
		rows = p.draw(prompt, width, rows)
	}
}

// draw repaints the prompt and buffer, returning how many terminal rows they
// occupy so the next repaint knows how far to climb.
//
// A full repaint per keystroke rather than incremental updates: the buffer is
// a prompt, not a document, and correctness beats cleverness at this size —
// every incremental-update bug is a cursor in the wrong place while the user
// is typing.
func (p *Prompter) draw(prompt string, width, previousRows int) int {
	text := p.editor.Text()
	cursor := p.editor.Cursor()

	// Lay the buffer out as the terminal will wrap it, tracking where the
	// cursor lands.
	var lines []string
	cursorRow, cursorCol := 0, 0
	row := ""
	col := 0
	push := func() {
		lines = append(lines, row)
		row, col = "", 0
	}
	for _, r := range prompt {
		row += string(r)
		col++
	}
	for i, r := range []rune(text) {
		if i == cursor {
			cursorRow, cursorCol = len(lines), col
		}
		if r == '\n' {
			push()
			continue
		}
		if col >= width-1 {
			push()
		}
		row += string(r)
		col++
	}
	if cursor == len([]rune(text)) {
		cursorRow, cursorCol = len(lines), col
	}
	push()

	var b strings.Builder
	// Climb to the first row of the previous paint and clear everything below.
	if previousRows > 1 {
		fmt.Fprintf(&b, "\x1b[%dA", previousRows-1)
	}
	b.WriteString("\r\x1b[J")
	for i, line := range lines {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(line)
	}
	// Put the cursor where the editor says it is.
	if up := len(lines) - 1 - cursorRow; up > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", up)
	}
	fmt.Fprintf(&b, "\r")
	if cursorCol > 0 {
		fmt.Fprintf(&b, "\x1b[%dC", cursorCol)
	}
	fmt.Fprint(p.tty, b.String())
	return len(lines)
}
