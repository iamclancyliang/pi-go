package tui

import (
	"strings"
	"sync"
	"time"
)

// Component is a piece of the interface: it renders itself to lines for a
// width, Pi's model (tui.ts). Every line must fit the width — the renderer
// refuses to draw one that does not — and Render may cache, which is what
// Invalidate is for.
type Component interface {
	Render(width int) []string
	Invalidate()
}

// KeyHandler is a component that takes keystrokes while it has focus.
type KeyHandler interface {
	HandleKey(Key)
}

// Focusable is a component told when it gains and loses focus. A focused one
// puts CursorMarker in its output where its cursor is.
type Focusable interface {
	SetFocused(bool)
}

// CursorMarker marks where a focused component's cursor is. It is an APC
// sequence, which terminals ignore and widths skip; the renderer finds it,
// removes it and moves the hardware cursor there, which is what places an
// input method's candidate window — Pi's CURSOR_MARKER, byte for byte.
const CursorMarker = "\x1b_pi:c\x07"

// RenderRequester is what a component calls when its state changed outside
// a keystroke — a spinner's next frame, a reply arriving.
type RenderRequester interface {
	RequestRender()
}

// Container renders its children one after another.
type Container struct {
	mu       sync.Mutex
	children []Component
}

func (c *Container) AddChild(child Component) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.children = append(c.children, child)
}

func (c *Container) RemoveChild(child Component) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, existing := range c.children {
		if existing == child {
			c.children = append(c.children[:i], c.children[i+1:]...)
			return
		}
	}
}

func (c *Container) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.children = nil
}

// Children is a copy of the children, in order.
func (c *Container) Children() []Component {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Component(nil), c.children...)
}

func (c *Container) Invalidate() {
	for _, child := range c.Children() {
		child.Invalidate()
	}
}

func (c *Container) Render(width int) []string {
	var lines []string
	for _, child := range c.Children() {
		lines = append(lines, child.Render(width)...)
	}
	return lines
}

// Style turns text into styled text — a colour or a background. Nil leaves
// text as it is.
type Style func(string) string

func (s Style) apply(text string) string {
	if s == nil {
		return text
	}
	return s(text)
}

// withBackground pads line to width and applies bg over the whole of it, so
// the background reaches the right edge rather than stopping at the text.
func withBackground(line string, width int, bg Style) string {
	return bg.apply(PadToWidth(line, width))
}

// Text is wrapped text with padding around it — Pi's Text. Empty or blank
// text renders nothing at all, not padding around nothing.
type Text struct {
	mu                 sync.Mutex
	text               string
	paddingX, paddingY int
	background         Style

	cachedText  string
	cachedWidth int
	cached      []string
}

// NewText is Text with the given padding and an optional background.
func NewText(text string, paddingX, paddingY int, background Style) *Text {
	return &Text{text: text, paddingX: paddingX, paddingY: paddingY, background: background}
}

func (t *Text) SetText(text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.text = text
	t.cached = nil
}

func (t *Text) Invalidate() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cached = nil
}

func (t *Text) Render(width int) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cached != nil && t.cachedText == t.text && t.cachedWidth == width {
		return t.cached
	}
	t.cachedText, t.cachedWidth = t.text, width
	if strings.TrimSpace(t.text) == "" {
		t.cached = []string{}
		return t.cached
	}

	content := max(1, width-2*t.paddingX)
	margin := strings.Repeat(" ", t.paddingX)
	var lines []string
	for range t.paddingY {
		lines = append(lines, withBackground("", width, t.background))
	}
	for _, line := range WrapText(strings.ReplaceAll(t.text, "\t", "   "), content) {
		lines = append(lines, withBackground(margin+line+margin, width, t.background))
	}
	for range t.paddingY {
		lines = append(lines, withBackground("", width, t.background))
	}
	t.cached = lines
	return lines
}

// Spacer is blank lines.
type Spacer struct{ Lines int }

func (s *Spacer) Invalidate() {}

func (s *Spacer) Render(int) []string { return make([]string, s.Lines) }

// Box is children with padding and an optional background around them —
// Pi's Box. With no children, or children rendering nothing, it renders
// nothing.
type Box struct {
	Container
	paddingX, paddingY int
	background         Style
}

// NewBox is a Box with the given padding and an optional background.
func NewBox(paddingX, paddingY int, background Style) *Box {
	return &Box{paddingX: paddingX, paddingY: paddingY, background: background}
}

func (b *Box) Render(width int) []string {
	content := max(1, width-2*b.paddingX)
	pad := strings.Repeat(" ", b.paddingX)
	var inner []string
	for _, child := range b.Children() {
		for _, line := range child.Render(content) {
			inner = append(inner, pad+line)
		}
	}
	if len(inner) == 0 {
		return nil
	}
	var lines []string
	for range b.paddingY {
		lines = append(lines, withBackground("", width, b.background))
	}
	for _, line := range inner {
		lines = append(lines, withBackground(line, width, b.background))
	}
	for range b.paddingY {
		lines = append(lines, withBackground("", width, b.background))
	}
	return lines
}

// TruncatedText is one line, cut to fit with an ellipsis — Pi's
// TruncatedText. Only the text before the first line break is shown.
type TruncatedText struct {
	Text               string
	PaddingX, PaddingY int
}

func (t *TruncatedText) Invalidate() {}

func (t *TruncatedText) Render(width int) []string {
	empty := strings.Repeat(" ", width)
	var lines []string
	for range t.PaddingY {
		lines = append(lines, empty)
	}
	line, _, _ := strings.Cut(t.Text, "\n")
	pad := strings.Repeat(" ", t.PaddingX)
	shown := TruncateToWidth(line, max(1, width-2*t.PaddingX), "...", false)
	lines = append(lines, PadToWidth(pad+shown+pad, width))
	for range t.PaddingY {
		lines = append(lines, empty)
	}
	return lines
}

// DefaultSpinnerFrames are Pi's loader frames, a braille spinner.
var DefaultSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// DefaultSpinnerInterval is how long each frame shows.
const DefaultSpinnerInterval = 80 * time.Millisecond

// Loader is a message with a spinner in front of it, on its own line after a
// blank one — Pi's Loader. It animates by itself once started, asking for a
// frame each time the spinner moves.
type Loader struct {
	text     *Text
	ui       RenderRequester
	spinner  Style
	messageC Style

	mu       sync.Mutex
	message  string
	frames   []string
	interval time.Duration
	frame    int
	stop     chan struct{}
}

// NewLoader is a loader showing message, already started. Empty frames hide
// the spinner; a single frame shows it without animating.
func NewLoader(ui RenderRequester, spinner, message Style, text string, frames []string, interval time.Duration) *Loader {
	if frames == nil {
		frames = DefaultSpinnerFrames
	}
	if interval <= 0 {
		interval = DefaultSpinnerInterval
	}
	l := &Loader{text: NewText("", 1, 0, nil), ui: ui, spinner: spinner, messageC: message,
		message: text, frames: append([]string(nil), frames...), interval: interval}
	l.Start()
	return l
}

func (l *Loader) Invalidate() { l.text.Invalidate() }

func (l *Loader) Render(width int) []string {
	return append([]string{""}, l.text.Render(width)...)
}

// Start shows the current frame and animates, restarting an animation
// already running.
func (l *Loader) Start() {
	l.Stop()
	l.update()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.frames) <= 1 {
		return
	}
	stop := make(chan struct{})
	l.stop = stop
	interval := l.interval
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				l.mu.Lock()
				l.frame = (l.frame + 1) % len(l.frames)
				l.mu.Unlock()
				l.update()
			}
		}
	}()
}

// Stop freezes the spinner on its current frame.
func (l *Loader) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stop != nil {
		close(l.stop)
		l.stop = nil
	}
}

func (l *Loader) SetMessage(message string) {
	l.mu.Lock()
	l.message = message
	l.mu.Unlock()
	l.update()
}

func (l *Loader) update() {
	l.mu.Lock()
	frame := ""
	if len(l.frames) > 0 {
		frame = l.frames[l.frame]
	}
	message := l.message
	l.mu.Unlock()
	indicator := ""
	if frame != "" {
		indicator = l.spinner.apply(frame) + " "
	}
	l.text.SetText(indicator + l.messageC.apply(message))
	if l.ui != nil {
		l.ui.RequestRender()
	}
}
