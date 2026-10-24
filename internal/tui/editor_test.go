package tui

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// These follow Pi's editor tests (packages/tui/test/editor.test.ts at the
// pin), keystrokes given as the bytes a terminal sends. Columns are byte
// offsets here where Pi's are UTF-16 indices; the two agree on ASCII.

type rowsHost struct{ rows int }

func (rowsHost) RequestRender() {}
func (h rowsHost) Rows() int    { return h.rows }

func newTestEditor() *Editor {
	return NewEditor(rowsHost{24}, EditorTheme{}, EditorOptions{})
}

// in feeds raw terminal input, as Pi's tests call handleInput.
func in(e *Editor, raws ...string) {
	for _, raw := range raws {
		keys, rest := DecodeKeys([]byte(raw), false)
		if string(rest) == "\x1b" {
			keys = append(keys, Key{Name: "escape"})
		}
		for _, k := range keys {
			e.HandleKey(k)
		}
	}
}

func typeInto(e *Editor, text string) {
	for _, r := range text {
		in(e, string(r))
	}
}

func times(e *Editor, raw string, n int) {
	for i := 0; i < n; i++ {
		in(e, raw)
	}
}

const (
	up, down, left, right = "\x1b[A", "\x1b[B", "\x1b[D", "\x1b[C"
	ctrlA, ctrlE          = "\x01", "\x05"
	ctrlW, ctrlU, ctrlK   = "\x17", "\x15", "\x0b"
	ctrlY, altY, altD     = "\x19", "\x1by", "\x1bd"
	ctrlLeft, ctrlRight   = "\x1b[1;5D", "\x1b[1;5C"
	undoKey               = "\x1b[45;5u"
	backspace, deleteKey  = "\x7f", "\x1b[3~"
	enter                 = "\r"
	jumpFwd, jumpBack     = "\x1d", "\x1b\x1d"
)

func paste(text string) string { return "\x1b[200~" + text + "\x1b[201~" }

func wantText(t *testing.T, e *Editor, want string) {
	t.Helper()
	if got := e.Text(); got != want {
		t.Fatalf("text %q, want %q", got, want)
	}
}

func wantCursor(t *testing.T, e *Editor, line, col int) {
	t.Helper()
	if l, c := e.Cursor(); l != line || c != col {
		t.Fatalf("cursor (%d,%d), want (%d,%d)", l, c, line, col)
	}
}

// --- history ---

func TestEditorHistory(t *testing.T) {
	t.Run("up does nothing with no history", func(t *testing.T) {
		e := newTestEditor()
		in(e, up)
		wantText(t, e, "")
	})
	t.Run("up shows the newest entry and cycles to the oldest", func(t *testing.T) {
		e := newTestEditor()
		e.AddToHistory("first")
		e.AddToHistory("second")
		e.AddToHistory("third")
		for _, want := range []string{"third", "second", "first", "first"} {
			in(e, up)
			wantText(t, e, want)
		}
	})
	t.Run("a draft is left at its start before history is entered", func(t *testing.T) {
		e := newTestEditor()
		e.AddToHistory("prompt")
		e.SetText("draft")
		in(e, left, left, up)
		wantText(t, e, "draft")
		wantCursor(t, e, 0, 0)
		in(e, up)
		wantText(t, e, "prompt")
		in(e, down)
		wantText(t, e, "draft")
		wantCursor(t, e, 0, 0)
	})
	t.Run("down comes forward to the draft", func(t *testing.T) {
		e := newTestEditor()
		for _, h := range []string{"first", "second", "third"} {
			e.AddToHistory(h)
		}
		e.SetText("draft")
		times(e, up, 4)
		for _, want := range []string{"second", "third", "draft"} {
			in(e, down)
			wantText(t, e, want)
		}
	})
	t.Run("typing leaves history", func(t *testing.T) {
		e := newTestEditor()
		e.AddToHistory("old prompt")
		in(e, up, "x")
		wantText(t, e, "xold prompt")
	})
	t.Run("setText leaves history", func(t *testing.T) {
		e := newTestEditor()
		e.AddToHistory("first")
		e.AddToHistory("second")
		in(e, up)
		e.SetText("")
		in(e, up)
		wantText(t, e, "second")
	})
	t.Run("blank entries and repeats are not added", func(t *testing.T) {
		e := newTestEditor()
		e.AddToHistory("")
		e.AddToHistory("   ")
		e.AddToHistory("same")
		e.AddToHistory("same")
		in(e, up, up)
		wantText(t, e, "same")
	})
	t.Run("non-consecutive repeats are kept", func(t *testing.T) {
		e := newTestEditor()
		e.AddToHistory("first")
		e.AddToHistory("second")
		e.AddToHistory("first")
		for _, want := range []string{"first", "second", "first"} {
			in(e, up)
			wantText(t, e, want)
		}
	})
	t.Run("up moves the cursor when there is a line above", func(t *testing.T) {
		e := newTestEditor()
		e.AddToHistory("history item")
		e.SetText("line1\nline2")
		in(e, up, "X")
		wantText(t, e, "line1X\nline2")
	})
	t.Run("history keeps a hundred entries", func(t *testing.T) {
		e := newTestEditor()
		for i := 0; i < 105; i++ {
			e.AddToHistory("prompt " + strconv.Itoa(i))
		}
		times(e, up, 100)
		wantText(t, e, "prompt 5")
		in(e, up)
		wantText(t, e, "prompt 5")
	})
	t.Run("going back puts the cursor at the start, coming forward at the end", func(t *testing.T) {
		e := newTestEditor()
		e.AddToHistory("older entry")
		e.AddToHistory("line1\nline2\nline3")
		e.AddToHistory("newer entry")
		in(e, up, up)
		wantCursor(t, e, 0, 0)
		in(e, up)
		wantText(t, e, "older entry")
		in(e, down)
		wantText(t, e, "line1\nline2\nline3")
		wantCursor(t, e, 2, 5)
		in(e, down)
		wantText(t, e, "newer entry")
	})
	t.Run("the cursor moves within a multi-line entry", func(t *testing.T) {
		e := newTestEditor()
		e.AddToHistory("line1\nline2\nline3")
		in(e, up)
		wantCursor(t, e, 0, 0)
		in(e, down)
		wantText(t, e, "line1\nline2\nline3")
		wantCursor(t, e, 1, 0)
		in(e, up)
		wantCursor(t, e, 0, 0)
	})
}

func TestEditorStateAccessors(t *testing.T) {
	e := newTestEditor()
	wantCursor(t, e, 0, 0)
	in(e, "a", "b", "c")
	wantCursor(t, e, 0, 3)
	in(e, left)
	wantCursor(t, e, 0, 2)

	e.SetText("a\nb")
	lines := e.Lines()
	lines[0] = "mutated"
	if got := e.Lines(); got[0] != "a" || got[1] != "b" {
		t.Fatalf("lines %q", got)
	}
}

func TestEditorBackslashEnter(t *testing.T) {
	e := newTestEditor()
	in(e, "\\")
	wantText(t, e, "\\")
	in(e, enter)
	wantText(t, e, "\n")

	e = newTestEditor()
	submitted := false
	e.OnSubmit = func(string) { submitted = true }
	in(e, "\\", "x", enter)
	if !submitted {
		t.Fatal("a backslash not before the cursor stopped the submit")
	}

	e = newTestEditor()
	in(e, "\\", "\\", "\\", enter)
	wantText(t, e, "\\\\\n")
}

func TestEditorKittyText(t *testing.T) {
	e := newTestEditor()
	in(e, "\x1b[99;9u")
	wantText(t, e, "")
	in(e, "\x1b[69;2u", "\x1b[27;2;69~")
	wantText(t, e, "EE")
}

// --- Unicode ---

func TestEditorUnicode(t *testing.T) {
	e := newTestEditor()
	typeInto(e, "Hello äöü 😀")
	wantText(t, e, "Hello äöü 😀")

	e = newTestEditor()
	typeInto(e, "äöü")
	in(e, backspace)
	wantText(t, e, "äö")

	e = newTestEditor()
	typeInto(e, "😀👍")
	in(e, backspace)
	wantText(t, e, "😀")

	e = newTestEditor()
	typeInto(e, "äöü")
	in(e, left, left, "x")
	wantText(t, e, "äxöü")

	e = newTestEditor()
	typeInto(e, "😀👍🎉")
	in(e, left, left, "x")
	wantText(t, e, "😀x👍🎉")

	e = newTestEditor()
	typeInto(e, "äöü\nÄÖÜ")
	wantText(t, e, "äöü\nÄÖÜ")

	e = newTestEditor()
	in(e, "a", "b", ctrlA, "x")
	wantText(t, e, "xab")
}

func TestEditorDeleteWord(t *testing.T) {
	cases := []struct{ text, want string }{
		{"foo bar baz", "foo bar "},
		{"foo bar   ", "foo "},
		{"foo bar...", "foo bar"},
		{"foo.bar", "foo."},
		{"foo:bar", "foo:"},
		{"line one\nline two", "line one\nline "},
		{"line one\n", "line one"},
		{"foo 😀😀 bar", "foo 😀😀 "},
	}
	for _, c := range cases {
		e := newTestEditor()
		e.SetText(c.text)
		in(e, ctrlW)
		wantText(t, e, c.want)
	}
	e := newTestEditor()
	e.SetText("foo 😀😀 ")
	in(e, ctrlW)
	wantText(t, e, "foo ")
	e.SetText("foo bar")
	in(e, "\x1b\x7f")
	wantText(t, e, "foo ")
}

func TestEditorWordMotion(t *testing.T) {
	e := newTestEditor()
	e.SetText("foo bar... baz")
	for _, step := range []struct {
		key string
		col int
	}{{ctrlLeft, 11}, {ctrlLeft, 7}, {ctrlLeft, 4}, {ctrlRight, 7}, {ctrlRight, 10}, {ctrlRight, 14}} {
		in(e, step.key)
		wantCursor(t, e, 0, step.col)
	}
	e.SetText("   foo bar")
	in(e, ctrlA, ctrlRight)
	wantCursor(t, e, 0, 6)

	e.SetText("foo.bar baz")
	for _, col := range []int{8, 4, 3} {
		in(e, ctrlLeft)
		wantCursor(t, e, 0, col)
	}
	in(e, ctrlA)
	for _, col := range []int{3, 4, 7} {
		in(e, ctrlRight)
		wantCursor(t, e, 0, col)
	}
}

// TestEditorCJKWordMotion: a run of ideographs is one word, and fullwidth
// punctuation stops the cursor, as Pi's dictionary segmentation has it for
// these texts. Columns are bytes: each character here is three.
func TestEditorCJKWordMotion(t *testing.T) {
	e := newTestEditor()
	e.SetText("你好，世界")
	for _, step := range []struct {
		key string
		col int
	}{{ctrlLeft, 9}, {ctrlLeft, 6}, {ctrlLeft, 0}, {ctrlRight, 6}, {ctrlRight, 9}, {ctrlRight, 15}} {
		in(e, step.key)
		wantCursor(t, e, 0, step.col)
	}

	e.SetText("hello你好，world世界")
	for _, col := range []int{19, 14, 11, 5, 0} {
		in(e, ctrlLeft)
		wantCursor(t, e, 0, col)
	}
	for _, col := range []int{5, 11, 14, 19, 25} {
		in(e, ctrlRight)
		wantCursor(t, e, 0, col)
	}
}

// --- rendering ---

func TestEditorScrollIndicatorsFitAndKeepTheirColor(t *testing.T) {
	const width = 10
	color := func(s string) string { return "\x1b[35m" + s + "\x1b[39m" }
	e := NewEditor(rowsHost{24}, EditorTheme{BorderColor: color}, EditorOptions{})
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "line "+strconv.Itoa(i))
	}
	e.SetText(strings.Join(lines, "\n"))
	e.Render(width)
	times(e, up, 10)
	out := e.Render(width)
	top, bottom := out[0], out[len(out)-1]
	if !strings.HasPrefix(StripSequences(top), "─── ↑") || !strings.HasPrefix(StripSequences(bottom), "─── ↓") {
		t.Fatalf("borders %q / %q", StripSequences(top), StripSequences(bottom))
	}
	if top != color(StripSequences(top)) || bottom != color(StripSequences(bottom)) {
		t.Fatalf("a border lost its color: %q", top)
	}
	for _, l := range out {
		if VisibleWidth(l) != width {
			t.Fatalf("line %q is %d wide", l, VisibleWidth(l))
		}
	}
}

func contentLines(out []string) []string { return out[1 : len(out)-1] }

func TestEditorWrapsWideText(t *testing.T) {
	for _, c := range []struct {
		text  string
		width int
	}{{"Hello ✅ World", 20}, {"✅✅✅✅✅✅", 10}, {"ำabc", 8}, {"ຳabc", 8}, {"A✅B", 20}} {
		e := newTestEditor()
		e.SetText(c.text)
		for _, l := range e.Render(c.width) {
			if VisibleWidth(l) != c.width {
				t.Fatalf("%q at %d: line %q is %d wide", c.text, c.width, l, VisibleWidth(l))
			}
		}
	}

	e := newTestEditor()
	e.SetText("日本語テスト")
	got := contentLines(e.Render(11))
	if len(got) != 2 || strings.TrimSpace(StripSequences(got[0])) != "日本語テス" || strings.TrimSpace(StripSequences(got[1])) != "ト" {
		t.Fatalf("CJK wrapped as %q", got)
	}

	e = newTestEditor()
	e.SetText("Test ✅ OK 日本")
	if got := contentLines(e.Render(16)); len(got) != 1 || VisibleWidth(got[0]) != 16 {
		t.Fatalf("mixed text %q", got)
	}

	e = newTestEditor()
	e.SetText("0123456789✅")
	for _, l := range e.Render(11) {
		if VisibleWidth(l) > 11 {
			t.Fatalf("line %q overflows", l)
		}
	}
}

func TestEditorCursorStaysOnTheLineUntilItWraps(t *testing.T) {
	const width = 10
	for _, paddingX := range []int{0, 1} {
		e := NewEditor(rowsHost{24}, EditorTheme{}, EditorOptions{PaddingX: paddingX})
		typeInto(e, "aaaaaaaaa")
		got := contentLines(e.Render(width + paddingX))
		if len(got) != 1 || !strings.HasSuffix(strings.TrimRight(got[0], " "), "\x1b[7m \x1b[0m") {
			t.Fatalf("padding %d: before the wrap %q", paddingX, got)
		}
		in(e, "a")
		if got := contentLines(e.Render(width + paddingX)); len(got) != 2 {
			t.Fatalf("padding %d: after the wrap %q", paddingX, got)
		}
	}
}

func TestEditorRenderShapes(t *testing.T) {
	e := newTestEditor()
	if got := e.Render(40); len(got) != 3 {
		t.Fatalf("empty editor %q", got)
	}
	e.SetText("1234567890")
	if got := e.Render(11); len(got) != 3 || !strings.Contains(StripSequences(got[1]), "1234567890") {
		t.Fatalf("exact fit %q", got)
	}
	e.SetText("Word1   Word2    Word3")
	if got := StripSequences(e.Render(50)[1]); !strings.Contains(got, "Word1   Word2") {
		t.Fatalf("spaces collapsed: %q", got)
	}
	e.SetText("Word1 Word2 Word3 Word4 Word5 Word6")
	for _, l := range contentLines(e.Render(20)) {
		if s := StripSequences(l); strings.TrimSpace(s) != "" && s[0] == ' ' {
			t.Fatalf("a wrapped line starts with a space: %q", s)
		}
	}
	e.SetText("Check https://example.com/very/long/path/that/exceeds/width here")
	for _, l := range e.Render(30) {
		if VisibleWidth(l) != 30 {
			t.Fatalf("line %q is %d wide", l, VisibleWidth(l))
		}
	}
}

func TestEditorFocusedPlacesTheHardwareCursor(t *testing.T) {
	e := newTestEditor()
	e.SetText("ab")
	if strings.Contains(strings.Join(e.Render(20), ""), CursorMarker) {
		t.Fatal("an unfocused editor placed the cursor")
	}
	e.SetFocused(true)
	if !strings.Contains(e.Render(20)[1], CursorMarker+"\x1b[7m \x1b[0m") {
		t.Fatalf("the marker is not before the cursor: %q", e.Render(20)[1])
	}
	in(e, left)
	if !strings.Contains(e.Render(20)[1], CursorMarker+"\x1b[7mb\x1b[0m") {
		t.Fatalf("the cursor does not show the character under it: %q", e.Render(20)[1])
	}
}

func TestEditorScrollsToAThirdOfTheTerminal(t *testing.T) {
	e := NewEditor(rowsHost{40}, EditorTheme{}, EditorOptions{})
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, "l"+strconv.Itoa(i))
	}
	e.SetText(strings.Join(lines, "\n"))
	out := e.Render(20)
	if got := len(contentLines(out)); got != 12 {
		t.Fatalf("%d lines shown on a 40-row terminal, want 12", got)
	}
	if got := StripSequences(out[0]); !strings.HasPrefix(got, "─── ↑ 18 more ") {
		t.Fatalf("top border %q", got)
	}
}

// --- word wrapping ---

func chunkTexts(chunks []textChunk) []string {
	var out []string
	for _, c := range chunks {
		out = append(out, c.text)
	}
	return out
}

func TestWordWrapLine(t *testing.T) {
	cases := []struct {
		line  string
		width int
		want  []string
	}{
		{"hello world test", 11, []string{"hello ", "world test"}},
		{"hello world test", 12, []string{"hello world ", "test"}},
		{"aaaaaaaaaaaa aaaa", 12, []string{"aaaaaaaaaaaa", " aaaa"}},
		{"      aaaaaaaaaaaa", 12, []string{"      ", "aaaaaaaaaaaa"}},
		{"Lorem ipsum dolor sit amet,    consectetur", 30, []string{"Lorem ipsum dolor sit ", "amet,    consectetur"}},
		{"Lorem ipsum dolor sit amet,              consectetur", 30, []string{"Lorem ipsum dolor sit ", "amet,              consectetur"}},
		{"Lorem ipsum dolor sit amet,               consectetur", 30, []string{"Lorem ipsum dolor sit ", "amet,               ", "consectetur"}},
		{"Lorem ipsum dolor sit amet,                         consectetur", 30, []string{"Lorem ipsum dolor sit ", "amet,                         ", "consectetur"}},
		{"Lorem ipsum dolor sit amet,                          consectetur", 30, []string{"Lorem ipsum dolor sit ", "amet,                         ", " consectetur"}},
		{"Lorem ipsum dolor sit amet,                                     consectetur", 30, []string{"Lorem ipsum dolor sit ", "amet,                         ", "            consectetur"}},
	}
	for _, c := range cases {
		if got := chunkTexts(wordWrapLine(c.line, c.width, nil)); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("wordWrapLine(%q, %d) = %q, want %q", c.line, c.width, got, c.want)
		}
	}
}

func checkChunks(t *testing.T, line string, width int, chunks []textChunk) {
	t.Helper()
	var rebuilt strings.Builder
	for _, c := range chunks {
		if VisibleWidth(c.text) > width {
			t.Fatalf("chunk %q is %d wide, over %d", c.text, VisibleWidth(c.text), width)
		}
		rebuilt.WriteString(line[c.start:c.end])
	}
	if rebuilt.String() != line {
		t.Fatalf("chunks lost text: %q", rebuilt.String())
	}
}

func TestWordWrapLineForceBreaksAWideCharacter(t *testing.T) {
	line := " " + strings.Repeat("a", 186) + "你"
	checkChunks(t, line, 187, wordWrapLine(line, 187, nil))
}

func TestWordWrapLineSplitsOversizedMarkers(t *testing.T) {
	m1, m2 := "[paste #1 +20 lines]", "[paste #2 +30 lines]"
	segs := func(parts ...string) (string, []segment) {
		var line string
		var out []segment
		for _, p := range parts {
			out = append(out, segment{text: p, index: len(line)})
			line += p
		}
		return line, out
	}
	for _, parts := range [][]string{{"A", m1, "B"}, {m1, "B"}, {"A", m1}, {m1, m2}} {
		line, s := segs(parts...)
		chunks := wordWrapLine(line, 10, s)
		checkChunks(t, line, 10, chunks)
		if parts[0] == "A" && len(parts) == 2 && chunks[0].text != "A" {
			t.Fatalf("first chunk %q", chunks[0].text)
		}
		if parts[len(parts)-1] == "B" && !strings.Contains(chunks[len(chunks)-1].text, "B") {
			t.Fatalf("last chunk %q", chunks[len(chunks)-1].text)
		}
	}
	parts := []string{m1}
	for _, r := range " hello world" {
		parts = append(parts, string(r))
	}
	line, s := segs(parts...)
	chunks := wordWrapLine(line, 10, s)
	checkChunks(t, line, 10, chunks)
	if last := chunks[len(chunks)-1].text; last != "world" {
		t.Fatalf("wrapping did not resume after the marker: %q", last)
	}
}

// --- kill ring ---

func TestEditorKillRing(t *testing.T) {
	t.Run("ctrl+w kills and ctrl+y yanks", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("foo bar baz")
		in(e, ctrlW)
		wantText(t, e, "foo bar ")
		in(e, ctrlA, ctrlY)
		wantText(t, e, "bazfoo bar ")
	})
	t.Run("ctrl+u and ctrl+k kill", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("hello world")
		in(e, ctrlA)
		times(e, right, 6)
		in(e, ctrlU)
		wantText(t, e, "world")
		in(e, ctrlY)
		wantText(t, e, "hello world")

		e.SetText("hello world")
		in(e, ctrlA, ctrlK)
		wantText(t, e, "")
		in(e, ctrlY)
		wantText(t, e, "hello world")
	})
	t.Run("ctrl+y with nothing killed", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("test")
		in(e, ctrlY)
		wantText(t, e, "test")
	})
	t.Run("alt+y cycles after a yank", func(t *testing.T) {
		e := newTestEditor()
		for _, s := range []string{"first", "second", "third"} {
			e.SetText(s)
			in(e, ctrlW)
		}
		wantText(t, e, "")
		in(e, ctrlY)
		wantText(t, e, "third")
		for _, want := range []string{"second", "first", "third"} {
			in(e, altY)
			wantText(t, e, want)
		}
	})
	t.Run("alt+y needs a yank before it and more than one entry", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("test")
		in(e, ctrlW)
		e.SetText("other")
		in(e, "x", altY)
		wantText(t, e, "otherx")

		e = newTestEditor()
		e.SetText("only")
		in(e, ctrlW, ctrlY, altY)
		wantText(t, e, "only")
	})
	t.Run("consecutive kills accumulate", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("one two three")
		in(e, ctrlW, ctrlW, ctrlW)
		wantText(t, e, "")
		in(e, ctrlY)
		wantText(t, e, "one two three")

		e = newTestEditor()
		e.SetText("line1\nline2\nline3")
		for _, want := range []string{"line1\nline2\n", "line1\nline2", "line1\n", "line1", ""} {
			in(e, ctrlU)
			wantText(t, e, want)
		}
		in(e, ctrlY)
		wantText(t, e, "line1\nline2\nline3")

		e = newTestEditor()
		e.SetText("1\n2\n3")
		times(e, ctrlW, 5)
		in(e, ctrlY)
		wantText(t, e, "1\n2\n3")
	})
	t.Run("backward kills prepend, forward kills append", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("prefix|suffix")
		in(e, ctrlA)
		times(e, right, 6)
		in(e, ctrlK, ctrlK)
		wantText(t, e, "prefix")
		in(e, ctrlY)
		wantText(t, e, "prefix|suffix")
	})
	t.Run("other actions break accumulation and the yank chain", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("foo bar baz")
		in(e, ctrlW, "x", ctrlW, ctrlY)
		wantText(t, e, "foo bar x")
		in(e, altY)
		wantText(t, e, "foo bar baz")

		e = newTestEditor()
		e.SetText("first")
		in(e, ctrlW)
		e.SetText("second")
		in(e, ctrlW)
		e.SetText("")
		in(e, ctrlY, "x", altY)
		wantText(t, e, "secondx")
	})
	t.Run("rotation persists", func(t *testing.T) {
		e := newTestEditor()
		for _, s := range []string{"first", "second", "third"} {
			e.SetText(s)
			in(e, ctrlW)
		}
		e.SetText("")
		in(e, ctrlY, altY)
		wantText(t, e, "second")
		in(e, "x")
		e.SetText("")
		in(e, ctrlY)
		wantText(t, e, "second")
	})
	t.Run("ctrl+k at a line's end kills the newline", func(t *testing.T) {
		e := newTestEditor()
		in(e, "a", "b", "\n", "c", "d", up, ctrlE, ctrlK)
		wantText(t, e, "abcd")
		in(e, ctrlK)
		wantText(t, e, "ab")
		in(e, ctrlY)
		wantText(t, e, "ab\ncd")
	})
	t.Run("yank and yank-pop mid-text, single and multi-line", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("FIRST")
		in(e, ctrlW)
		e.SetText("SECOND")
		in(e, ctrlW)
		e.SetText("hello world")
		in(e, ctrlA)
		times(e, right, 6)
		in(e, ctrlY)
		wantText(t, e, "hello SECONDworld")
		in(e, altY)
		wantText(t, e, "hello FIRSTworld")

		e = newTestEditor()
		e.SetText("SINGLE")
		in(e, ctrlW)
		e.SetText("A\nB")
		in(e, ctrlU, ctrlU, ctrlU)
		e.SetText("hello world")
		in(e, ctrlA)
		times(e, right, 6)
		in(e, ctrlY)
		wantText(t, e, "hello A\nBworld")
		in(e, altY)
		wantText(t, e, "hello SINGLEworld")
	})
	t.Run("alt+d kills forward", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("hello world test")
		in(e, ctrlA, altD)
		wantText(t, e, " world test")
		in(e, altD)
		wantText(t, e, " test")
		in(e, ctrlY)
		wantText(t, e, "hello world test")

		e.SetText("line1\nline2")
		in(e, up, ctrlE, altD)
		wantText(t, e, "line1line2")
		in(e, ctrlY)
		wantText(t, e, "line1\nline2")
	})
}

// --- undo ---

func TestEditorUndo(t *testing.T) {
	t.Run("nothing to undo", func(t *testing.T) {
		e := newTestEditor()
		in(e, undoKey)
		wantText(t, e, "")
	})
	t.Run("a word is one step and each space starts one", func(t *testing.T) {
		e := newTestEditor()
		typeInto(e, "hello world")
		for _, want := range []string{"hello", ""} {
			in(e, undoKey)
			wantText(t, e, want)
		}
		typeInto(e, "hello  ")
		for _, want := range []string{"hello ", "hello", ""} {
			in(e, undoKey)
			wantText(t, e, want)
		}
		typeInto(e, "hello\nworld")
		for _, want := range []string{"hello\n", "hello", ""} {
			in(e, undoKey)
			wantText(t, e, want)
		}
	})
	t.Run("deletions undo", func(t *testing.T) {
		e := newTestEditor()
		typeInto(e, "hello")
		in(e, backspace)
		wantText(t, e, "hell")
		in(e, undoKey)
		wantText(t, e, "hello")
		in(e, ctrlA, right, deleteKey)
		wantText(t, e, "hllo")
		in(e, undoKey)
		wantText(t, e, "hello")

		e = newTestEditor()
		typeInto(e, "hello world")
		in(e, ctrlW, undoKey)
		wantText(t, e, "hello world")
		in(e, ctrlA)
		times(e, right, 6)
		in(e, ctrlK, undoKey, "|")
		wantText(t, e, "hello |world")

		e = newTestEditor()
		typeInto(e, "hello world")
		in(e, ctrlA)
		times(e, right, 6)
		in(e, ctrlU)
		wantText(t, e, "world")
		in(e, undoKey)
		wantText(t, e, "hello world")
	})
	t.Run("yank undoes", func(t *testing.T) {
		e := newTestEditor()
		typeInto(e, "hello ")
		in(e, ctrlW, ctrlY)
		wantText(t, e, "hello ")
		in(e, undoKey)
		wantText(t, e, "")
	})
	t.Run("a paste is one step", func(t *testing.T) {
		for _, c := range []struct{ pasted, want string }{
			{"beep boop", "hellobeep boop world"},
			{"line1\nline2\nline3", "helloline1\nline2\nline3 world"},
		} {
			e := newTestEditor()
			e.SetText("hello world")
			in(e, ctrlA)
			times(e, right, 5)
			in(e, paste(c.pasted))
			wantText(t, e, c.want)
			in(e, undoKey, "|")
			wantText(t, e, "hello| world")
		}
	})
	t.Run("ctrl+letter codes inside a paste are decoded", func(t *testing.T) {
		e := newTestEditor()
		in(e, paste("line1\x1b[106;5uline2\x1b[106;5uline3"))
		wantText(t, e, "line1\nline2\nline3")
	})
	t.Run("insertTextAtCursor is one step", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("hello world")
		in(e, ctrlA)
		times(e, right, 5)
		e.InsertTextAtCursor("/tmp/image.png")
		wantText(t, e, "hello/tmp/image.png world")
		in(e, undoKey, "|")
		wantText(t, e, "hello| world")

		e.SetText("hello world")
		in(e, ctrlA)
		times(e, right, 5)
		e.InsertTextAtCursor("line1\nline2\nline3")
		wantText(t, e, "helloline1\nline2\nline3 world")
		wantCursor(t, e, 2, 5)
		in(e, undoKey)
		wantText(t, e, "hello world")

		e.SetText("")
		e.InsertTextAtCursor("a\r\nb\r\nc")
		wantText(t, e, "a\nb\nc")
		in(e, undoKey)
		wantText(t, e, "")
		e.InsertTextAtCursor("x\ry\rz")
		wantText(t, e, "x\ny\nz")
	})
	t.Run("setText undoes", func(t *testing.T) {
		e := newTestEditor()
		typeInto(e, "hello world")
		e.SetText("")
		in(e, undoKey)
		wantText(t, e, "hello world")
	})
	t.Run("submit clears undo", func(t *testing.T) {
		e := newTestEditor()
		var submitted string
		e.OnSubmit = func(s string) { submitted = s }
		typeInto(e, "hello")
		in(e, enter)
		if submitted != "hello" {
			t.Fatalf("submitted %q", submitted)
		}
		in(e, undoKey)
		wantText(t, e, "")
	})
	t.Run("undo returns to before history browsing", func(t *testing.T) {
		e := newTestEditor()
		e.AddToHistory("hello")
		typeInto(e, "world")
		in(e, ctrlW, up)
		wantText(t, e, "hello")
		in(e, undoKey)
		wantText(t, e, "")
		in(e, undoKey)
		wantText(t, e, "world")

		e = newTestEditor()
		for _, h := range []string{"first", "second", "third"} {
			e.AddToHistory(h)
		}
		typeInto(e, "current")
		in(e, ctrlW, up, up, up)
		wantText(t, e, "first")
		in(e, undoKey)
		wantText(t, e, "")
		in(e, undoKey)
		wantText(t, e, "current")
	})
	t.Run("moving the cursor starts a new step", func(t *testing.T) {
		e := newTestEditor()
		typeInto(e, "hello world")
		times(e, left, 5)
		typeInto(e, "lol")
		wantText(t, e, "hello lolworld")
		in(e, undoKey, "|")
		wantText(t, e, "hello |world")
	})
	t.Run("a deletion that deletes nothing is no step", func(t *testing.T) {
		e := newTestEditor()
		typeInto(e, "hello")
		in(e, ctrlW, ctrlW, ctrlW, undoKey)
		wantText(t, e, "hello")
	})
}

// --- jump to character ---

func TestEditorJumpToCharacter(t *testing.T) {
	e := newTestEditor()
	e.SetText("hello world")
	in(e, ctrlA, jumpFwd, "o")
	wantCursor(t, e, 0, 4)
	in(e, jumpFwd, "o")
	wantCursor(t, e, 0, 7)

	e.SetText("abc\ndef\nghi")
	in(e, up, up, ctrlA, jumpFwd, "g")
	wantCursor(t, e, 2, 0)

	e.SetText("hello world")
	in(e, jumpBack, "o")
	wantCursor(t, e, 0, 7)

	e.SetText("abc\ndef\nghi")
	in(e, jumpBack, "a")
	wantCursor(t, e, 0, 0)

	e.SetText("hello world")
	in(e, ctrlA, jumpFwd, "z")
	wantCursor(t, e, 0, 0)
	in(e, ctrlE, jumpBack, "z")
	wantCursor(t, e, 0, 11)

	e.SetText("Hello World")
	in(e, ctrlA, jumpFwd, "h")
	wantCursor(t, e, 0, 0)
	in(e, jumpFwd, "W")
	wantCursor(t, e, 0, 6)

	e.SetText("foo(bar) = baz;")
	in(e, ctrlA, jumpFwd, "(")
	wantCursor(t, e, 0, 3)
	in(e, jumpFwd, "=")
	wantCursor(t, e, 0, 9)

	e.SetText("")
	in(e, jumpFwd, "x")
	wantCursor(t, e, 0, 0)
}

func TestEditorJumpCancels(t *testing.T) {
	e := newTestEditor()
	e.SetText("hello world")
	in(e, ctrlA, jumpFwd, jumpFwd, "o")
	wantText(t, e, "ohello world")

	e.SetText("hello world")
	in(e, ctrlA, jumpFwd, "\x1b")
	wantCursor(t, e, 0, 0)
	in(e, "o")
	wantText(t, e, "ohello world")

	e.SetText("hello world")
	in(e, jumpBack, jumpBack, "o")
	wantText(t, e, "hello worldo")

	e.SetText("hello world")
	in(e, ctrlA, "x", jumpFwd, "o", "Y")
	wantText(t, e, "xhellYo world")
	in(e, undoKey)
	wantText(t, e, "xhello world")
}

// --- sticky column ---

func positionCursor(e *Editor, line, col int) {
	times(e, up, 20)
	times(e, down, line)
	in(e, ctrlA)
	times(e, right, col)
}

func TestEditorStickyColumn(t *testing.T) {
	t.Run("kept through a shorter line, up and down", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("2222222222x222\n\n1111111111_111111111111")
		wantCursor(t, e, 2, 23)
		in(e, ctrlA)
		times(e, right, 10)
		in(e, up)
		wantCursor(t, e, 1, 0)
		in(e, up)
		wantCursor(t, e, 0, 10)

		e.SetText("1111111111_111\n\n2222222222x222222222222")
		in(e, up, up, ctrlA)
		times(e, right, 10)
		in(e, down)
		wantCursor(t, e, 1, 0)
		in(e, down)
		wantCursor(t, e, 2, 10)
	})
	resets := []struct {
		name  string
		start [2]int
		moveU bool
		act   string
		after [2]int
		end   [2]int
	}{
		{"left arrow", [2]int{2, 5}, true, left, [2]int{0, 4}, [2]int{2, 4}},
		{"right arrow", [2]int{0, 5}, false, right, [2]int{2, 6}, [2]int{0, 6}},
		{"typing", [2]int{2, 8}, true, "X", [2]int{0, 9}, [2]int{2, 9}},
		{"backspace", [2]int{2, 8}, true, backspace, [2]int{0, 7}, [2]int{2, 7}},
	}
	for _, r := range resets {
		t.Run("reset by "+r.name, func(t *testing.T) {
			e := newTestEditor()
			e.SetText("1234567890\n\n1234567890")
			positionCursor(e, r.start[0], r.start[1])
			there, back := up, down
			if !r.moveU {
				there, back = down, up
			}
			in(e, there, there, r.act)
			wantCursor(t, e, r.after[0], r.after[1])
			in(e, back, back)
			wantCursor(t, e, r.end[0], r.end[1])
		})
	}
	t.Run("reset by line start and end", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("1234567890\n\n1234567890")
		positionCursor(e, 2, 8)
		in(e, up, ctrlA)
		wantCursor(t, e, 1, 0)
		in(e, up)
		wantCursor(t, e, 0, 0)

		e.SetText("12345\n\n1234567890")
		positionCursor(e, 2, 3)
		in(e, up, up)
		wantCursor(t, e, 0, 3)
		in(e, ctrlE, down, down)
		wantCursor(t, e, 2, 5)
	})
	t.Run("reset by word motion", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("hello world\n\nhello world")
		in(e, up, up, ctrlLeft)
		wantCursor(t, e, 0, 6)
		in(e, down, down)
		wantCursor(t, e, 2, 6)

		e.SetText("hello world\n\nhello world")
		in(e, up, up, ctrlA, down, down, ctrlRight)
		wantCursor(t, e, 2, 5)
		in(e, up, up)
		wantCursor(t, e, 0, 5)
	})
	t.Run("reset by undo", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("1234567890\n\n1234567890")
		positionCursor(e, 0, 8)
		in(e, down, down, "X")
		wantCursor(t, e, 2, 9)
		in(e, up, up)
		wantCursor(t, e, 0, 9)
		in(e, undoKey)
		wantCursor(t, e, 2, 8)
		in(e, up, up)
		wantCursor(t, e, 0, 8)
	})
	t.Run("through several short lines", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("1234567890\nab\ncd\nef\n1234567890")
		in(e, ctrlA)
		times(e, right, 7)
		times(e, up, 4)
		wantCursor(t, e, 0, 7)
		times(e, down, 4)
		wantCursor(t, e, 4, 7)
	})
	t.Run("through wrapped visual lines", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("short\n123456789012345678901234567890")
		e.Render(15)
		wantCursor(t, e, 1, 30)
		for _, line := range []int{1, 1, 0} {
			in(e, up)
			if l, _ := e.Cursor(); l != line {
				t.Fatalf("on line %d, want %d", l, line)
			}
		}
	})
	t.Run("setText resets it", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("1234567890\n\n1234567890")
		in(e, ctrlA)
		times(e, right, 8)
		in(e, up)
		e.SetText("abcdefghij\n\nabcdefghij")
		wantCursor(t, e, 2, 10)
		in(e, up, up)
		wantCursor(t, e, 0, 10)
	})
	t.Run("right at the end of the text sets it", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("111111111x1111111111\n\n333333333_")
		in(e, up, up, ctrlE)
		wantCursor(t, e, 0, 20)
		in(e, down, down)
		wantCursor(t, e, 2, 10)
		in(e, right)
		wantCursor(t, e, 2, 10)
		in(e, up, up)
		wantCursor(t, e, 0, 10)
	})
	t.Run("resizes", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("12345678901234567890\n\n12345678901234567890")
		in(e, ctrlA)
		times(e, right, 15)
		in(e, up, up)
		wantCursor(t, e, 0, 15)
		e.Render(12)
		in(e, down, down)
		if _, col := e.Cursor(); col != 4 {
			t.Fatalf("col %d, want 4", col)
		}

		e = newTestEditor()
		e.SetText("short\n12345678901234567890")
		in(e, ctrlA)
		times(e, right, 15)
		in(e, up)
		wantCursor(t, e, 0, 5)
		e.Render(10)
		in(e, down)
		wantCursor(t, e, 1, 8)
		in(e, up)
		wantCursor(t, e, 0, 5)
		e.Render(80)
		in(e, down)
		wantCursor(t, e, 1, 15)
	})
	t.Run("rewrapped lines", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("abcdefghijklmnopqr\n123456789012345678")
		positionCursor(e, 0, 18)
		e.Render(10)
		in(e, down)
		wantCursor(t, e, 1, 8)
		e.Render(80)
		in(e, up)
		wantCursor(t, e, 0, 8)
		in(e, down)
		wantCursor(t, e, 1, 8)

		e = newTestEditor()
		e.SetText("abcdefghijklmnopqr\n123456789012345678\nab")
		positionCursor(e, 0, 18)
		e.Render(10)
		in(e, down)
		wantCursor(t, e, 1, 8)
		e.Render(80)
		in(e, down)
		wantCursor(t, e, 2, 2)
		in(e, up)
		wantCursor(t, e, 1, 8)
	})
}

// --- paste markers ---

var markerRE = regexp.MustCompile(`\[paste #\d+ (\+\d+ lines|\d+ chars)\]`)

func pasteWithMarker(e *Editor) {
	in(e, paste(strings.TrimRight(strings.Repeat("line\n", 20), "\n")))
}

func bigPaste(tag string) string {
	var lines []string
	for i := 0; i < 12; i++ {
		lines = append(lines, tag+strconv.Itoa(i))
	}
	return strings.Join(lines, "\n")
}

func TestEditorPasteMarkers(t *testing.T) {
	t.Run("a large paste becomes a marker", func(t *testing.T) {
		e := newTestEditor()
		pasteWithMarker(e)
		if !regexp.MustCompile(`^\[paste #1 \+20 lines\]$`).MatchString(e.Text()) {
			t.Fatalf("text %q", e.Text())
		}
		e = newTestEditor()
		in(e, paste(strings.Repeat("x", 1001)))
		wantText(t, e, "[paste #1 1001 chars]")
	})
	t.Run("arrows, backspace and delete take a marker whole", func(t *testing.T) {
		e := newTestEditor()
		in(e, "A")
		pasteWithMarker(e)
		in(e, "B")
		marker := markerRE.FindString(e.Text())
		in(e, ctrlA, right)
		wantCursor(t, e, 0, 1)
		in(e, right)
		wantCursor(t, e, 0, 1+len(marker))
		in(e, right)
		wantCursor(t, e, 0, 2+len(marker))
		in(e, left, left)
		wantCursor(t, e, 0, 1)

		in(e, right, backspace)
		wantText(t, e, "AB")
		wantCursor(t, e, 0, 1)
		in(e, undoKey)
		wantText(t, e, "A"+marker+"B")

		in(e, ctrlA, right, deleteKey)
		wantText(t, e, "AB")
		wantCursor(t, e, 0, 1)
	})
	t.Run("word motion takes a marker whole", func(t *testing.T) {
		e := newTestEditor()
		in(e, "X", " ")
		pasteWithMarker(e)
		in(e, " ", "Y")
		marker := markerRE.FindString(e.Text())
		in(e, ctrlA, ctrlRight)
		wantCursor(t, e, 0, 1)
		in(e, ctrlRight)
		wantCursor(t, e, 0, 2+len(marker))
	})
	t.Run("undo restores the registry", func(t *testing.T) {
		e := newTestEditor()
		var submitted string
		e.OnSubmit = func(s string) { submitted = s }
		in(e, paste(bigPaste("alpha")), backspace, undoKey, enter)
		if submitted != bigPaste("alpha") {
			t.Fatalf("submitted %q", submitted)
		}

		in(e, paste(bigPaste("alpha")), paste(bigPaste("beta")), ctrlA, right, backspace, undoKey, enter)
		if submitted != bigPaste("alpha")+bigPaste("beta") {
			t.Fatalf("submitted %q", submitted)
		}

		in(e, paste(bigPaste("alpha")))
		e.SetText("replacement")
		in(e, undoKey, enter)
		if submitted != bigPaste("alpha") {
			t.Fatalf("submitted %q", submitted)
		}
	})
	t.Run("deleting a marker renumbers the rest in id order", func(t *testing.T) {
		e := newTestEditor()
		var submitted string
		e.OnSubmit = func(s string) { submitted = s }
		in(e, paste(bigPaste("alpha")), ctrlA, paste(bigPaste("beta")), ctrlA, paste(bigPaste("gamma")), ctrlE, backspace, enter)
		if submitted != bigPaste("gamma")+bigPaste("beta") {
			t.Fatalf("submitted %q", submitted)
		}
	})
	t.Run("two markers on a line", func(t *testing.T) {
		e := newTestEditor()
		pasteWithMarker(e)
		in(e, " ")
		pasteWithMarker(e)
		markers := markerRE.FindAllString(e.Text(), -1)
		if len(markers) != 2 {
			t.Fatalf("markers %q", markers)
		}
		in(e, ctrlA, right)
		wantCursor(t, e, 0, len(markers[0]))
		in(e, right, right)
		wantCursor(t, e, 0, len(markers[0])+1+len(markers[1]))
	})
	t.Run("a typed marker is ordinary text", func(t *testing.T) {
		e := newTestEditor()
		typeInto(e, "[paste #99 +5 lines]")
		in(e, ctrlA, right)
		wantCursor(t, e, 0, 1)
	})
	t.Run("a marker wider than the editor is split to fit", func(t *testing.T) {
		e := newTestEditor()
		in(e, paste(strings.TrimRight(strings.Repeat("line\n", 47), "\n")))
		for _, l := range e.Render(8) {
			if VisibleWidth(l) > 8 {
				t.Fatalf("line %q overflows", l)
			}
		}

		for _, lead := range []string{"", " "} {
			e = newTestEditor()
			typeInto(e, lead+strings.Repeat("b", 35))
			in(e, paste(strings.TrimRight(strings.Repeat("line\n", 27), "\n")))
			typeInto(e, "bbbb")
			times(e, left, 5)
			for _, l := range e.Render(54) {
				if VisibleWidth(l) > 54 {
					t.Fatalf("line %q is %d wide", l, VisibleWidth(l))
				}
			}
		}
	})
	t.Run("pasted text is expanded literally", func(t *testing.T) {
		text := "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\nline 10\ntokens $1 $2 $& $$ $` $' end"
		e := newTestEditor()
		var submitted string
		e.OnSubmit = func(s string) { submitted = s }
		in(e, paste(text))
		if e.ExpandedText() != text {
			t.Fatalf("expanded %q", e.ExpandedText())
		}
		in(e, enter)
		if submitted != text {
			t.Fatalf("submitted %q", submitted)
		}
	})
	t.Run("moving down into a marker lands on its start", func(t *testing.T) {
		e := newTestEditor()
		e.SetText("12345678901234567890\n\nhello ")
		in(e, paste(strings.Repeat("x", 2000)))
		e.Render(80)
		in(e, up, up, ctrlA)
		times(e, right, 10)
		in(e, down)
		wantCursor(t, e, 1, 0)
		in(e, down)
		wantCursor(t, e, 2, 6)
	})
	t.Run("the sticky column survives a marker line", func(t *testing.T) {
		e := newTestEditor()
		typeInto(e, "1234567890123456")
		in(e, "\n", "\n", paste(strings.Repeat("x", 2000)), "\n", "\n")
		typeInto(e, "abcdefghijklmnop")
		e.Render(30)
		times(e, up, 4)
		in(e, ctrlA)
		times(e, right, 10)
		for _, want := range [][2]int{{1, 0}, {2, 0}, {3, 0}, {4, 10}} {
			in(e, down)
			wantCursor(t, e, want[0], want[1])
		}
	})
	t.Run("a marker spanning visual lines", func(t *testing.T) {
		build := func() *Editor {
			e := newTestEditor()
			typeInto(e, "abcdefgh")
			in(e, paste(strings.TrimRight(strings.Repeat("line\n", 100), "\n")))
			typeInto(e, "ijklmnopqr\n123456789012345678")
			e.Render(20)
			return e
		}
		e := build()
		marker := markerRE.FindString(e.Text())
		in(e, up, ctrlA)
		times(e, right, 6)
		in(e, down)
		wantCursor(t, e, 0, 8)
		in(e, down)
		wantCursor(t, e, 0, 8+len(marker))
		in(e, up)
		wantCursor(t, e, 0, 8)
		in(e, up)
		wantCursor(t, e, 0, 6)

		e = build()
		in(e, up, ctrlA)
		times(e, right, 3)
		in(e, down)
		wantCursor(t, e, 0, 8)
		in(e, down)
		wantCursor(t, e, 1, 3)
		in(e, up)
		wantCursor(t, e, 0, 8)
		in(e, up)
		wantCursor(t, e, 0, 3)
	})
}

func TestEditorPastedPathGetsASpaceAfterAWord(t *testing.T) {
	e := newTestEditor()
	typeInto(e, "see")
	in(e, paste("/tmp/a.png"))
	wantText(t, e, "see /tmp/a.png")
	e.SetText("see ")
	in(e, paste("./b"))
	wantText(t, e, "see ./b")
}

// TestEditorSwappedEnter: with enter bound to new lines and shift+enter to
// submit, a backslash before the cursor makes enter submit; a bare line feed
// still starts a line whatever the bindings say.
func TestEditorSwappedEnter(t *testing.T) {
	e := newTestEditor()
	e.Keybindings = NewKeybindings(TUIKeybindings, map[string][]string{
		"tui.input.submit":  {"shift+enter"},
		"tui.input.newLine": {"enter"},
	})
	var submitted []string
	e.OnSubmit = func(s string) { submitted = append(submitted, s) }
	in(e, "a", enter)
	wantText(t, e, "a\n")
	in(e, "b", "\\", enter)
	if len(submitted) != 1 || submitted[0] != "a\nb" {
		t.Fatalf("submitted %q", submitted)
	}

	e = newTestEditor()
	e.Keybindings = NewKeybindings(TUIKeybindings, map[string][]string{"tui.input.newLine": {"alt+n"}})
	in(e, "a", "\n", "b", "\x1b\r", "c")
	wantText(t, e, "a\nb\nc")
}

func TestEditorDisableSubmit(t *testing.T) {
	e := newTestEditor()
	e.DisableSubmit = true
	called := false
	e.OnSubmit = func(string) { called = true }
	in(e, "a", enter)
	if called {
		t.Fatal("submitted while disabled")
	}
	wantText(t, e, "a")
}

func TestEditorPageScroll(t *testing.T) {
	e := newTestEditor() // 24 rows: a page is 7 lines
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "line"+strconv.Itoa(i))
	}
	e.SetText(strings.Join(lines, "\n"))
	e.Render(40)
	in(e, "\x1b[5~")
	wantCursor(t, e, 12, 6)
	in(e, "\x1b[5~", "\x1b[5~")
	wantCursor(t, e, 0, 5)
	in(e, "\x1b[6~")
	wantCursor(t, e, 7, 5)
}

// --- autocomplete ---

// funcProvider is a provider made of functions, as Pi's tests write them.
type funcProvider struct {
	suggest  func(ctx context.Context, lines []string, line, col int, force bool) *AutocompleteSuggestions
	triggers []string
}

func (p funcProvider) Suggestions(ctx context.Context, lines []string, line, col int, force bool) *AutocompleteSuggestions {
	return p.suggest(ctx, lines, line, col, force)
}

// ApplyCompletion replaces the prefix with the value, as Pi's tests do.
func (funcProvider) ApplyCompletion(lines []string, line, col int, item AutocompleteItem, prefix string) ([]string, int, int) {
	out := slices.Clone(lines)
	out[line] = lines[line][:col-len(prefix)] + item.Value + lines[line][col:]
	return out, line, col - len(prefix) + len(item.Value)
}

func (p funcProvider) TriggerCharacters() []string { return p.triggers }

func items(vs ...string) []AutocompleteItem {
	var out []AutocompleteItem
	for _, v := range vs {
		out = append(out, AutocompleteItem{Value: v, Label: v})
	}
	return out
}

func before(lines []string, line, col int) string { return lines[line][:col] }

func TestEditorAutocompleteTab(t *testing.T) {
	t.Run("a single path is applied at once, and undoes", func(t *testing.T) {
		e := newTestEditor()
		e.SetAutocompleteProvider(funcProvider{suggest: func(_ context.Context, l []string, line, col int, force bool) *AutocompleteSuggestions {
			if force && before(l, line, col) == "Work" {
				return &AutocompleteSuggestions{Items: items("Workspace/"), Prefix: "Work"}
			}
			return nil
		}})
		typeInto(e, "Work")
		in(e, "\t")
		wantText(t, e, "Workspace/")
		if e.IsShowingAutocomplete() {
			t.Fatal("a list was shown for one answer")
		}
		in(e, undoKey)
		wantText(t, e, "Work")
	})
	t.Run("several paths show a list, and tab takes the first", func(t *testing.T) {
		e := newTestEditor()
		e.SetAutocompleteProvider(funcProvider{suggest: func(_ context.Context, l []string, line, col int, force bool) *AutocompleteSuggestions {
			if force && before(l, line, col) == "src" {
				return &AutocompleteSuggestions{Items: items("src/", "src.txt"), Prefix: "src"}
			}
			return nil
		}})
		typeInto(e, "src")
		in(e, "\t")
		wantText(t, e, "src")
		if !e.IsShowingAutocomplete() {
			t.Fatal("no list")
		}
		in(e, "\t")
		wantText(t, e, "src/")
		if e.IsShowingAutocomplete() {
			t.Fatal("the list stayed open")
		}
	})
	t.Run("a tab-opened list follows typing", func(t *testing.T) {
		all := items("readme.md", "package.json", "src/", "dist/")
		e := newTestEditor()
		e.SetAutocompleteProvider(funcProvider{suggest: func(_ context.Context, l []string, line, col int, force bool) *AutocompleteSuggestions {
			prefix := before(l, line, col)
			if !force && !strings.Contains(prefix, "/") && !strings.HasPrefix(prefix, ".") {
				return nil
			}
			var matched []AutocompleteItem
			for _, f := range all {
				if strings.HasPrefix(strings.ToLower(f.Value), strings.ToLower(prefix)) {
					matched = append(matched, f)
				}
			}
			if len(matched) == 0 {
				return nil
			}
			return &AutocompleteSuggestions{Items: matched, Prefix: prefix}
		}})
		in(e, "\t")
		if !e.IsShowingAutocomplete() {
			t.Fatal("no list on an empty prompt")
		}
		for _, ch := range []string{"r", "e"} {
			in(e, ch)
			if !e.IsShowingAutocomplete() {
				t.Fatalf("the list closed after %q", e.Text())
			}
		}
		in(e, "\t")
		wantText(t, e, "readme.md")
	})
	t.Run("escape closes the list", func(t *testing.T) {
		e := newTestEditor()
		e.SetAutocompleteProvider(funcProvider{suggest: func(context.Context, []string, int, int, bool) *AutocompleteSuggestions {
			return &AutocompleteSuggestions{Items: items("a", "b"), Prefix: ""}
		}})
		in(e, "\t")
		if !e.IsShowingAutocomplete() {
			t.Fatal("no list")
		}
		in(e, "\x1b")
		if e.IsShowingAutocomplete() {
			t.Fatal("escape left the list open")
		}
	})
}

func TestEditorAutocompleteSlashCommands(t *testing.T) {
	commands := []AutocompleteItem{{Value: "/model", Label: "model"}, {Value: "/help", Label: "help"}}
	e := newTestEditor()
	e.SetAutocompleteProvider(funcProvider{suggest: func(_ context.Context, l []string, line, col int, _ bool) *AutocompleteSuggestions {
		prefix := before(l, line, col)
		if !strings.HasPrefix(prefix, "/") {
			return nil
		}
		var matched []AutocompleteItem
		for _, c := range commands {
			if strings.HasPrefix(c.Value, prefix[1:]) {
				matched = append(matched, c)
			}
		}
		if len(matched) == 0 {
			return nil
		}
		return &AutocompleteSuggestions{Items: matched, Prefix: prefix}
	}})
	in(e, "/")
	if !e.IsShowingAutocomplete() {
		t.Fatal("no list after /")
	}
	in(e, backspace)
	wantText(t, e, "")
	if e.IsShowingAutocomplete() {
		t.Fatal("the list survived deleting the slash")
	}
}

// argProvider completes /argtest's argument from a fixed list, filtered by
// prefix or not.
func argProvider(command string, filter bool, args ...string) funcProvider {
	return funcProvider{suggest: func(_ context.Context, l []string, line, col int, _ bool) *AutocompleteSuggestions {
		b := before(l, line, col)
		rest, ok := strings.CutPrefix(b, "/"+command+" ")
		if !ok || rest == "" || strings.Contains(rest, " ") {
			return nil
		}
		var out []AutocompleteItem
		for _, a := range args {
			if !filter || strings.HasPrefix(a, rest) {
				out = append(out, AutocompleteItem{Value: a, Label: a})
			}
		}
		if len(out) == 0 {
			return nil
		}
		return &AutocompleteSuggestions{Items: out, Prefix: rest}
	}}
}

func TestEditorAutocompleteEnterOnArguments(t *testing.T) {
	cases := []struct {
		name, typed, want string
		provider          funcProvider
	}{
		{"an exact value is kept", "/argtest two", "/argtest two", argProvider("argtest", true, "one", "two", "three")},
		{"the first prefix match is taken", "/argtest t", "/argtest two", argProvider("argtest", true, "two", "three", "twelve")},
		{"a unique prefix is highlighted", "/argtest tw", "/argtest two", argProvider("argtest", false, "one", "two", "three")},
		{"the first of several matches", "/argtest t", "/argtest two", argProvider("argtest", false, "one", "two", "three")},
		{"a model-like exact value", "/model gpt-4o-mini", "/model gpt-4o-mini", argProvider("model", true, "gpt-4o", "gpt-4o-mini", "claude-sonnet")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTestEditor()
			e.SetAutocompleteProvider(c.provider)
			submitted := false
			e.OnSubmit = func(string) { submitted = true }
			typeInto(e, c.typed)
			if !e.IsShowingAutocomplete() {
				t.Fatal("no list")
			}
			in(e, enter)
			wantText(t, e, c.want)
			if submitted {
				t.Fatal("enter on an argument submitted")
			}
		})
	}
}

func TestEditorAutocompleteEnterOnACommandSubmits(t *testing.T) {
	e := newTestEditor()
	e.SetAutocompleteProvider(NewCombinedAutocompleteProvider([]SlashCommand{{Name: "help"}, {Name: "hotkeys"}}, t.TempDir(), nil))
	var submitted string
	e.OnSubmit = func(s string) { submitted = s }
	typeInto(e, "/he")
	in(e, enter)
	if submitted != "/help" {
		t.Fatalf("submitted %q", submitted)
	}
}

func TestEditorAutocompleteMovesRequery(t *testing.T) {
	e := newTestEditor()
	e.SetAutocompleteProvider(funcProvider{suggest: func(_ context.Context, l []string, line, col int, _ bool) *AutocompleteSuggestions {
		b := before(l, line, col)
		if !strings.HasPrefix(b, "/") {
			return nil
		}
		if i := strings.IndexByte(b, ' '); i >= 0 {
			return &AutocompleteSuggestions{Items: items("repo", "message", "help"), Prefix: b[i+1:]}
		}
		return &AutocompleteSuggestions{Items: items("cmd"), Prefix: b}
	}})
	typeInto(e, "/cmd ")
	if !strings.Contains(StripSequences(strings.Join(e.Render(80), "\n")), "repo") {
		t.Fatal("no argument list at '/cmd '")
	}
	in(e, left)
	if got := StripSequences(strings.Join(e.Render(80), "\n")); strings.Contains(got, "repo") || strings.Contains(got, "message") {
		t.Fatalf("the argument list survived moving back into the command:\n%s", got)
	}
}

func TestEditorAutocompleteWithTheCombinedProvider(t *testing.T) {
	cmds := []SlashCommand{
		{Name: "help", Description: "Show help"},
		{Name: "model", Description: "Switch model", ArgumentCompletions: func(context.Context, string) []AutocompleteItem {
			return items("claude-opus")
		}},
		{Name: "load-skills", Description: "Load skills", ArgumentCompletions: func(_ context.Context, prefix string) []AutocompleteItem {
			if strings.HasPrefix(prefix, "s") {
				return items("skill-a")
			}
			return nil
		}},
	}
	e := newTestEditor()
	e.SetAutocompleteProvider(NewCombinedAutocompleteProvider(cmds, t.TempDir(), nil))
	e.SetText("/load-skills ")
	in(e, "s")
	if !e.IsShowingAutocomplete() {
		t.Fatal("no argument list")
	}
	in(e, "\t")
	wantText(t, e, "/load-skills skill-a")

	e = newTestEditor()
	e.SetAutocompleteProvider(NewCombinedAutocompleteProvider(cmds, t.TempDir(), nil))
	typeInto(e, "/he")
	if !e.IsShowingAutocomplete() {
		t.Fatal("no command list")
	}
	in(e, "\t")
	wantText(t, e, "/help ")
	if e.IsShowingAutocomplete() {
		t.Fatal("arguments were offered for a command without any")
	}
}

func TestEditorAutocompleteListRendersBelow(t *testing.T) {
	e := newTestEditor()
	e.SetAutocompleteProvider(NewCombinedAutocompleteProvider([]SlashCommand{{Name: "help", Description: "Show help"}, {Name: "hotkeys", Description: "Keys"}}, t.TempDir(), nil))
	typeInto(e, "/h")
	out := e.Render(60)
	if len(out) != 5 || !strings.HasPrefix(out[3], "→ help") || !strings.Contains(out[3], "Show help") {
		t.Fatalf("render:\n%s", strings.Join(out, "\n"))
	}
	for _, l := range out {
		if VisibleWidth(l) != 60 {
			t.Fatalf("line %q is %d wide", l, VisibleWidth(l))
		}
	}
	in(e, down)
	if out := e.Render(60); !strings.HasPrefix(out[4], "→ hotkeys") {
		t.Fatalf("down did not move the selection:\n%s", strings.Join(out, "\n"))
	}
}

func TestEditorPasteDoesNotOpenCompletion(t *testing.T) {
	calls := 0
	e := newTestEditor()
	e.SetAutocompleteProvider(funcProvider{suggest: func(context.Context, []string, int, int, bool) *AutocompleteSuggestions {
		calls++
		return nil
	}})
	in(e, paste("look at @node_modules/react/index.js please"))
	wantText(t, e, "look at @node_modules/react/index.js please")
	if calls != 0 || e.IsShowingAutocomplete() {
		t.Fatalf("a paste asked for completions %d times", calls)
	}
}

// loopTestHost runs completion as the interface does: posted work is queued
// and run on the test's goroutine by settle.
type loopTestHost struct {
	rowsHost
	work chan func()
}

func newLoopHost() *loopTestHost { return &loopTestHost{rowsHost{24}, make(chan func(), 64)} }

func (h *loopTestHost) Do(fn func()) { h.work <- fn }

// settle runs posted work until none arrives for a while.
func (h *loopTestHost) settle(wait time.Duration) {
	for {
		select {
		case fn := <-h.work:
			fn()
		case <-time.After(wait):
			return
		}
	}
}

func TestEditorAutocompleteDebouncesTriggers(t *testing.T) {
	for _, c := range []struct {
		typed    string
		triggers []string
	}{{"@mai", nil}, {"#298", nil}, {"$sk", []string{"$"}}} {
		host := newLoopHost()
		e := NewEditor(host, EditorTheme{}, EditorOptions{})
		var calls atomic.Int32
		e.SetAutocompleteProvider(funcProvider{triggers: c.triggers, suggest: func(_ context.Context, l []string, line, col int, _ bool) *AutocompleteSuggestions {
			calls.Add(1)
			return &AutocompleteSuggestions{Items: items("x"), Prefix: before(l, line, col)}
		}})
		typeInto(e, c.typed)
		if calls.Load() != 0 || e.IsShowingAutocomplete() {
			t.Fatalf("%q: asked before the pause", c.typed)
		}
		host.settle(60 * time.Millisecond)
		if calls.Load() != 1 || !e.IsShowingAutocomplete() {
			t.Fatalf("%q: %d requests after the pause, showing %v", c.typed, calls.Load(), e.IsShowingAutocomplete())
		}
	}
}

func TestEditorAutocompleteTriggersFollowTheProvider(t *testing.T) {
	host := newLoopHost()
	e := NewEditor(host, EditorTheme{}, EditorOptions{})
	e.SetAutocompleteProvider(funcProvider{triggers: []string{"$"}, suggest: func(context.Context, []string, int, int, bool) *AutocompleteSuggestions {
		return &AutocompleteSuggestions{Items: items("$skill-name"), Prefix: "$"}
	}})
	calls := 0
	e.SetAutocompleteProvider(funcProvider{suggest: func(context.Context, []string, int, int, bool) *AutocompleteSuggestions {
		calls++
		return &AutocompleteSuggestions{Items: items("$skill-name"), Prefix: "$"}
	}})
	typeInto(e, "$s")
	host.settle(60 * time.Millisecond)
	if calls != 0 || e.IsShowingAutocomplete() {
		t.Fatalf("an old provider's trigger still opened completion (%d calls)", calls)
	}
}

func TestEditorAutocompleteCancelsAStaleRequest(t *testing.T) {
	host := newLoopHost()
	e := NewEditor(host, EditorTheme{}, EditorOptions{})
	var aborts atomic.Int32
	e.SetAutocompleteProvider(funcProvider{suggest: func(ctx context.Context, _ []string, _, _ int, _ bool) *AutocompleteSuggestions {
		select {
		case <-ctx.Done():
			aborts.Add(1)
			return nil
		case <-time.After(500 * time.Millisecond):
			return &AutocompleteSuggestions{Items: items("@main.ts"), Prefix: "@main"}
		}
	}})
	typeInto(e, "@mai")
	host.settle(250 * time.Millisecond)
	in(e, "n")
	time.Sleep(50 * time.Millisecond)
	if aborts.Load() != 1 {
		t.Fatalf("%d requests stopped, want 1", aborts.Load())
	}
	e.cancelAutocomplete()
}

func TestEditorAutocompleteUndo(t *testing.T) {
	e := newTestEditor()
	e.SetAutocompleteProvider(funcProvider{suggest: func(_ context.Context, l []string, line, col int, _ bool) *AutocompleteSuggestions {
		if before(l, line, col) == "di" {
			return &AutocompleteSuggestions{Items: items("dist/"), Prefix: "di"}
		}
		return nil
	}})
	typeInto(e, "di")
	in(e, "\t")
	wantText(t, e, "dist/")
	in(e, undoKey)
	wantText(t, e, "di")
}
