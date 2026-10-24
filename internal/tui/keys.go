// Package tui is the terminal the interactive mode runs in.
//
// Two layers. The input layer — key decoding, a line editor and the bindings
// between them — is what the line-based prompt uses today. The rendering
// core under it is Pi's regular-mode interface model (ADR-0011): components
// render themselves to lines for a width, and a renderer draws only the lines
// that changed into the terminal's main screen, leaving the conversation in
// its scrollback. What the renderer must do is recorded in
// docs/product/pi-feature-inventory.md §11.4–11.5.
//
// Decoding, editing, layout and rendering are pure over their inputs and
// outputs; only tty.go and terminal.go touch a real terminal. That split is
// what makes them testable — a frame is a screen a test terminal can be
// asked about, and a keystroke's effect is a function of the buffer.
package tui

import (
	"os"
	"strconv"
	"strings"
)

// pasteEnd closes a bracketed paste.
const pasteEnd = "\x1b[201~"

// Key is one decoded keystroke, named the way Pi names them: "enter",
// "ctrl+a", "alt+left", "shift+enter". Modifiers come in Pi's order —
// shift, ctrl, alt, super — though matching a binding ignores their order.
// Printable text arrives as Text instead.
type Key struct {
	// Name identifies a non-text key, or is empty for plain text.
	Name string

	// Also are other names the same bytes are known by. A legacy terminal
	// sends one byte for keys Pi treats as distinct — ESC b is alt+b and
	// alt+left, a line feed is ctrl+j and enter — and Pi's matcher accepts
	// any of them; a binding to either name answers to the key.
	Also []string

	// Text is the printable input, when the keystroke is one.
	Text rune

	// Paste is what was pasted, for the "paste" key: the terminal's
	// bracketed paste delivered whole, line breaks and all, so none of it is
	// taken for keystrokes.
	Paste string

	// Repeat and Release are the Kitty keyboard protocol's event types. A
	// release is not a keystroke, and is not delivered as one.
	Repeat, Release bool

	// Report is a terminal's answer to a query rather than a keystroke:
	// "kitty:<flags>" for the keyboard protocol's flags, "device-attributes"
	// for the sentinel that says the terminal does not speak it.
	Report string
}

// IsText reports a keystroke that types something rather than commands
// something.
func (k Key) IsText() bool { return k.Name == "" && k.Text != 0 && k.Report == "" }

// Decode turns raw terminal bytes into keystrokes, for a terminal not using
// the Kitty keyboard protocol.
func Decode(raw []byte) (keys []Key, rest []byte) { return DecodeKeys(raw, false) }

// DecodeKeys turns raw terminal bytes into keystrokes.
//
// It consumes what it recognises and returns the rest, because a read can end
// mid-sequence: an ESC that arrived alone may be the start of an arrow key
// whose remainder is in the next read, and deciding too early turns arrows
// into stray letters.
//
// kitty says the terminal has the Kitty keyboard protocol on, which changes
// what two legacy byte sequences mean, as it does in Pi: ESC CR and a bare line
// feed are what Kitty and Ghostty send for shift+enter.
func DecodeKeys(raw []byte, kitty bool) (keys []Key, rest []byte) {
	for len(raw) > 0 {
		key, consumed, complete := decodeOne(raw, kitty)
		if !complete {
			return keys, raw
		}
		if key.Name == "" && key.Text == 0 && key.Report == "" {
			// Unrecognised: dropped rather than passed through, because a
			// control byte typed into the buffer is invisible and corrupts the
			// line the user is looking at.
			raw = raw[max(1, consumed):]
			continue
		}
		keys = append(keys, key)
		raw = raw[consumed:]
	}
	return keys, nil
}

func named(name string, also ...string) Key { return Key{Name: name, Also: also} }

func decodeOne(raw []byte, kitty bool) (Key, int, bool) {
	switch b := raw[0]; {
	case b == 0x1b:
		return decodeEscape(raw, kitty)
	case b == '\r':
		return named("enter", "ctrl+m"), 1, true
	case b == '\n':
		if kitty {
			return named("shift+enter"), 1, true
		}
		// Pi's newline-in-input key; Pi's matcher also takes it for enter,
		// and checks newline first, so it inserts a line.
		return named("ctrl+j", "enter"), 1, true
	case b == '\t':
		return named("tab", "ctrl+i"), 1, true
	case b == 0x7f:
		return named("backspace"), 1, true
	case b == 0x08:
		// Windows Terminal sends a raw BS for ctrl+backspace; other legacy
		// terminals and tmux send it for backspace.
		if windowsTerminal() {
			return named("ctrl+backspace", "ctrl+h"), 1, true
		}
		return named("backspace", "ctrl+h"), 1, true
	case b == 0x00:
		return named("ctrl+space"), 1, true
	case b == 0x1c:
		return named("ctrl+\\"), 1, true
	case b == 0x1d:
		return named("ctrl+]"), 1, true
	case b == 0x1f:
		// Terminals send undo this way.
		return named("ctrl+-", "ctrl+_"), 1, true
	case b >= 1 && b <= 26:
		return named("ctrl+" + string(rune('a'+b-1))), 1, true
	case b < 0x20:
		return Key{}, 1, true
	}
	r, size := decodeRune(raw)
	if size == 0 {
		return Key{}, 0, len(raw) >= 4
	}
	return Key{Text: r}, size, true
}

func windowsTerminal() bool {
	return os.Getenv("WT_SESSION") != "" && os.Getenv("SSH_CONNECTION") == "" &&
		os.Getenv("SSH_CLIENT") == "" && os.Getenv("SSH_TTY") == ""
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

// ss3Keys are what an SS3 sequence (ESC O x) names.
var ss3Keys = map[byte]Key{
	'A': named("up"), 'B': named("down"), 'C': named("right"), 'D': named("left"),
	'H': named("home"), 'F': named("end"), 'E': named("clear"), 'M': named("enter"),
	'P': named("f1"), 'Q': named("f2"), 'R': named("f3"), 'S': named("f4"),
	'a': named("ctrl+up"), 'b': named("ctrl+down"), 'c': named("ctrl+right"), 'd': named("ctrl+left"),
	'e': named("ctrl+clear"),
}

// decodeEscape reads an ESC-introduced sequence, by Pi's parseKey.
func decodeEscape(raw []byte, kitty bool) (Key, int, bool) {
	if len(raw) == 1 {
		// Maybe a bare escape, maybe the start of a sequence still arriving.
		// The caller retries with more bytes; a truly bare ESC is resolved by
		// the reader timing out and flushing it.
		return Key{}, 0, false
	}
	switch raw[1] {
	case '[':
		return decodeCSI(raw, kitty)
	case 'O':
		if len(raw) < 3 {
			return Key{}, 0, false
		}
		return ss3Keys[raw[2]], 3, true
	case 0x1b:
		return named("ctrl+alt+["), 2, true
	case 0x1c:
		return named("ctrl+alt+\\"), 2, true
	case 0x1d:
		return named("ctrl+alt+]"), 2, true
	case 0x1f:
		return named("ctrl+alt+-"), 2, true
	case '\r':
		if kitty {
			return named("shift+enter"), 2, true
		}
		return named("alt+enter"), 2, true
	case ' ':
		if kitty {
			return Key{}, 2, true
		}
		return named("alt+space"), 2, true
	case 0x7f, 0x08:
		return named("alt+backspace"), 2, true
	case 'B', 'F':
		if kitty {
			return Key{}, 2, true
		}
		if raw[1] == 'B' {
			return named("alt+left"), 2, true
		}
		return named("alt+right"), 2, true
	}
	c := raw[1]
	if kitty {
		// With the protocol on, alt combinations arrive as CSI u; an ESC
		// pair is not one.
		return Key{}, 2, true
	}
	switch {
	case c >= 1 && c <= 26:
		return named("ctrl+alt+" + string(rune(c+96))), 2, true
	case c >= 'a' && c <= 'z', c >= '0' && c <= '9', isSymbolKey(rune(c)):
		key := named("alt+" + string(rune(c)))
		// Emacs-style word and line movement keys, which Pi also takes for
		// the arrows they stand for.
		switch c {
		case 'b':
			key.Also = []string{"alt+left"}
		case 'f':
			key.Also = []string{"alt+right"}
		case 'p':
			key.Also = []string{"alt+up"}
		case 'n':
			key.Also = []string{"alt+down"}
		}
		return key, 2, true
	}
	// ESC followed by any other ASCII character names no key Pi knows, and
	// the pair is dropped together; typing the character alone would put a
	// letter in the text the user did not type.
	if c >= 0x20 && c < 0x7f {
		return Key{}, 2, true
	}
	return Key{}, 1, true
}

const symbolKeys = "`-=[]\\;',./!@#$%^&*()_+|~{}:<>?"

func isSymbolKey(r rune) bool { return r < 0x80 && strings.ContainsRune(symbolKeys, r) }

// Modifier bits, as the Kitty protocol and xterm number them (minus one).
const (
	modShift = 1
	modAlt   = 2
	modCtrl  = 4
	modSuper = 8
	modLocks = 64 + 128 // caps and num lock, which name no key
)

// Codepoints for keys with no character, as Pi numbers them internally.
const (
	cpUp = -1 - iota
	cpDown
	cpRight
	cpLeft
)

const (
	cpDelete   = -10
	cpInsert   = -11
	cpPageUp   = -12
	cpPageDown = -13
	cpHome     = -14
	cpEnd      = -15
)

// keypadEquivalents maps the Kitty protocol's keypad codepoints onto the
// keys they type.
var keypadEquivalents = map[int]int{
	57399: '0', 57400: '1', 57401: '2', 57402: '3', 57403: '4', 57404: '5', 57405: '6',
	57406: '7', 57407: '8', 57408: '9', 57409: '.', 57410: '/', 57411: '*', 57412: '-',
	57413: '+', 57415: '=', 57416: ',', 57417: cpLeft, 57418: cpRight, 57419: cpUp,
	57420: cpDown, 57421: cpPageUp, 57422: cpPageDown, 57423: cpHome, 57424: cpEnd,
	57425: cpInsert, 57426: cpDelete,
}

// decodeCSI reads ESC [ sequences: arrows, home and end, the editing and
// function keys with modifiers, the Kitty protocol's CSI u, xterm's
// modifyOtherKeys, rxvt's shifted and controlled forms, bracketed paste and
// the keyboard protocol's replies.
func decodeCSI(raw []byte, kitty bool) (Key, int, bool) {
	if len(raw) < 3 {
		return Key{}, 0, false
	}
	if raw[2] == '[' {
		// Linux console: ESC [ [ A..E are f1..f5, and ESC [ [ 5~ / 6~ pages.
		if len(raw) < 4 {
			return Key{}, 0, false
		}
		switch raw[3] {
		case 'A', 'B', 'C', 'D', 'E':
			return named("f" + string(rune('1'+raw[3]-'A'))), 4, true
		case '5', '6':
			if len(raw) < 5 {
				return Key{}, 0, false
			}
			if raw[4] == '~' {
				if raw[3] == '5' {
					return named("pageUp"), 5, true
				}
				return named("pageDown"), 5, true
			}
		}
		return Key{}, 4, true
	}

	end := 2
	for {
		if end >= len(raw) {
			return Key{}, 0, false
		}
		c := raw[end]
		// rxvt ends its shifted forms in $, which is not a standard final.
		if (c >= 0x40 && c <= 0x7e) || c == '$' {
			break
		}
		end++
	}
	body, final, consumed := string(raw[2:end]), raw[end], end+1

	if strings.HasPrefix(body, "?") {
		switch final {
		case 'u':
			if flags, err := strconv.Atoi(body[1:]); err == nil {
				return Key{Report: "kitty:" + strconv.Itoa(flags)}, consumed, true
			}
		case 'c':
			return Key{Report: "device-attributes"}, consumed, true
		}
		return Key{}, consumed, true
	}

	switch final {
	case 'u':
		return decodeKitty(body), consumed, true
	case 'A', 'B', 'C', 'D', 'H', 'F':
		cp := map[byte]int{'A': cpUp, 'B': cpDown, 'C': cpRight, 'D': cpLeft, 'H': cpHome, 'F': cpEnd}[final]
		if body == "" {
			return keyFor(cp, 0, 0, event("")), consumed, true
		}
		params, ev, _ := strings.Cut(body, ":")
		if first, mod, ok := strings.Cut(params, ";"); ok && first == "1" {
			if m, err := strconv.Atoi(mod); err == nil {
				return keyFor(cp, m-1, 0, event(ev)), consumed, true
			}
		}
		return Key{}, consumed, true
	case 'E':
		return named("clear"), consumed, true
	case 'Z':
		return named("shift+tab"), consumed, true
	case 'a', 'b', 'c', 'd', 'e':
		return named("shift+" + map[byte]string{'a': "up", 'b': "down", 'c': "right", 'd': "left", 'e': "clear"}[final]), consumed, true
	case '$', '^':
		mod := "shift+"
		if final == '^' {
			mod = "ctrl+"
		}
		if name, ok := map[string]string{"2": "insert", "3": "delete", "5": "pageUp", "6": "pageDown", "7": "home", "8": "end"}[body]; ok {
			return named(mod + name), consumed, true
		}
		return Key{}, consumed, true
	case '~':
		if body == "200" {
			// Bracketed paste: everything up to the end marker is the paste.
			i := strings.Index(string(raw[consumed:]), pasteEnd)
			if i < 0 {
				return Key{}, 0, false
			}
			return Key{Name: "paste", Paste: string(raw[consumed : consumed+i])}, consumed + i + len(pasteEnd), true
		}
		parts := strings.Split(body, ";")
		if len(parts) == 3 && parts[0] == "27" {
			// xterm modifyOtherKeys: CSI 27 ; modifier ; keycode ~
			mod, err1 := strconv.Atoi(parts[1])
			code, err2 := strconv.Atoi(parts[2])
			if err1 == nil && err2 == nil {
				return decodeModified(code, mod-1), consumed, true
			}
			return Key{}, consumed, true
		}
		num, rest, _ := strings.Cut(body, ";")
		modPart, ev, _ := strings.Cut(rest, ":")
		mod := 0
		if modPart != "" {
			m, err := strconv.Atoi(modPart)
			if err != nil {
				return Key{}, consumed, true
			}
			mod = m - 1
		}
		if cp, ok := map[string]int{"1": cpHome, "7": cpHome, "4": cpEnd, "8": cpEnd, "2": cpInsert,
			"3": cpDelete, "5": cpPageUp, "6": cpPageDown}[num]; ok {
			return keyFor(cp, mod, 0, event(ev)), consumed, true
		}
		if f, ok := map[string]string{"11": "f1", "12": "f2", "13": "f3", "14": "f4", "15": "f5", "17": "f6",
			"18": "f7", "19": "f8", "20": "f9", "21": "f10", "23": "f11", "24": "f12"}[num]; ok && mod == 0 {
			return named(f), consumed, true
		}
		return Key{}, consumed, true
	}
	return Key{}, consumed, true
}

type eventType int

const (
	press eventType = iota
	repeat
	release
)

func event(s string) eventType {
	switch s {
	case "2":
		return repeat
	case "3":
		return release
	}
	return press
}

// decodeKitty reads the body of a CSI u sequence:
// <codepoint>[:<shifted>[:<base>]][;<modifiers>[:<event>]] — Pi's
// parseKittySequence and decodeKittyPrintable.
func decodeKitty(body string) Key {
	keyPart, modPart, _ := strings.Cut(body, ";")
	codes := strings.Split(keyPart, ":")
	codepoint, err := strconv.Atoi(codes[0])
	if err != nil {
		return Key{}
	}
	shifted, base := -1, 0
	if len(codes) > 1 && codes[1] != "" {
		if v, err := strconv.Atoi(codes[1]); err == nil {
			shifted = v
		}
	}
	if len(codes) > 2 && codes[2] != "" {
		if v, err := strconv.Atoi(codes[2]); err == nil {
			base = v
		}
	}
	mod := 0
	modText, ev, _ := strings.Cut(modPart, ":")
	if modText != "" {
		m, err := strconv.Atoi(modText)
		if err != nil {
			return Key{}
		}
		mod = m - 1
	}
	e := event(ev)

	// Plain or shifted text keys type their character; ctrl, alt and the
	// rest are keys a binding matches.
	if mod&^(modShift|modLocks) == 0 {
		cp := codepoint
		if mod&modShift != 0 && shifted >= 0 {
			cp = shifted
		}
		if v, ok := keypadEquivalents[cp]; ok {
			cp = v
		}
		if cp >= 32 && cp != 127 && (cp < 57344 || cp > 63743) {
			return Key{Text: rune(cp), Repeat: e == repeat, Release: e == release}
		}
	}
	return keyFor(codepoint, mod, base, e)
}

// decodeModified reads a modifyOtherKeys key: shifted or plain characters
// type themselves, anything else is named.
func decodeModified(code, mod int) Key {
	if mod&^modLocks&^modShift == 0 && code >= 32 && code != 127 {
		return Key{Text: rune(code)}
	}
	return keyFor(code, mod, 0, press)
}

// keyFor names a key from its codepoint and modifiers — Pi's
// formatParsedKey. A modifier bit Pi does not name leaves the key unnamed.
func keyFor(codepoint, mod, base int, e eventType) Key {
	if v, ok := keypadEquivalents[codepoint]; ok {
		codepoint = v
	}
	effective := mod &^ modLocks
	if effective&modShift != 0 && codepoint >= 'A' && codepoint <= 'Z' {
		codepoint += 32
	}
	// The base-layout key names the physical key only when the codepoint is
	// not itself a Latin letter, digit or known symbol: a remapped layout
	// must not rename a key the user typed.
	if !(codepoint >= 'a' && codepoint <= 'z') && !(codepoint >= '0' && codepoint <= '9') &&
		!(codepoint > 0 && isSymbolKey(rune(codepoint))) && base != 0 {
		codepoint = base
	}
	var name string
	switch {
	case codepoint == 27:
		name = "escape"
	case codepoint == 9:
		name = "tab"
	case codepoint == 13 || codepoint == 57414:
		name = "enter"
	case codepoint == 32:
		name = "space"
	case codepoint == 127:
		name = "backspace"
	case codepoint == cpDelete:
		name = "delete"
	case codepoint == cpInsert:
		name = "insert"
	case codepoint == cpHome:
		name = "home"
	case codepoint == cpEnd:
		name = "end"
	case codepoint == cpPageUp:
		name = "pageUp"
	case codepoint == cpPageDown:
		name = "pageDown"
	case codepoint == cpUp:
		name = "up"
	case codepoint == cpDown:
		name = "down"
	case codepoint == cpLeft:
		name = "left"
	case codepoint == cpRight:
		name = "right"
	case codepoint >= '0' && codepoint <= '9', codepoint >= 'a' && codepoint <= 'z',
		codepoint > 0 && isSymbolKey(rune(codepoint)):
		name = string(rune(codepoint))
	default:
		return Key{}
	}
	if effective&^(modShift|modCtrl|modAlt|modSuper) != 0 {
		return Key{}
	}
	var mods []string
	if effective&modShift != 0 {
		mods = append(mods, "shift")
	}
	if effective&modCtrl != 0 {
		mods = append(mods, "ctrl")
	}
	if effective&modAlt != 0 {
		mods = append(mods, "alt")
	}
	if effective&modSuper != 0 {
		mods = append(mods, "super")
	}
	if len(mods) > 0 {
		name = strings.Join(mods, "+") + "+" + name
	}
	return Key{Name: name, Repeat: e == repeat, Release: e == release}
}
