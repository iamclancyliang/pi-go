package tui

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The cases below are Pi's own (packages/tui/test/truncate-to-width.test.ts,
// wrap-ansi.test.ts and regression-regional-indicator-width.test.ts at the
// pin), with Pi's expected values: a width that disagrees with Pi's is a line
// that wraps differently in one program than the other.

func TestVisibleWidthMatchesPi(t *testing.T) {
	cases := map[string]int{
		"\t\x1b[31m界\x1b[0m": 5, // tabs inline, styling skipped
		"र्क":                2, // Indic conjuncts take a cell per consonant
		"नेटवर्क":            5,
		"सर्वाधिकार सुरक्षित। ऑर्डर पर क्लिक करें": 33,
		"র্ক":                                   2,
		"ર્ક":                                   2,
		"ର୍କ":                                   2,
		"ర్క":                                   2,
		"ര്‍ക":                                  2,
		"é":                                    1, // ordinary combining marks are zero-width
		"čřžůú":                                 5,
		"שָׁ":                                   1,
		"بّ":                                    1,
		"རྐ":                                    1,
		"ᜠ᜴":                                    1,
		"가〮":                                    2,
		"가〯":                                    2,
		"网络":                                    4, // CJK and Japanese
		"ネットワーク":                                12,
		"が":                                     2,
		"が":                                    2,
		"ကာ":                                    2, // Myanmar marks terminals give a cell to
		"ကေ":                                    2,
		"က်":                                    2,
		"ကျ":                                    2,
		"ကြ":                                    2,
		"ကဳ":                                    2,
		"ကဴ":                                    2,
		"ကဵ":                                    2,
		"ကး":                                    2,
		"ကို":                                   1,
		"က္":                                    1,
		"ำ":                                     1, // Thai and Lao AM
		"ຳ":                                     1,
		"กำ":                                    2,
		"ກຳ":                                    2,
		"\x1b]133;A\x07hello\x1b]133;B\x07":     5, // OSC markers, BEL and ST
		"\x1b]133;A\x1b\\hello\x1b]133;B\x1b\\": 5,
		"🇨":                                     2, // half a flag, as a stream delivers it
		"🇨🇳":                                    2,
		"🙂":                                     2,
	}
	for s, want := range cases {
		if got := VisibleWidth(s); got != want {
			t.Errorf("VisibleWidth(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestThaiAndLaoAMAreDecomposedOnlyForTheTerminal(t *testing.T) {
	if got := normalizeOutput("ำ"); got != "ํา" {
		t.Errorf("normalizeOutput(ำ) = %q", got)
	}
	if got := normalizeOutput("ຳ"); got != "ໍາ" {
		t.Errorf("normalizeOutput(ຳ) = %q", got)
	}
	if VisibleWidth(normalizeOutput("ำabc")) != VisibleWidth("ำabc") {
		t.Error("decomposing AM changed the measured width")
	}
	if got := normalizeOutput("a\tb\x1b]8;;x\ty\x07"); got != "a   b\x1b]8;;x\ty\x07" {
		t.Errorf("a tab inside a control sequence was expanded: %q", got)
	}
}

func TestTruncateToWidthMatchesPi(t *testing.T) {
	huge := strings.Repeat("🙂界", 100_000)
	if got := TruncateToWidth(huge, 40, "…", false); VisibleWidth(got) > 40 || !strings.HasSuffix(got, "…\x1b[0m") {
		t.Errorf("large unicode input: width %d, %q", VisibleWidth(got), got[len(got)-10:])
	}

	styled := TruncateToWidth("\x1b[31m"+strings.Repeat("hello ", 1000)+"\x1b[0m", 20, "…", false)
	if VisibleWidth(styled) > 20 || !strings.Contains(styled, "\x1b[31m") || !strings.HasSuffix(styled, "\x1b[0m…\x1b[0m") {
		t.Errorf("styling around the ellipsis: %q", styled)
	}

	open, closeLink := "\x1b]8;;https://example.com\x07", "\x1b]8;;\x07"
	if got := TruncateToWidth(open+"some-longer-label-here"+closeLink, 15, "...", false); got != open+"some-longer-"+closeLink+"\x1b[0m...\x1b[0m" {
		t.Errorf("a cut link label is not closed: %q", got)
	}

	if got := TruncateToWidth("abc\x1bnot-ansi "+strings.Repeat("🙂", 1000), 20, "…", false); VisibleWidth(got) > 20 {
		t.Errorf("a malformed escape: width %d", VisibleWidth(got))
	}

	if got := TruncateToWidth("abcdef", 1, "🙂", false); got != "" {
		t.Errorf("a wide ellipsis that cannot fit: %q", got)
	}
	if got := TruncateToWidth("abcdef", 2, "🙂", false); got != "\x1b[0m🙂\x1b[0m" {
		t.Errorf("a wide ellipsis that just fits: %q", got)
	}
	if got := TruncateToWidth("a", 2, "🙂", false); got != "a" {
		t.Errorf("text that fits: %q", got)
	}
	if got := TruncateToWidth("界", 2, "🙂", false); got != "界" {
		t.Errorf("wide text that fits: %q", got)
	}
	if got := TruncateToWidth("🙂界🙂界🙂界", 8, "…", true); VisibleWidth(got) != 8 {
		t.Errorf("padding: width %d", VisibleWidth(got))
	}
	if got := TruncateToWidth("\x1b[31m"+strings.Repeat("hello", 100), 10, "", false); VisibleWidth(got) > 10 || !strings.HasSuffix(got, "\x1b[0m") {
		t.Errorf("no ellipsis: %q", got)
	}
	if got := TruncateToWidth("🙂\t界 \x1b_abc\x07", 7, "…", true); got != "🙂\t\x1b[0m…\x1b[0m " {
		t.Errorf("a contiguous prefix: %q", got)
	}
}

func TestWrapTextMatchesPi(t *testing.T) {
	const ul, ulOff, reset = "\x1b[4m", "\x1b[24m", "\x1b[0m"

	url := "https://example.com/very/long/path/that/will/wrap"
	wrapped := WrapText("read this thread "+ul+url+ulOff, 40)
	if wrapped[0] != "read this thread" || !strings.HasPrefix(wrapped[1], ul) || !strings.Contains(wrapped[1], "https://") {
		t.Errorf("underline placement: %q", wrapped)
	}

	if wrapped := WrapText(ul+"underlined text here "+ulOff+"more", 18); strings.Contains(wrapped[0], " "+ulOff) {
		t.Errorf("space before the underline reset: %q", wrapped)
	}

	wrapped = WrapText("prefix "+ul+"https://example.com/very/long/path/that/will/definitely/wrap"+ulOff+" suffix", 30)
	for _, line := range wrapped[1 : len(wrapped)-1] {
		if strings.Contains(line, ul) && (!strings.HasSuffix(line, ulOff) || strings.HasSuffix(line, reset)) {
			t.Errorf("underline bleeds into padding: %q", line)
		}
	}

	wrapped = WrapText("\x1b[44mhello world this is blue background text"+reset, 15)
	for i, line := range wrapped {
		if !strings.Contains(line, "\x1b[44m") {
			t.Errorf("line %d lost the background: %q", i, line)
		}
		if i < len(wrapped)-1 && strings.HasSuffix(line, reset) {
			t.Errorf("line %d ends in a full reset, killing the background for padding: %q", i, line)
		}
	}

	if got := WrapText("first\nsecond\r\nthird\rfourth", 80); !reflect.DeepEqual(got, []string{"first", "second", "third", "fourth"}) {
		t.Errorf("line endings: %q", got)
	}
	red := "\x1b[31m"
	if got := WrapText(red+"first\r\nsecond\rthird"+reset, 80); !reflect.DeepEqual(got, []string{red + "first", red + "second", red + "third" + reset}) {
		t.Errorf("styles across line endings: %q", got)
	}

	for _, line := range WrapText("hello world this is a test", 10) {
		if VisibleWidth(line) > 10 {
			t.Errorf("a plain line overflows: %q", line)
		}
	}

	cjk := "This is an example 中文汉字测试段落内容中文汉字测试段落内容."
	if got := WrapText(cjk, 40); !reflect.DeepEqual(got, []string{"This is an example 中文汉字测试段落内容", "中文汉字测试段落内容."}) {
		t.Errorf("CJK breaking: %q", got)
	}
	if got := WrapText(red+cjk+reset, 40); !reflect.DeepEqual(got, []string{red + "This is an example 中文汉字测试段落内容", red + "中文汉字测试段落内容." + reset}) {
		t.Errorf("styled CJK breaking: %q", got)
	}

	if got := WrapText("  ", 1); VisibleWidth(got[0]) > 1 {
		t.Errorf("trailing whitespace overflows: %q", got)
	}

	wrapped = WrapText(red+"hello world this is red"+reset, 10)
	for i, line := range wrapped {
		if i > 0 && !strings.HasPrefix(line, red) {
			t.Errorf("continuation %d lost its colour: %q", i, line)
		}
		if i < len(wrapped)-1 && strings.HasSuffix(line, reset) {
			t.Errorf("line %d ends in a full reset: %q", i, line)
		}
	}
}

func TestWrapTextCarriesHyperlinksAcrossLines(t *testing.T) {
	url := "https://example.com"
	openST, closeST := "\x1b]8;;"+url+"\x1b\\", "\x1b]8;;\x1b\\"
	lines := WrapText(openST+"0123456789"+closeST, 6)
	strip := regexp.MustCompile("\x1b\\]8;;[^\x1b\x07]*\x1b\\\\|\x1b\\[[0-9;]*m")
	for i, line := range lines {
		if strings.TrimSpace(strip.ReplaceAllString(line, "")) != "" && !strings.Contains(line, openST) {
			t.Errorf("line %q has link text but does not reopen the link", line)
		}
		if i < len(lines)-1 && strings.Contains(line, openST) && !strings.HasSuffix(line, closeST) {
			t.Errorf("line %q breaks inside the link without closing it", line)
		}
	}

	long := "https://example.com/oauth/" + strings.Repeat("a", 32)
	openBEL := "\x1b]8;;" + long + "\x07"
	lines = WrapText(openBEL+long+"\x1b]8;;\x07", 20)
	if len(lines) < 2 {
		t.Fatalf("the link did not wrap: %q", lines)
	}
	for i, line := range lines {
		if !strings.Contains(line, openBEL) || strings.Contains(line, "\x1b]8;;"+long+"\x1b\\") {
			t.Errorf("line %q does not reopen the link with BEL", line)
		}
		if i < len(lines)-1 && !strings.HasSuffix(line, "\x1b]8;;\x07") {
			t.Errorf("line %q does not close the link with BEL", line)
		}
	}

	lines = WrapText("before "+openST+"link"+closeST+" after", 80)
	if len(lines) != 1 || strings.Count(lines[0], openST) != 1 || strings.Count(lines[0], closeST) != 1 {
		t.Errorf("a fitting line gained link sequences: %q", lines)
	}
}
