package tui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// goldenCase is one rendering Pi produced, from testdata/markdown/generate.ts.
type goldenCase struct {
	Name       string   `json:"name"`
	Text       string   `json:"text"`
	Width      int      `json:"width"`
	PaddingX   int      `json:"paddingX"`
	PaddingY   int      `json:"paddingY"`
	Style      string   `json:"style"`
	Hyperlinks bool     `json:"hyperlinks"`
	Lines      []string `json:"lines"`
}

func fg256(n int) Style {
	code := "\x1b[38;5;" + itoa3(n) + "m"
	return func(s string) string { return code + s + "\x1b[39m" }
}

func itoa3(n int) string {
	if n >= 10 {
		return string(rune('0'+n/10)) + string(rune('0'+n%10))
	}
	return string(rune('0' + n))
}

// goldenTheme is the theme generate.ts renders with.
var goldenTheme = MarkdownTheme{
	Heading: fg256(1), Link: fg256(2), LinkURL: fg256(3), Code: fg256(4), CodeBlock: fg256(5),
	CodeBlockBorder: fg256(6), Quote: fg256(7), QuoteBorder: fg256(8), HR: fg256(9), ListBullet: fg256(10),
	Bold:          func(s string) string { return "\x1b[1m" + s + "\x1b[22m" },
	Italic:        func(s string) string { return "\x1b[3m" + s + "\x1b[23m" },
	Strikethrough: func(s string) string { return "\x1b[9m" + s + "\x1b[29m" },
	Underline:     func(s string) string { return "\x1b[4m" + s + "\x1b[24m" },
}

var goldenStyles = map[string]*DefaultTextStyle{
	"none":     nil,
	"thinking": {Color: fg256(11), Italic: true},
	"user":     {Color: fg256(12), BgColor: func(s string) string { return "\x1b[48;5;13m" + s + "\x1b[49m" }},
}

// markdownDivergences are cases where goldmark reads the Markdown
// differently from marked and the difference is accepted, with why.
var markdownDivergences = map[string]string{}

func TestMarkdownMatchesPi(t *testing.T) {
	data, err := os.ReadFile("testdata/markdown/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []goldenCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	defer SetCapabilities(TerminalCapabilities{})
	failed := map[string]bool{}
	for _, c := range cases {
		if _, ok := markdownDivergences[c.Name]; ok {
			continue
		}
		SetCapabilities(TerminalCapabilities{Hyperlinks: c.Hyperlinks})
		md := NewMarkdown(c.Text, c.PaddingX, c.PaddingY, goldenTheme, goldenStyles[c.Style], MarkdownOptions{})
		got := md.Render(c.Width)
		if strings.Join(got, "\n") != strings.Join(c.Lines, "\n") {
			key := c.Name
			if failed[key] {
				continue
			}
			failed[key] = true
			t.Errorf("%s (width %d, style %s, hyperlinks %v) %q\n got:\n%s\nwant:\n%s", c.Name, c.Width, c.Style, c.Hyperlinks,
				c.Text, quoteLines(got), quoteLines(c.Lines))
		}
	}
	if len(failed) > 0 {
		t.Logf("%d differing inputs", len(failed))
	}
}

func quoteLines(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("  ")
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(l, "\x1b", "␛"), " ", "·"))
		b.WriteString("\n")
	}
	return b.String()
}
