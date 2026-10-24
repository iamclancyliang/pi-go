package tui_test

import (
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/tui"
)

// TestDecodingTheKeysTheBindingsName: every sequence a terminal sends for a
// bound key must come out under the name the binding table uses, or the key
// silently does nothing.
func TestDecodingTheKeysTheBindingsName(t *testing.T) {
	cases := map[string]string{
		"\r":         "enter",
		"\n":         "ctrl+j",
		"\t":         "tab",
		"\x7f":       "backspace",
		"\x01":       "ctrl+a",
		"\x05":       "ctrl+e",
		"\x17":       "ctrl+w",
		"\x0b":       "ctrl+k",
		"\x15":       "ctrl+u",
		"\x19":       "ctrl+y",
		"\x04":       "ctrl+d",
		"\x1f":       "ctrl+-",
		"\x1b[A":     "up",
		"\x1b[B":     "down",
		"\x1b[C":     "right",
		"\x1b[D":     "left",
		"\x1b[H":     "home",
		"\x1b[F":     "end",
		"\x1b[3~":    "delete",
		"\x1b[5~":    "pageUp",
		"\x1b[6~":    "pageDown",
		"\x1b[1;5C":  "ctrl+right",
		"\x1b[1;5D":  "ctrl+left",
		"\x1b[1;3C":  "alt+right",
		"\x1b[1;3D":  "alt+left",
		"\x1bb":      "alt+b",
		"\x1bf":      "alt+f",
		"\x1bd":      "alt+d",
		"\x1by":      "alt+y",
		"\x1b\x7f":   "alt+backspace",
		"\x1b[13;2u": "shift+enter",
	}
	for raw, want := range cases {
		keys, rest := tui.Decode([]byte(raw))
		if len(rest) != 0 {
			t.Errorf("%q left %q undecoded", raw, rest)
			continue
		}
		if len(keys) != 1 || keys[0].Name != want {
			t.Errorf("%q decoded to %+v, want %q", raw, keys, want)
		}
	}
}

// TestTextComesThroughAsText, multi-byte included.
func TestTextComesThroughAsText(t *testing.T) {
	keys, rest := tui.Decode([]byte("hé 世"))
	if len(rest) != 0 {
		t.Fatalf("left %q undecoded", rest)
	}
	var got []rune
	for _, k := range keys {
		if !k.IsText() {
			t.Fatalf("%+v is not text", k)
		}
		got = append(got, k.Text)
	}
	if string(got) != "hé 世" {
		t.Fatalf("decoded %q", string(got))
	}
}

// TestAReadEndingMidSequenceWaitsForTheRest. Deciding too early turns the
// remainder of an arrow key into typed letters.
func TestAReadEndingMidSequenceWaitsForTheRest(t *testing.T) {
	keys, rest := tui.Decode([]byte("\x1b["))
	if len(keys) != 0 {
		t.Fatalf("an incomplete sequence decoded to %+v", keys)
	}
	if string(rest) != "\x1b[" {
		t.Fatalf("the remainder %q was not returned", rest)
	}
	// The rest arrives; together they are one arrow.
	keys, rest = tui.Decode(append(rest, 'A'))
	if len(rest) != 0 || len(keys) != 1 || keys[0].Name != "up" {
		t.Fatalf("reassembly decoded %+v, rest %q", keys, rest)
	}
}

// TestASplitRuneWaitsForItsTail too.
func TestASplitRuneWaitsForItsTail(t *testing.T) {
	whole := []byte("é")
	keys, rest := tui.Decode(whole[:1])
	if len(keys) != 0 || len(rest) != 1 {
		t.Fatalf("half a rune decoded to %+v, rest %q", keys, rest)
	}
	keys, rest = tui.Decode(append(rest, whole[1:]...))
	if len(rest) != 0 || len(keys) != 1 || keys[0].Text != 'é' {
		t.Fatalf("reassembly decoded %+v", keys)
	}
}

// TestAPasteArrivesAsOneKeyWithItsText. With bracketed paste on, the terminal
// wraps what was pasted in markers; decoding it as keystrokes would run every
// newline in it as enter and every control character as a command.
func TestAPasteArrivesAsOneKeyWithItsText(t *testing.T) {
	keys, rest := tui.Decode([]byte("a\x1b[200~line one\r\nline\ttwo\x1b[201~b"))
	if len(rest) != 0 || len(keys) != 3 {
		t.Fatalf("decoded %+v, rest %q", keys, rest)
	}
	if keys[1].Name != "paste" || keys[1].Paste != "line one\r\nline\ttwo" {
		t.Fatalf("the paste decoded as %+v", keys[1])
	}
	if keys[0].Text != 'a' || keys[2].Text != 'b' {
		t.Fatalf("the keys around the paste were lost: %+v", keys)
	}

	// A paste still arriving waits for its end marker.
	keys, rest = tui.Decode([]byte("\x1b[200~half of it"))
	if len(keys) != 0 || string(rest) != "\x1b[200~half of it" {
		t.Fatalf("an unfinished paste decoded as %+v, rest %q", keys, rest)
	}
}

// TestDecodingFollowsPisParseKey, for the forms beyond the legacy ones: the
// Kitty keyboard protocol, xterm's modifyOtherKeys, rxvt and the Linux
// console. Names are Pi's, modifiers in its order.
func TestDecodingFollowsPisParseKey(t *testing.T) {
	cases := map[string]string{
		"\x1b[Z":            "shift+tab",
		"\x1b[112;6u":       "shift+ctrl+p", // Kitty: p with shift+ctrl
		"\x1b[99;5u":        "ctrl+c",
		"\x1b[13;3u":        "alt+enter",
		"\x1b[27u":          "escape",
		"\x1b[9;2u":         "shift+tab",
		"\x1b[57414u":       "enter", // keypad enter
		"\x1b[1;6A":         "shift+ctrl+up",
		"\x1b[3;5~":         "ctrl+delete",
		"\x1b[5;2~":         "shift+pageUp",
		"\x1b[27;5;106~":    "ctrl+j", // modifyOtherKeys
		"\x1b[27;6;80~":     "shift+ctrl+p",
		"\x1b[2~":           "insert",
		"\x1b[15~":          "f5",
		"\x1bOP":            "f1",
		"\x1b[[A":           "f1",
		"\x1b[[5~":          "pageUp",
		"\x1b[a":            "shift+up",
		"\x1b[7$":           "shift+home",
		"\x1b[8^":           "ctrl+end",
		"\x1bOd":            "ctrl+left",
		"\x1b\x01":          "ctrl+alt+a",
		"\x1b\x1d":          "ctrl+alt+]",
		"\x1b\r":            "alt+enter",
		"\x00":              "ctrl+space",
		"\x1c":              "ctrl+\\",
		"\x1d":              "ctrl+]",
		"\x1b[105;9u":       "super+i",
		"\x1b[1074::100;5u": "ctrl+d", // a Cyrillic layout: the base key names it
	}
	for raw, want := range cases {
		keys, rest := tui.Decode([]byte(raw))
		if len(rest) != 0 || len(keys) != 1 || keys[0].Name != want {
			t.Errorf("%q decoded to %+v (rest %q), want %q", raw, keys, rest, want)
		}
	}
}

// TestKittyTextKeysTypeTheirCharacter, the shifted one when shift is held.
func TestKittyTextKeysTypeTheirCharacter(t *testing.T) {
	for raw, want := range map[string]rune{"\x1b[97u": 'a', "\x1b[97:65;2u": 'A', "\x1b[1080u": 'и', "\x1b[57401u": '2'} {
		keys, _ := tui.Decode([]byte(raw))
		if len(keys) != 1 || keys[0].Text != want || !keys[0].IsText() {
			t.Errorf("%q decoded to %+v, want text %q", raw, keys, want)
		}
	}
}

// TestAKeyReleaseIsMarkedSoItIsNotTakenForAPress. With the protocol's event
// types on, the terminal reports releases too.
func TestAKeyReleaseIsMarkedSoItIsNotTakenForAPress(t *testing.T) {
	keys, _ := tui.Decode([]byte("\x1b[97;1:3u\x1b[1;1:2A"))
	if len(keys) != 2 || !keys[0].Release || keys[0].Text != 'a' || !keys[1].Repeat || keys[1].Name != "up" {
		t.Fatalf("decoded %+v", keys)
	}
}

// TestProtocolRepliesAreReportsNotKeys.
func TestProtocolRepliesAreReportsNotKeys(t *testing.T) {
	keys, _ := tui.Decode([]byte("\x1b[?7u\x1b[?62;22c"))
	if len(keys) != 2 || keys[0].Report != "kitty:7" || keys[1].Report != "device-attributes" || keys[0].IsText() {
		t.Fatalf("decoded %+v", keys)
	}
}

// TestKittyModeRereadsTwoLegacySequences as shift+enter, as Pi does: Kitty
// sends ESC CR and Ghostty a bare line feed for it.
func TestKittyModeRereadsTwoLegacySequences(t *testing.T) {
	for _, raw := range []string{"\x1b\r", "\n"} {
		keys, _ := tui.DecodeKeys([]byte(raw), true)
		if len(keys) != 1 || keys[0].Name != "shift+enter" {
			t.Errorf("%q in Kitty mode decoded to %+v", raw, keys)
		}
	}
}

// TestOneLegacySequenceAnswersToEveryNamePiGivesIt.
func TestOneLegacySequenceAnswersToEveryNamePiGivesIt(t *testing.T) {
	cases := map[string][]string{"\x1bb": {"alt+b", "alt+left"}, "\n": {"ctrl+j", "enter"}, "\x1bp": {"alt+p", "alt+up"}}
	for raw, names := range cases {
		keys, _ := tui.Decode([]byte(raw))
		got := append([]string{keys[0].Name}, keys[0].Also...)
		if strings.Join(got, ",") != strings.Join(names, ",") {
			t.Errorf("%q answers to %v, want %v", raw, got, names)
		}
	}
}

// TestAnEscapedCapitalNamesNothingAndTypesNothing.
func TestAnEscapedCapitalNamesNothingAndTypesNothing(t *testing.T) {
	keys, rest := tui.Decode([]byte("\x1bQx"))
	if len(rest) != 0 || len(keys) != 1 || keys[0].Text != 'x' {
		t.Fatalf("decoded %+v", keys)
	}
}
