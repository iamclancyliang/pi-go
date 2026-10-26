package interactive

import (
	"encoding/json"
	"os"
	"testing"
)

// TestThemeMatchesPi checks every colour of the dark theme, in both colour
// modes, and chalk's modifiers against output from Pi's own theme code
// (testdata/theme/generate.ts).
func TestThemeMatchesPi(t *testing.T) {
	data, err := os.ReadFile("testdata/theme/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Modes map[string]struct {
			Fg map[string]string `json:"fg"`
			Bg map[string]string `json:"bg"`
		} `json:"modes"`
		Modifiers map[string]map[string]string `json:"modifiers"`
	}
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	for mode, want := range golden.Modes {
		theme := DarkTheme(mode == "truecolor")
		for name, code := range want.Fg {
			if got := theme.Fg(name, ""); got != code+"\x1b[39m" {
				t.Errorf("%s fg %s = %q, want %q", mode, name, got, code)
			}
		}
		for name, code := range want.Bg {
			if got := theme.Bg(name, ""); got != code+"\x1b[49m" {
				t.Errorf("%s bg %s = %q, want %q", mode, name, got, code)
			}
		}
	}
	modifiers := map[string]func(string) string{
		"bold": Bold, "dim": Dim, "italic": Italic, "underline": Underline, "inverse": Inverse, "strikethrough": Strikethrough,
	}
	for name, cases := range golden.Modifiers {
		for in, want := range cases {
			if got := modifiers[name](in); got != want {
				t.Errorf("%s(%q) = %q, want %q", name, in, got, want)
			}
		}
	}
}

func TestThinkingBorderColours(t *testing.T) {
	theme := DarkTheme(true)
	for level, color := range map[string]string{
		"off": "thinkingOff", "minimal": "thinkingMinimal", "xhigh": "thinkingXhigh", "max": "thinkingMax", "bogus": "thinkingOff",
	} {
		if got, want := theme.ThinkingBorder(level)("x"), theme.Fg(color, "x"); got != want {
			t.Errorf("%s: %q, want %q", level, got, want)
		}
	}
}
