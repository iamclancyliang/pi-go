// Package interactive is the coding agent's full-screen interactive mode —
// Pi's modes/interactive: the theme, the components that show a
// conversation, and the mode that drives them. The terminal machinery they
// stand on — rendering, keys, the editor — is internal/tui.
package interactive

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/tui"
)

// Theme colours the interface. Until themes can be chosen it is always
// Pi's built-in dark theme.
type Theme struct {
	fg map[string]string
	bg map[string]string
}

// darkVars and darkColors are Pi's dark.json at the pin.
var darkVars = map[string]string{
	"cyan": "#00d7ff", "blue": "#5f87ff", "green": "#b5bd68", "red": "#cc6666", "yellow": "#ffff00",
	"text": "#d4d4d4", "gray": "#808080", "dimGray": "#666666", "darkGray": "#505050", "accent": "#8abeb7",
	"selectedBg": "#3a3a4a", "userMsgBg": "#343541", "toolPendingBg": "#282832", "toolSuccessBg": "#283228",
	"toolErrorBg": "#3c2828", "customMsgBg": "#2d2838",
}

var darkColors = map[string]string{
	"accent": "accent", "border": "blue", "borderAccent": "cyan", "borderMuted": "darkGray",
	"success": "green", "error": "red", "warning": "yellow", "muted": "gray", "dim": "dimGray",
	"text": "text", "thinkingText": "gray",
	"selectedBg": "selectedBg", "userMessageBg": "userMsgBg", "userMessageText": "text",
	"customMessageBg": "customMsgBg", "customMessageText": "text", "customMessageLabel": "#9575cd",
	"toolPendingBg": "toolPendingBg", "toolSuccessBg": "toolSuccessBg", "toolErrorBg": "toolErrorBg",
	"toolTitle": "text", "toolOutput": "gray",
	"mdHeading": "#f0c674", "mdLink": "#81a2be", "mdLinkUrl": "dimGray", "mdCode": "accent",
	"mdCodeBlock": "green", "mdCodeBlockBorder": "gray", "mdQuote": "gray", "mdQuoteBorder": "gray",
	"mdHr": "gray", "mdListBullet": "accent",
	"toolDiffAdded": "green", "toolDiffRemoved": "red", "toolDiffContext": "gray",
	"syntaxComment": "#6A9955", "syntaxKeyword": "#569CD6", "syntaxFunction": "#DCDCAA",
	"syntaxVariable": "#9CDCFE", "syntaxString": "#CE9178", "syntaxNumber": "#B5CEA8",
	"syntaxType": "#4EC9B0", "syntaxOperator": "#D4D4D4", "syntaxPunctuation": "#D4D4D4",
	"thinkingOff": "darkGray", "thinkingMinimal": "#6e6e6e", "thinkingLow": "#5f87af",
	"thinkingMedium": "#81a2be", "thinkingHigh": "#b294bb", "thinkingXhigh": "#d183e8",
	"thinkingMax": "#ff5fff", "bashMode": "green",
	// Colours a theme may leave out fall back as Pi's do.
	"scrollbarThumb": "selectedBg", "searchMatchBg": "selectedBg", "searchMatchText": "text",
}

var backgroundKeys = map[string]bool{
	"selectedBg": true, "scrollbarThumb": true, "searchMatchBg": true, "userMessageBg": true,
	"customMessageBg": true, "toolPendingBg": true, "toolSuccessBg": true, "toolErrorBg": true,
}

// DarkTheme is Pi's dark theme, in true colour where the terminal has it
// and the nearest of 256 colours where it does not.
func DarkTheme(trueColor bool) *Theme {
	t := &Theme{fg: map[string]string{}, bg: map[string]string{}}
	for name, value := range darkColors {
		hex := value
		for !strings.HasPrefix(hex, "#") {
			hex = darkVars[hex]
		}
		if backgroundKeys[name] {
			t.bg[name] = colorCode(hex, trueColor, 48)
		} else {
			t.fg[name] = colorCode(hex, trueColor, 38)
		}
	}
	return t
}

func colorCode(hex string, trueColor bool, layer int) string {
	r, g, b := hexRGB(hex)
	if trueColor {
		return fmt.Sprintf("\x1b[%d;2;%d;%d;%dm", layer, r, g, b)
	}
	return fmt.Sprintf("\x1b[%d;5;%dm", layer, rgbTo256(r, g, b))
}

func hexRGB(hex string) (int, int, int) {
	v, _ := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff)
}

var cubeValues = []int{0, 95, 135, 175, 215, 255}

func closestIndex(values []int, v int) int {
	best, bestDist := 0, 1<<30
	for i, c := range values {
		if d := abs(v - c); d < bestDist {
			best, bestDist = i, d
		}
	}
	return best
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func colorDistance(r1, g1, b1, r2, g2, b2 int) float64 {
	dr, dg, db := float64(r1-r2), float64(g1-g2), float64(b1-b2)
	return dr*dr*0.299 + dg*dg*0.587 + db*db*0.114
}

// rgbTo256 is Pi's choice of the nearest 256-colour index: the colour cube,
// unless the colour is nearly grey and a grey is closer.
func rgbTo256(r, g, b int) int {
	ri, gi, bi := closestIndex(cubeValues, r), closestIndex(cubeValues, g), closestIndex(cubeValues, b)
	cubeDist := colorDistance(r, g, b, cubeValues[ri], cubeValues[gi], cubeValues[bi])

	grays := make([]int, 24)
	for i := range grays {
		grays[i] = 8 + i*10
	}
	gray := int(0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b) + 0.5)
	gi2 := closestIndex(grays, gray)
	grayDist := colorDistance(r, g, b, grays[gi2], grays[gi2], grays[gi2])

	spread := max(r, g, b) - min(r, g, b)
	if spread < 10 && grayDist < cubeDist {
		return 232 + gi2
	}
	return 16 + 36*ri + 6*gi + bi
}

// Fg colours text with a theme colour.
func (t *Theme) Fg(color, text string) string {
	code, ok := t.fg[color]
	if !ok {
		panic("interactive: unknown theme colour " + color)
	}
	return code + text + "\x1b[39m"
}

// Bg gives text a theme background.
func (t *Theme) Bg(color, text string) string {
	code, ok := t.bg[color]
	if !ok {
		panic("interactive: unknown theme background " + color)
	}
	return code + text + "\x1b[49m"
}

// FgStyle and BgStyle are Fg and Bg as styles for components.
func (t *Theme) FgStyle(color string) tui.Style {
	return func(s string) string { return t.Fg(color, s) }
}

func (t *Theme) BgStyle(color string) tui.Style {
	return func(s string) string { return t.Bg(color, s) }
}

// chalkStyle applies a text attribute the way chalk does, which is what Pi
// styles with: a close code already inside the text is followed by the
// open code again, so the attribute survives a nested style ending, and
// each line is closed and reopened around its line break.
func chalkStyle(open, close string) func(string) string {
	return func(s string) string {
		if s == "" {
			return ""
		}
		if strings.Contains(s, "\x1b") {
			s = strings.ReplaceAll(s, close, close+open)
		}
		if strings.Contains(s, "\n") {
			var b strings.Builder
			lines := strings.Split(s, "\n")
			for i, l := range lines {
				if i < len(lines)-1 {
					if cr := strings.HasSuffix(l, "\r"); cr {
						b.WriteString(strings.TrimSuffix(l, "\r") + close + "\r\n" + open)
					} else {
						b.WriteString(l + close + "\n" + open)
					}
				} else {
					b.WriteString(l)
				}
			}
			s = b.String()
		}
		return open + s + close
	}
}

var (
	Bold          = chalkStyle("\x1b[1m", "\x1b[22m")
	Dim           = chalkStyle("\x1b[2m", "\x1b[22m")
	Italic        = chalkStyle("\x1b[3m", "\x1b[23m")
	Underline     = chalkStyle("\x1b[4m", "\x1b[24m")
	Inverse       = chalkStyle("\x1b[7m", "\x1b[27m")
	Strikethrough = chalkStyle("\x1b[9m", "\x1b[29m")
)

// ThinkingBorder is the editor's border colour for a thinking level.
func (t *Theme) ThinkingBorder(level string) tui.Style {
	switch level {
	case "minimal", "low", "medium", "high", "xhigh", "max":
		return t.FgStyle("thinking" + strings.ToUpper(level[:1]) + level[1:])
	}
	return t.FgStyle("thinkingOff")
}

// MarkdownTheme is how messages draw their Markdown.
func (t *Theme) MarkdownTheme() tui.MarkdownTheme {
	return tui.MarkdownTheme{
		Heading: t.FgStyle("mdHeading"), Link: t.FgStyle("mdLink"), LinkURL: t.FgStyle("mdLinkUrl"),
		Code: t.FgStyle("mdCode"), CodeBlock: t.FgStyle("mdCodeBlock"),
		CodeBlockBorder: t.FgStyle("mdCodeBlockBorder"), Quote: t.FgStyle("mdQuote"),
		QuoteBorder: t.FgStyle("mdQuoteBorder"), HR: t.FgStyle("mdHr"), ListBullet: t.FgStyle("mdListBullet"),
		Bold: Bold, Italic: Italic, Underline: Underline, Strikethrough: Strikethrough,
		HighlightCode: t.highlightCode,
	}
}

// SelectListTheme is how lists of choices are drawn.
func (t *Theme) SelectListTheme() tui.SelectListTheme {
	muted := t.FgStyle("muted")
	return tui.SelectListTheme{
		SelectedPrefix: t.FgStyle("accent"), SelectedText: t.FgStyle("accent"),
		Description: muted, ScrollInfo: muted, NoMatch: muted,
	}
}

// EditorTheme is how the prompt editor is drawn.
func (t *Theme) EditorTheme() tui.EditorTheme {
	return tui.EditorTheme{BorderColor: t.FgStyle("borderMuted"), SelectList: t.SelectListTheme()}
}

// highlightCode colours code for display. Pi highlights a language
// highlight.js knows and draws anything else in the code-block colour.
// Highlighting itself is not ported, so known languages are drawn as the
// plain text most of a highlighted block is, without the coloured keywords
// and strings.
func (t *Theme) highlightCode(code, lang string) []string {
	lines := strings.Split(code, "\n")
	if knownLanguage(lang) {
		return lines
	}
	for i, l := range lines {
		lines[i] = t.Fg("mdCodeBlock", l)
	}
	return lines
}
