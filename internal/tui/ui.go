package tui

import (
	"os"
	"strconv"
	"sync"
	"time"
)

// Terminal is what the interface draws on and reads keystrokes from.
type Terminal interface {
	// Start enters the mode the interface needs — raw input, bracketed
	// paste — and begins delivering input and size changes. Both callbacks
	// may be called from any goroutine.
	Start(onInput func([]byte), onResize func()) error
	// Stop restores the terminal to how Start found it.
	Stop() error
	Write(p []byte) (int, error)
	// Size is the terminal's width and height in cells.
	Size() (width, height int)
}

// KeyboardProtocol is a terminal that negotiates a keyboard protocol: it
// says whether Kitty's is on, and acts on the terminal's answers to its
// query, which arrive among the keystrokes.
type KeyboardProtocol interface {
	KittyActive() bool
	HandleReport(report string)
}

// minFrameInterval is the shortest time between two throttled frames — Pi's
// MIN_RENDER_INTERVAL_MS. Requests in between are folded into one frame;
// keystrokes do not wait for it.
const minFrameInterval = 16 * time.Millisecond

// sequenceTimeout is how long an incomplete escape sequence is waited on
// before it is dropped — Pi's stdin buffer's 50 ms.
const sequenceTimeout = 50 * time.Millisecond

// UI is the interface: a root container drawn on a terminal's main screen,
// with keystrokes delivered to the focused component — Pi's regular-mode TUI
// (tui.ts, tui-main-screen.ts). Components are added to its embedded
// Container.
//
// Everything that touches a component — rendering, a keystroke, a callback
// posted with Do — runs on one goroutine, the loop Run drives. Pi runs on one
// thread and its components are written for that: a key handler asks for a
// frame, moves focus or stops the interface without a thought for locks.
// Here those calls, from a handler or from anywhere else, only queue work for
// the loop, so they are safe in both places.
type UI struct {
	Container

	term       Terminal
	screen     mainScreen
	escTimeout time.Duration

	// Loop state: touched only on the loop goroutine.
	focused    Component
	listeners  []func(Key) bool
	input      []byte
	inputTimer *time.Timer
	lastRender time.Time

	queue    sync.Mutex
	work     []func()
	wake     chan struct{}
	pending  bool // a throttled frame is scheduled
	stopping bool
	failure  error
}

// NewUI is an interface on term. The environment switches Pi's switches:
// PI_HARDWARE_CURSOR=1 shows the hardware cursor, PI_CLEAR_ON_SHRINK=1
// redraws to clear rows a shrink left behind, PI_TUI_ESC_TIMEOUT sets how
// long a lone escape is waited on, and Termux keeps the screen through the
// height changes its soft keyboard causes.
func NewUI(term Terminal) *UI {
	return &UI{
		term:       term,
		escTimeout: escapeTimeout(os.Getenv),
		wake:       make(chan struct{}, 1),
		screen: mainScreen{
			out:                term,
			showCursor:         os.Getenv("PI_HARDWARE_CURSOR") == "1",
			clearOnShrink:      os.Getenv("PI_CLEAR_ON_SHRINK") == "1",
			keepOnHeightChange: os.Getenv("TERMUX_VERSION") != "",
		},
	}
}

// escapeTimeout is how long a lone escape byte is waited on before it is
// taken for the escape key rather than the start of a sequence: 10 ms
// locally, 100 ms over SSH, where the rest of an alt+key can arrive later —
// Pi's resolveEscapeTimeoutMs.
func escapeTimeout(getenv func(string) string) time.Duration {
	if ms, err := strconv.ParseFloat(getenv("PI_TUI_ESC_TIMEOUT"), 64); err == nil && ms > 0 {
		return time.Duration(ms * float64(time.Millisecond))
	}
	if getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != "" {
		return 100 * time.Millisecond
	}
	return 10 * time.Millisecond
}

// Run takes over the terminal, draws, and delivers keystrokes until Stop is
// called or rendering fails; then it leaves the cursor below the interface,
// gives the terminal back, and returns the failure if there was one — a
// component that drew a line wider than the terminal.
func (u *UI) Run() error {
	if err := u.term.Start(u.receive, u.RequestRender); err != nil {
		return err
	}
	_, _ = u.term.Write([]byte("\x1b[?25l"))
	u.RequestRender()
	for {
		<-u.wake
		u.queue.Lock()
		work := u.work
		u.work = nil
		u.queue.Unlock()
		for _, fn := range work {
			fn()
		}
		u.queue.Lock()
		stopping, failure := u.stopping, u.failure
		u.queue.Unlock()
		if stopping {
			if failure == nil {
				u.screen.leave()
			}
			if u.inputTimer != nil {
				u.inputTimer.Stop()
			}
			_, _ = u.term.Write([]byte("\x1b[?25h"))
			if err := u.term.Stop(); err != nil && failure == nil {
				failure = err
			}
			return failure
		}
	}
}

// Do runs fn on the loop, where it may touch components. It is how anything
// outside a keystroke — a reply arriving, a timer — changes what is shown.
func (u *UI) Do(fn func()) {
	u.queue.Lock()
	u.work = append(u.work, fn)
	u.queue.Unlock()
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

// Stop ends Run after the work already queued.
func (u *UI) Stop() {
	u.queue.Lock()
	u.stopping = true
	u.queue.Unlock()
	u.Do(func() {})
}

// SetFocus gives keystrokes to c, telling the components that gain and lose
// focus.
func (u *UI) SetFocus(c Component) {
	u.Do(func() {
		previous := u.focused
		u.focused = c
		if f, ok := previous.(Focusable); ok && previous != c {
			f.SetFocused(false)
		}
		if f, ok := c.(Focusable); ok {
			f.SetFocused(true)
		}
		u.renderFrame()
	})
}

// AddInputListener sees every keystroke before the focused component, and
// takes it when it returns true.
func (u *UI) AddInputListener(listener func(Key) bool) {
	u.Do(func() { u.listeners = append(u.listeners, listener) })
}

// RequestRender asks for a frame. Requests are folded into one frame at most
// every 16 ms.
func (u *UI) RequestRender() {
	u.queue.Lock()
	if u.pending || u.stopping {
		u.queue.Unlock()
		return
	}
	u.pending = true
	u.queue.Unlock()
	u.Do(func() {
		delay := max(0, minFrameInterval-time.Since(u.lastRender))
		time.AfterFunc(delay, func() { u.Do(u.renderFrame) })
	})
}

// Rows is the terminal's height, which components such as the editor size
// themselves by.
func (u *UI) Rows() int {
	_, height := u.term.Size()
	return height
}

// RenderNow draws a frame on the loop at once, redrawing everything when
// force is set.
func (u *UI) RenderNow(force bool) {
	u.Do(func() {
		if force {
			u.screen.reset()
		}
		u.renderFrame()
	})
}

// renderFrame draws one frame. Loop only.
func (u *UI) renderFrame() {
	u.queue.Lock()
	if u.stopping {
		u.queue.Unlock()
		return
	}
	u.pending = false
	u.queue.Unlock()
	u.lastRender = time.Now()
	width, height := u.term.Size()
	if err := u.screen.frame(u.Container.Render(width), width, height); err != nil {
		u.queue.Lock()
		u.stopping, u.failure = true, err
		u.queue.Unlock()
	}
}

// receive takes bytes from the terminal: whole keys are delivered, and a tail
// that may be the start of a longer sequence waits a moment for the rest.
func (u *UI) receive(data []byte) {
	data = append([]byte(nil), data...)
	u.Do(func() {
		if u.inputTimer != nil {
			u.inputTimer.Stop()
			u.inputTimer = nil
		}
		u.input = append(u.input, data...)
		protocol, _ := u.term.(KeyboardProtocol)
		keys, rest := DecodeKeys(u.input, protocol != nil && protocol.KittyActive())
		u.input = append([]byte(nil), rest...)
		if len(rest) > 0 {
			wait := sequenceTimeout
			if len(rest) == 1 && rest[0] == 0x1b {
				wait = u.escTimeout
			}
			held := u.input
			u.inputTimer = time.AfterFunc(wait, func() { u.Do(func() { u.flushInput(held) }) })
		}
		u.deliver(keys)
	})
}

// flushInput resolves input left waiting, unless more arrived since: a lone
// escape byte is the escape key; anything else incomplete is dropped, since
// typing it into the focused component would put invisible control bytes in
// its text.
func (u *UI) flushInput(held []byte) {
	if string(u.input) != string(held) {
		return
	}
	u.input, u.inputTimer = nil, nil
	if len(held) == 1 && held[0] == 0x1b {
		u.deliver([]Key{{Name: "escape"}})
	}
}

// deliver hands keys to the listeners and then the focused component, and
// draws their effect at once rather than at the next throttled frame:
// typing is where latency shows. Loop only.
func (u *UI) deliver(keys []Key) {
	if len(keys) == 0 {
		return
	}
	protocol, _ := u.term.(KeyboardProtocol)
	for _, key := range keys {
		if key.Report != "" {
			if protocol != nil {
				protocol.HandleReport(key.Report)
			}
			continue
		}
		if key.Release {
			// A release reports a key let go, not one pressed; no component
			// here asks for them.
			continue
		}
		taken := false
		for _, listener := range u.listeners {
			if listener(key) {
				taken = true
				break
			}
		}
		if handler, ok := u.focused.(KeyHandler); ok && !taken {
			handler.HandleKey(key)
		}
	}
	u.renderFrame()
}
