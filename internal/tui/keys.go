// Package tui is the terminal the interactive mode runs in.
//
// This is the INPUT slice: raw mode, key decoding, a line editor and the
// bindings between them. It is not Pi's full-screen interface — there is no
// alternate screen, no chat rendering, no selector lists yet. The seam between
// the two is the editor: what Pi's interface does with a keystroke is decided
// by the same kind of binding table this one reads.
//
// Decoding, editing and layout are pure; only terminal.go touches the
// terminal. That split is what makes an editor testable — a keystroke's effect
// is a function of the buffer, not of the device it came from.
package tui

import "strings"

// Key is one decoded keystroke, named the way Pi names them: "enter", "ctrl+a",
// "alt+left", "shift+enter". Printable text arrives as Text instead.
type Key struct {
	// Name identifies a non-text key, or is empty for plain text.
	Name string

	// Text is the printable input, when the keystroke is one.
	Text rune
}

// IsText reports a keystroke that types something rather than commands
// something.
func (k Key) IsText() bool { return k.Name == "" && k.Text != 0 }

// Decode turns raw terminal bytes into keystrokes.
//
// It consumes what it recognises and returns the rest, because a read can end
// mid-sequence: an ESC that arrived alone may be the start of an arrow key
// whose remainder is in the next read, and deciding too early turns arrows
// into stray letters.
func Decode(raw []byte) (keys []Key, rest []byte) {
	for len(raw) > 0 {
		key, consumed, complete := decodeOne(raw)
		if !complete {
			return keys, raw
		}
		if consumed == 0 {
			// Unrecognised byte: dropped rather than passed through, because a
			// control byte typed into the buffer is invisible and corrupts the
			// line the user is looking at.
			raw = raw[1:]
			continue
		}
		keys = append(keys, key)
		raw = raw[consumed:]
	}
	return keys, nil
}

func decodeOne(raw []byte) (Key, int, bool) {
	switch raw[0] {
	case 0x1b:
		return decodeEscape(raw)
	case '\r':
		return Key{Name: "enter"}, 1, true
	case '\n':
		// Ctrl+J: Pi's newline-in-input key. Distinct from enter, which
		// submits.
		return Key{Name: "ctrl+j"}, 1, true
	case '\t':
		return Key{Name: "tab"}, 1, true
	case 0x7f, 0x08:
		return Key{Name: "backspace"}, 1, true
	}
	if raw[0] < 0x20 {
		// Ctrl+letter. Ctrl+_ (0x1f) is Pi's "ctrl+-": terminals send undo
		// that way.
		if raw[0] == 0x1f {
			return Key{Name: "ctrl+-"}, 1, true
		}
		if raw[0] == 0x1d {
			return Key{Name: "ctrl+]"}, 1, true
		}
		return Key{Name: "ctrl+" + string('a'+rune(raw[0])-1)}, 1, true
	}
	// UTF-8 text, one rune at a time.
	r, size := decodeRune(raw)
	if size == 0 {
		return Key{}, 0, len(raw) >= 4
	}
	return Key{Text: r}, size, true
}

func decodeRune(raw []byte) (rune, int) {
	if raw[0] < 0x80 {
		return rune(raw[0]), 1
	}
	// Multi-byte: length from the leading byte; incomplete input waits.
	var size int
	switch {
	case raw[0]&0xE0 == 0xC0:
		size = 2
	case raw[0]&0xF0 == 0xE0:
		size = 3
	case raw[0]&0xF8 == 0xF0:
		size = 4
	default:
		return 0, 0
	}
	if len(raw) < size {
		return 0, 0
	}
	r := rune(raw[0] & (0xFF >> (size + 1)))
	for _, b := range raw[1:size] {
		if b&0xC0 != 0x80 {
			return 0, 0
		}
		r = r<<6 | rune(b&0x3F)
	}
	return r, size
}

// decodeEscape reads an ESC-introduced sequence.
func decodeEscape(raw []byte) (Key, int, bool) {
	if len(raw) == 1 {
		// Maybe a bare escape, maybe the start of a sequence still arriving.
		// The caller retries with more bytes; a truly bare ESC is resolved by
		// the reader timing out and flushing it.
		return Key{}, 0, false
	}
	switch raw[1] {
	case '[':
		return decodeCSI(raw)
	case 'O':
		// SS3: how some terminals send home/end and F-keys.
		if len(raw) < 3 {
			return Key{}, 0, false
		}
		switch raw[2] {
		case 'H':
			return Key{Name: "home"}, 3, true
		case 'F':
			return Key{Name: "end"}, 3, true
		}
		return Key{}, 3, true
	case 0x7f, 0x08:
		return Key{Name: "alt+backspace"}, 2, true
	}
	// Alt+key: ESC prefixing an ordinary byte.
	inner, consumed, complete := decodeOne(raw[1:])
	if !complete {
		return Key{}, 0, false
	}
	if inner.IsText() {
		return Key{Name: "alt+" + string(inner.Text)}, 1 + consumed, true
	}
	if inner.Name != "" {
		return Key{Name: "alt+" + inner.Name}, 1 + consumed, true
	}
	return Key{}, 1 + consumed, true
}

// decodeCSI reads ESC [ ... sequences: arrows, home/end, delete, page keys,
// with modifiers.
func decodeCSI(raw []byte) (Key, int, bool) {
	// Find the final byte (0x40–0x7E).
	end := 2
	for {
		if end >= len(raw) {
			return Key{}, 0, false
		}
		if raw[end] >= 0x40 && raw[end] <= 0x7E {
			break
		}
		end++
	}
	body := string(raw[2:end])
	final := raw[end]
	consumed := end + 1

	base := ""
	switch final {
	case 'A':
		base = "up"
	case 'B':
		base = "down"
	case 'C':
		base = "right"
	case 'D':
		base = "left"
	case 'H':
		base = "home"
	case 'F':
		base = "end"
	case '~':
		switch strings.SplitN(body, ";", 2)[0] {
		case "1", "7":
			base = "home"
		case "3":
			base = "delete"
		case "4", "8":
			base = "end"
		case "5":
			base = "pageUp"
		case "6":
			base = "pageDown"
		default:
			return Key{}, consumed, true
		}
	case 'u':
		// Kitty-style: "13;2u" is shift+enter, which classic terminals cannot
		// express — the reason Pi documents ctrl+j as the fallback.
		if strings.HasPrefix(body, "13;") {
			if modifierName(strings.TrimPrefix(body, "13;")) == "shift" {
				return Key{Name: "shift+enter"}, consumed, true
			}
			return Key{Name: "enter"}, consumed, true
		}
		return Key{}, consumed, true
	default:
		return Key{}, consumed, true
	}

	// A modifier arrives as ";N" after a parameter: "1;5C" is ctrl+right.
	if parts := strings.SplitN(body, ";", 2); len(parts) == 2 {
		if name := modifierName(parts[1]); name != "" {
			return Key{Name: name + "+" + base}, consumed, true
		}
	}
	return Key{Name: base}, consumed, true
}

func modifierName(code string) string {
	switch code {
	case "2":
		return "shift"
	case "3":
		return "alt"
	case "5":
		return "ctrl"
	case "7":
		return "ctrl+alt"
	default:
		return ""
	}
}
