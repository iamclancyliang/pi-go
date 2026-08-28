package tui_test

import (
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
