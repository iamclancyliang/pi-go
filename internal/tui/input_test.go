package tui

import (
	"strings"
	"testing"
)

func typeInput(i *Input, text string) {
	for _, r := range text {
		i.HandleKey(Key{Text: r})
	}
}

// TestInputSubmitsWhatWasTypedIncludingABackslash: one line has no backslash
// workaround to apply.
func TestInputSubmitsWhatWasTypedIncludingABackslash(t *testing.T) {
	i := &Input{}
	submitted := ""
	i.OnSubmit = func(v string) { submitted = v }
	typeInput(i, `hello\`)
	i.HandleKey(Key{Name: "enter"})
	if submitted != `hello\` {
		t.Fatalf("submitted %q", submitted)
	}
}

func TestInputEditsByGraphemeAndWord(t *testing.T) {
	i := &Input{}
	typeInput(i, "héllo wörld")
	i.HandleKey(Key{Name: "ctrl+w"})
	if i.Value() != "héllo " {
		t.Fatalf("after deleting a word: %q", i.Value())
	}
	i.HandleKey(Key{Name: "ctrl+y"})
	if i.Value() != "héllo wörld" {
		t.Fatalf("after yanking: %q", i.Value())
	}
	i.HandleKey(Key{Name: "left"})
	i.HandleKey(Key{Name: "backspace"})
	if i.Value() != "héllo wörd" {
		t.Fatalf("after one backspace: %q", i.Value())
	}
	i.HandleKey(Key{Name: "ctrl+-"})
	if i.Value() != "héllo wörld" {
		t.Fatalf("after undo: %q", i.Value())
	}
	i.HandleKey(Key{Name: "ctrl+a"})
	i.HandleKey(Key{Name: "ctrl+k"})
	if i.Value() != "" {
		t.Fatalf("after killing to the end: %q", i.Value())
	}
}

func TestInputPastesOneLine(t *testing.T) {
	i := &Input{}
	i.HandleKey(Key{Name: "paste", Paste: "a\r\nb\tc\n"})
	if i.Value() != "ab    c" {
		t.Fatalf("pasted %q", i.Value())
	}
}

// TestInputNeverOverflowsWithWideText, with the cursor at the start, in the
// middle and at the end — Pi's render test.
func TestInputNeverOverflowsWithWideText(t *testing.T) {
	const width = 93
	cases := []string{
		"가나다라마바사아자차카타파하 한글 텍스트가 터미널 너비를 초과하면 크래시가 발생합니다 이것은 재현용 테스트입니다",
		"これはテスト文章です。日本語のテキストが正しく表示されるかどうかを確認するためのサンプルテキストです。あいうえお",
		"这是一段测试文本，用于验证中文字符在终端中的显示宽度是否被正确计算，如果不正确就会导致用户界面崩溃的问题",
		"ＡＢＣＤＥＦＧＨＩＪＫＬＭＮＯＰＱＲＳＴＵＶＷＸＹＺ０１２３４５６７８９ａｂｃｄｅｆｇｈｉｊｋｌｍ",
	}
	for _, text := range cases {
		for _, where := range []string{"start", "middle", "end"} {
			i := &Input{}
			i.SetValue(text)
			switch where {
			case "middle":
				for range 10 {
					i.HandleKey(Key{Name: "right"})
				}
			case "end":
				i.HandleKey(Key{Name: "ctrl+e"})
			}
			lines := i.Render(width)
			if len(lines) != 1 {
				t.Fatalf("%s %s: %d lines", where, text[:12], len(lines))
			}
			if w := VisibleWidth(lines[0]); w > width {
				t.Fatalf("%s: width %d > %d", where, w, width)
			}
		}
	}
}

func TestInputShowsTheCursorAndMarksItWhenFocused(t *testing.T) {
	i := &Input{}
	typeInput(i, "ab")
	i.HandleKey(Key{Name: "left"})
	plain := i.Render(20)[0]
	if !strings.HasPrefix(plain, "> a\x1b[7mb\x1b[27m") || strings.Contains(plain, CursorMarker) {
		t.Fatalf("unfocused: %q", plain)
	}
	i.SetFocused(true)
	focused := i.Render(20)[0]
	if !strings.HasPrefix(focused, "> a"+CursorMarker+"\x1b[7mb\x1b[27m") {
		t.Fatalf("focused: %q", focused)
	}
	if VisibleWidth(focused) != 20 {
		t.Fatalf("not padded to the width: %q", focused)
	}
}

func TestAMaskedInputShowsNothingItWasGiven(t *testing.T) {
	i := &Input{Mask: "*"}
	typeInput(i, "sk-1")
	line := i.Render(20)[0]
	if strings.Contains(line, "sk-1") || !strings.Contains(line, "****") {
		t.Fatalf("masked: %q", line)
	}
	if i.Value() != "sk-1" {
		t.Fatalf("the value was masked too: %q", i.Value())
	}
}

func TestSliceByColumnKeepsStylesAndWideCharacters(t *testing.T) {
	line := "\x1b[31mab\x1b[39mc漢d"
	if got := SliceByColumn(line, 1, 3, false); got != "\x1b[31mb\x1b[39mc漢" {
		t.Fatalf("got %q", got)
	}
	// Strict leaves out a wide character that would straddle the end. A code
	// met inside the range goes out as met, before the styles carried from in
	// front of it — Pi's order.
	if got := SliceByColumn(line, 2, 2, true); got != "\x1b[39m\x1b[31mc" {
		t.Fatalf("strict: %q", got)
	}
	if got := SliceByColumn(line, 2, 2, false); got != "\x1b[39m\x1b[31mc漢" {
		t.Fatalf("loose: %q", got)
	}
	if got := SliceByColumn("abc", 5, 2, false); got != "" {
		t.Fatalf("past the end: %q", got)
	}
}

func settingsTheme() SettingsListTheme {
	return SettingsListTheme{
		Label:       func(s string, _ bool) string { return s },
		Value:       func(s string, _ bool) string { return s },
		Description: func(s string) string { return s },
		Cursor:      "→ ",
		Hint:        func(s string) string { return s },
	}
}

func TestSettingsListCyclesValuesAndOpensSubmenus(t *testing.T) {
	var changed []string
	var cancelled bool
	items := []*SettingItem{
		{ID: "a", Label: "Alpha", CurrentValue: "true", Values: []string{"true", "false"}, Description: "the first"},
		{ID: "b", Label: "Beta", CurrentValue: "x", Submenu: func(current string, done func(string, bool)) Component {
			return submenuStub{current: current, done: done}
		}},
	}
	l := NewSettingsList(items, 10, settingsTheme(), func(id, v string) { changed = append(changed, id+"="+v) }, func() { cancelled = true }, false)
	l.HandleKey(Key{Name: "enter"})
	l.HandleKey(Key{Text: ' '})
	if strings.Join(changed, " ") != "a=false a=true" {
		t.Fatalf("cycled %v", changed)
	}
	rendered := strings.Join(l.Render(40), "\n")
	if !strings.Contains(rendered, "→ Alpha  true") || !strings.Contains(rendered, "  the first") || !strings.Contains(rendered, "Enter/Space to change") {
		t.Fatalf("rendered:\n%s", rendered)
	}
	l.HandleKey(Key{Name: "down"})
	l.HandleKey(Key{Name: "enter"})
	if got := strings.Join(l.Render(40), "\n"); got != "submenu for x" {
		t.Fatalf("the submenu is not drawn: %q", got)
	}
	l.HandleKey(Key{Text: 'y'})
	if items[1].CurrentValue != "y" || changed[len(changed)-1] != "b=y" {
		t.Fatalf("the submenu's answer did not land: %v %v", items[1].CurrentValue, changed)
	}
	if got := strings.Join(l.Render(40), "\n"); !strings.Contains(got, "→ Beta   y") {
		t.Fatalf("after the submenu:\n%s", got)
	}
	l.HandleKey(Key{Name: "escape"})
	if !cancelled {
		t.Fatal("escape did not cancel")
	}
}

type submenuStub struct {
	current string
	done    func(string, bool)
}

func (s submenuStub) Invalidate()         {}
func (s submenuStub) Render(int) []string { return []string{"submenu for " + s.current} }
func (s submenuStub) HandleKey(k Key)     { s.done(string(k.Text), true) }

func TestSettingsListSearches(t *testing.T) {
	items := []*SettingItem{
		{ID: "a", Label: "Auto-compact", CurrentValue: "true"},
		{ID: "b", Label: "Steering mode", CurrentValue: "all"},
	}
	l := NewSettingsList(items, 10, settingsTheme(), func(string, string) {}, func() {}, true)
	for _, r := range "steer" {
		l.HandleKey(Key{Text: r})
	}
	got := strings.Join(l.Render(40), "\n")
	if strings.Contains(got, "Auto-compact") || !strings.Contains(got, "→ Steering mode") || !strings.Contains(got, "Type to search") {
		t.Fatalf("searched:\n%s", got)
	}
	// A space with a search typed is part of it, not a change.
	l.HandleKey(Key{Text: ' '})
	if items[1].CurrentValue != "all" {
		t.Fatal("a space in the search changed the value")
	}
}
