package tui

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The scenarios are Pi's own (packages/tui/test/tui-render.test.ts and
// tui-shrink.test.ts at the pin), driven through the renderer and checked on
// the screen the test terminal ends up with.

type harness struct {
	t      *testing.T
	vt     *vt
	screen *mainScreen
}

func newHarness(t *testing.T, width, height int) *harness {
	v := newVT(t, width, height)
	return &harness{t: t, vt: v, screen: &mainScreen{out: v}}
}

func (h *harness) draw(lines ...string) {
	h.t.Helper()
	if err := h.screen.frame(lines, h.vt.width, h.vt.height); err != nil {
		h.t.Fatalf("frame: %v", err)
	}
}

func numbered(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s %d", prefix, i)
	}
	return out
}

func (h *harness) wantScreen(want ...string) {
	h.t.Helper()
	got := h.vt.screen()
	for len(want) < len(got) {
		want = append(want, "")
	}
	if !reflect.DeepEqual(got, want) {
		h.t.Fatalf("screen\n got %q\nwant %q", got, want)
	}
}

func TestOnlyTheChangedLinesAreRewritten(t *testing.T) {
	h := newHarness(t, 40, 10)
	h.draw("Header", "Working...", "Footer")
	for _, frame := range []string{"|", "/", "-", "\\"} {
		before := h.screen.fullRedraws
		h.draw("Header", "Working "+frame, "Footer")
		h.wantScreen("Header", "Working "+frame, "Footer")
		if h.screen.fullRedraws != before {
			t.Fatal("a spinner frame redrew the whole screen")
		}
	}

	h = newHarness(t, 40, 10)
	h.draw(numbered("Line", 4)...)
	h.draw("CHANGED", "Line 1", "Line 2", "Line 3")
	h.wantScreen("CHANGED", "Line 1", "Line 2", "Line 3")
	h.draw("CHANGED", "Line 1", "Line 2", "LAST")
	h.wantScreen("CHANGED", "Line 1", "Line 2", "LAST")

	h = newHarness(t, 40, 10)
	h.draw(numbered("Line", 5)...)
	h.draw("Line 0", "CHANGED 1", "Line 2", "CHANGED 3", "Line 4")
	h.wantScreen("Line 0", "CHANGED 1", "Line 2", "CHANGED 3", "Line 4")
}

func TestTheCursorIsTrackedThroughAShrink(t *testing.T) {
	h := newHarness(t, 40, 10)
	h.draw(numbered("Line", 5)...)
	h.draw(numbered("Line", 3)...)
	h.draw("Line 0", "CHANGED", "Line 2")
	h.wantScreen("Line 0", "CHANGED", "Line 2")
}

func TestContentCanEmptyAndComeBack(t *testing.T) {
	h := newHarness(t, 40, 10)
	h.draw(numbered("Line", 3)...)
	h.draw()
	h.wantScreen()
	h.draw("New Line 0", "New Line 1")
	h.wantScreen("New Line 0", "New Line 1")
}

func TestAWidthChangeRedrawsEverythingAndClearsScrollback(t *testing.T) {
	h := newHarness(t, 40, 3)
	h.draw(numbered("Line", 6)...)
	before := h.screen.fullRedraws
	h.vt.resize(60, 3)
	h.draw(numbered("Line", 6)...)
	if h.screen.fullRedraws == before {
		t.Fatal("a width change did not redraw")
	}
	// Scrollback was wrapped for the old width, so it goes too; what remains
	// is one copy of the content, not two.
	if got := h.vt.transcript(); !reflect.DeepEqual(got, numbered("Line", 6)) {
		t.Fatalf("transcript after a width change %q", got)
	}
}

func TestAHeightChangeRedrawsExceptWhereTheKeyboardCausesIt(t *testing.T) {
	h := newHarness(t, 40, 10)
	h.draw(numbered("Line", 3)...)
	before := h.screen.fullRedraws
	h.vt.resize(40, 15)
	h.draw(numbered("Line", 3)...)
	if h.screen.fullRedraws == before {
		t.Fatal("a height change did not redraw")
	}
	h.wantScreen(numbered("Line", 3)...)

	var writes strings.Builder
	h = newHarness(t, 40, 10)
	h.screen.keepOnHeightChange = true
	h.draw(numbered("Line", 20)...)
	h.screen.out = writerFunc(func(p []byte) { writes.Write(p); h.vt.Write(p) })
	before = h.screen.fullRedraws
	for _, height := range []int{15, 8, 14, 11} {
		h.vt.resize(40, height)
		h.draw(numbered("Line", 20)...)
	}
	if h.screen.fullRedraws != before || strings.Contains(writes.String(), "\x1b[2J") || strings.Contains(writes.String(), "\x1b[3J") {
		t.Fatal("under Termux a height change redrew or cleared the screen")
	}
	if !strings.Contains(strings.Join(h.vt.screen(), "\n"), "Line 19") {
		t.Fatalf("the latest content is gone: %q", h.vt.screen())
	}
}

type writerFunc func([]byte)

func (f writerFunc) Write(p []byte) (int, error) { f(p); return len(p), nil }

func TestAShrinkClearsTheRowsLeftBehind(t *testing.T) {
	for _, clearOnShrink := range []bool{false, true} {
		h := newHarness(t, 40, 10)
		h.screen.clearOnShrink = clearOnShrink
		h.draw(numbered("Line", 6)...)
		before := h.screen.fullRedraws
		h.draw("Line 0", "Line 1")
		h.wantScreen("Line 0", "Line 1")
		if clearOnShrink && h.screen.fullRedraws == before {
			t.Error("with clearOnShrink on, a shrink did not redraw")
		}

		h.draw("Only line")
		h.wantScreen("Only line")
		h.draw()
		h.wantScreen()
	}
}

func TestDeletedLinesThatMoveTheViewportUpRedraw(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.draw(numbered("Line", 12)...)
	before := h.screen.fullRedraws
	h.draw(numbered("Line", 7)...)
	if h.screen.fullRedraws == before {
		t.Fatal("a shrink that moved the viewport up did not redraw")
	}
	h.wantScreen("Line 2", "Line 3", "Line 4", "Line 5", "Line 6")
}

func TestAnAppendAfterAShrinkStaysDifferential(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.draw(numbered("Line", 8)...)
	before := h.screen.fullRedraws
	h.draw("Line 0", "Line 1")
	if h.screen.fullRedraws == before {
		t.Fatal("the shrink did not reset the viewport")
	}
	after := h.screen.fullRedraws
	h.draw("Line 0", "Line 1", "Line 2")
	if h.screen.fullRedraws != after {
		t.Fatal("an append after the shrink redrew everything")
	}
	h.wantScreen("Line 0", "Line 1", "Line 2", "", "")
}

func TestStaleRowsFromATransientComponentAreCleared(t *testing.T) {
	h := newHarness(t, 40, 10)
	editor := []string{"Editor 0", "Editor 1", "Editor 2"}
	h.draw(append(numbered("Chat", 15), editor...)...)
	h.draw(append(numbered("Chat", 15), numbered("Selector", 8)...)...)
	h.draw(append(numbered("Chat", 15), editor...)...)
	before := h.screen.fullRedraws
	h.draw(append(numbered("Chat", 12), editor...)...)
	if h.screen.fullRedraws == before {
		t.Fatal("switching to a shorter branch did not redraw")
	}
	h.wantScreen("Chat 5", "Chat 6", "Chat 7", "Chat 8", "Chat 9", "Chat 10", "Chat 11",
		"Editor 0", "Editor 1", "Editor 2")
}

func TestContentLongerThanTheScreenScrollsIntoScrollback(t *testing.T) {
	h := newHarness(t, 20, 3)
	h.draw(numbered("Line", 2)...)
	for n := 3; n <= 7; n++ {
		h.draw(numbered("Line", n)...)
	}
	if got := h.vt.transcript(); !reflect.DeepEqual(got, numbered("Line", 7)) {
		t.Fatalf("transcript %q", got)
	}
	h.draw(append(numbered("Line", 6), "LAST")...)
	h.wantScreen("Line 4", "Line 5", "LAST")
}

func TestEveryLineEndsInAReset(t *testing.T) {
	var out strings.Builder
	s := &mainScreen{out: &out}
	if err := s.frame([]string{"\x1b[3mItalic", "Plain"}, 20, 6); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\x1b[3mItalic"+lineReset+"\r\nPlain"+lineReset) {
		t.Fatalf("a line's style can run into the next: %q", out.String())
	}
}

func TestTheHardwareCursorGoesToTheMarker(t *testing.T) {
	h := newHarness(t, 20, 5)
	h.draw("first", "ab"+CursorMarker+"cd", "third")
	if h.vt.row != 1 || h.vt.col != 2 {
		t.Fatalf("cursor at %d,%d, want 1,2", h.vt.row, h.vt.col)
	}
	if h.vt.cursorShown {
		t.Fatal("the cursor is shown without PI_HARDWARE_CURSOR")
	}
	h.wantScreen("first", "abcd", "third")

	h.screen.showCursor = true
	h.draw("first", "abc"+CursorMarker+"d", "third")
	if h.vt.row != 1 || h.vt.col != 3 || !h.vt.cursorShown {
		t.Fatalf("cursor at %d,%d shown %v", h.vt.row, h.vt.col, h.vt.cursorShown)
	}
	h.draw("first", "abcd", "third")
	if h.vt.cursorShown {
		t.Fatal("with no marker the cursor stays shown")
	}
}

func TestALineWiderThanTheTerminalIsRefused(t *testing.T) {
	h := newHarness(t, 10, 5)
	err := h.screen.frame([]string{"fits", "this line is far too wide"}, 10, 5)
	var tooWide *errLineTooWide
	if !errors.As(err, &tooWide) || tooWide.index != 1 {
		t.Fatalf("an overflowing line was drawn: %v", err)
	}
}

// TestLeavingPutsTheShellBelowTheInterface, one blank row below it as Pi
// leaves it: a space, a move to the row after the content, then a newline.
func TestLeavingPutsTheShellBelowTheInterface(t *testing.T) {
	h := newHarness(t, 20, 6)
	h.draw("one", "two"+CursorMarker, "three")
	h.screen.leave()
	if h.vt.row != 4 || h.vt.col != 0 {
		t.Fatalf("after leaving the cursor is at %d,%d, want 4,0", h.vt.row, h.vt.col)
	}
	h.wantScreen("one", "two", "three")
}
