package tui

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"golang.org/x/term"
)

// TTY is the terminal the user is sitting at, opened through /dev/tty so the
// interface works even when stdin and stdout are redirected.
type TTY struct {
	file     *os.File
	previous *term.State

	mu        sync.Mutex
	stopping  bool
	readDone  chan struct{}
	stopWatch func()
}

// OpenTTY opens the controlling terminal.
func OpenTTY() (*TTY, error) {
	file, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("tui: no terminal to draw on: %w", err)
	}
	return &TTY{file: file}, nil
}

// Start enters raw mode, turns bracketed paste on — so a paste arrives as
// one piece rather than as keystrokes — and starts reading input and
// watching for size changes, as Pi's terminal does on start.
func (t *TTY) Start(onInput func([]byte), onResize func()) error {
	fd := int(t.file.Fd())
	previous, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	t.previous = previous
	if _, err := t.file.WriteString("\x1b[?2004h"); err != nil {
		_ = term.Restore(fd, previous)
		return fmt.Errorf("tui: %w", err)
	}
	t.stopWatch = watchResize(onResize)
	t.readDone = make(chan struct{})
	go t.read(onInput)
	// A size that changed while the process was suspended is not reported
	// again, so the first frame asks rather than trusting what it had.
	onResize()
	return nil
}

// pollInterval bounds how long the reader takes to notice it should stop.
const pollInterval = 50 * time.Millisecond

func (t *TTY) read(onInput func([]byte)) {
	defer close(t.readDone)
	buf := make([]byte, 4096)
	fd := int(t.file.Fd())
	for {
		t.mu.Lock()
		stopping := t.stopping
		t.mu.Unlock()
		if stopping {
			return
		}
		ready, err := waitReadable(fd, pollInterval)
		if err != nil {
			return
		}
		if !ready {
			continue
		}
		n, err := t.file.Read(buf)
		if n > 0 {
			onInput(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// Stop turns bracketed paste off, stops reading, and restores the mode Start
// found. Reading stops before raw mode ends, so a keystroke still in flight —
// a ctrl+d on its way — is not handed to the shell afterwards.
func (t *TTY) Stop() error {
	t.mu.Lock()
	t.stopping = true
	t.mu.Unlock()
	if t.stopWatch != nil {
		t.stopWatch()
	}
	_, _ = t.file.WriteString("\x1b[?2004l")
	if t.readDone != nil {
		select {
		case <-t.readDone:
		case <-time.After(time.Second):
		}
	}
	if t.previous != nil {
		if err := term.Restore(int(t.file.Fd()), t.previous); err != nil {
			return fmt.Errorf("tui: %w", err)
		}
	}
	return nil
}

// Close releases the terminal. Stop first.
func (t *TTY) Close() error { return t.file.Close() }

func (t *TTY) Write(p []byte) (int, error) { return t.file.Write(p) }

// Size is the terminal's size, or what COLUMNS and LINES say, or 80 by 24 —
// Pi's fallbacks, in its order.
func (t *TTY) Size() (int, int) {
	width, height, err := term.GetSize(int(t.file.Fd()))
	if err == nil && width > 0 && height > 0 {
		return width, height
	}
	width, height = 80, 24
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 {
		width = n
	}
	if n, err := strconv.Atoi(os.Getenv("LINES")); err == nil && n > 0 {
		height = n
	}
	return width, height
}
