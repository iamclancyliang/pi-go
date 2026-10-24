package tui

import (
	"strconv"
	"strings"
)

// esc is the byte every terminal control sequence starts with.
const esc = "\x1b"

// extractANSI reports the control sequence starting at s[i], if one does.
//
// The set is Pi's (extractAnsiCode, utils.ts): CSI sequences ending in one of
// m, G, K, H or J — styling and the cursor moves a component may embed — and
// OSC and APC strings ending in BEL or ST. OSC carries hyperlinks and
// semantic markers; APC carries the cursor marker. Anything else is left as
// text, which is what Pi does too: a component is not expected to embed
// other sequences, and guessing at their length would swallow visible text.
func extractANSI(s string, i int) (code string, ok bool) {
	if i >= len(s) || s[i] != 0x1b || i+1 >= len(s) {
		return "", false
	}
	switch s[i+1] {
	case '[':
		for j := i + 2; j < len(s); j++ {
			switch s[j] {
			case 'm', 'G', 'K', 'H', 'J':
				return s[i : j+1], true
			}
		}
		return "", false
	case ']', '_':
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return s[i : j+1], true
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return s[i : j+2], true
			}
		}
		return "", false
	}
	return "", false
}

// hyperlink is an open OSC 8 link, kept so a wrapped line can close it at
// its end and reopen it on the next.
//
// The terminator is kept as written: some terminals make a link clickable
// only when it ends in BEL, and reopening a BEL link with ST would leave every
// line after the first unclickable.
type hyperlink struct {
	params, url, terminator string
}

// parseHyperlink reads an OSC 8 sequence. It reports ok false for anything
// that is not one, and a nil link for the closing form, which has no URL.
func parseHyperlink(code string) (link *hyperlink, ok bool) {
	if !strings.HasPrefix(code, "\x1b]8;") {
		return nil, false
	}
	terminator := "\x1b\\"
	body := code[4:]
	if strings.HasSuffix(code, "\x07") {
		terminator = "\x07"
		body = body[:len(body)-1]
	} else if len(body) >= 2 {
		body = body[:len(body)-2]
	}
	sep := strings.IndexByte(body, ';')
	if sep < 0 {
		return nil, false
	}
	if body[sep+1:] == "" {
		return nil, true
	}
	return &hyperlink{params: body[:sep], url: body[sep+1:], terminator: terminator}, true
}

func (h hyperlink) open() string { return "\x1b]8;" + h.params + ";" + h.url + h.terminator }

func closeHyperlink(terminator string) string { return "\x1b]8;;" + terminator }

// styleTracker follows the SGR attributes and the hyperlink in force through
// a run of text, so a line break can carry them onto the next line — Pi's
// AnsiCodeTracker.
type styleTracker struct {
	bold, dim, italic, underline, blink, inverse, hidden, strike bool
	fg, bg                                                       string
	link                                                         *hyperlink
}

// process folds one control sequence into the state.
func (t *styleTracker) process(code string) {
	if link, ok := parseHyperlink(code); ok {
		t.link = link
		return
	}
	if !strings.HasPrefix(code, "\x1b[") || !strings.HasSuffix(code, "m") {
		return
	}
	params := code[2 : len(code)-1]
	for _, c := range params {
		if c != ';' && (c < '0' || c > '9') {
			return
		}
	}
	if params == "" || params == "0" {
		t.resetStyle()
		return
	}
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		n, _ := strconv.Atoi(parts[i])
		if n == 38 || n == 48 {
			// 256-colour (38;5;N) and truecolour (38;2;R;G;B) take their
			// arguments with them.
			var colour string
			switch {
			case i+2 < len(parts) && parts[i+1] == "5":
				colour = strings.Join(parts[i:i+3], ";")
				i += 2
			case i+4 < len(parts) && parts[i+1] == "2":
				colour = strings.Join(parts[i:i+5], ";")
				i += 4
			}
			if colour != "" {
				if n == 38 {
					t.fg = colour
				} else {
					t.bg = colour
				}
				continue
			}
		}
		switch {
		case n == 0:
			t.resetStyle()
		case n == 1:
			t.bold = true
		case n == 2:
			t.dim = true
		case n == 3:
			t.italic = true
		case n == 4:
			t.underline = true
		case n == 5:
			t.blink = true
		case n == 7:
			t.inverse = true
		case n == 8:
			t.hidden = true
		case n == 9:
			t.strike = true
		case n == 21:
			t.bold = false
		case n == 22:
			t.bold, t.dim = false, false
		case n == 23:
			t.italic = false
		case n == 24:
			t.underline = false
		case n == 25:
			t.blink = false
		case n == 27:
			t.inverse = false
		case n == 28:
			t.hidden = false
		case n == 29:
			t.strike = false
		case n == 39:
			t.fg = ""
		case n == 49:
			t.bg = ""
		case (n >= 30 && n <= 37) || (n >= 90 && n <= 97):
			t.fg = strconv.Itoa(n)
		case (n >= 40 && n <= 47) || (n >= 100 && n <= 107):
			t.bg = strconv.Itoa(n)
		}
	}
}

// resetStyle is SGR 0, which leaves a hyperlink open: links are a separate
// state from styling.
func (t *styleTracker) resetStyle() {
	link := t.link
	*t = styleTracker{link: link}
}

// processText folds every control sequence in s into the state.
func (t *styleTracker) processText(s string) {
	for i := 0; i < len(s); {
		if code, ok := extractANSI(s, i); ok {
			t.process(code)
			i += len(code)
			continue
		}
		i++
	}
}

// active is what reopens the current state at the start of a line.
func (t *styleTracker) active() string {
	var codes []string
	for _, on := range []struct {
		set  bool
		code string
	}{{t.bold, "1"}, {t.dim, "2"}, {t.italic, "3"}, {t.underline, "4"}, {t.blink, "5"},
		{t.inverse, "7"}, {t.hidden, "8"}, {t.strike, "9"}} {
		if on.set {
			codes = append(codes, on.code)
		}
	}
	if t.fg != "" {
		codes = append(codes, t.fg)
	}
	if t.bg != "" {
		codes = append(codes, t.bg)
	}
	out := ""
	if len(codes) > 0 {
		out = "\x1b[" + strings.Join(codes, ";") + "m"
	}
	if t.link != nil {
		out += t.link.open()
	}
	return out
}

// lineEnd is what a wrapped line must close before it breaks: underline,
// which would otherwise run on into the padding after the text, and an open
// link, which is reopened on the next line. A background is deliberately
// left on, so the padding a caller adds keeps it.
func (t *styleTracker) lineEnd() string {
	out := ""
	if t.underline {
		out += "\x1b[24m"
	}
	if t.link != nil {
		out += closeHyperlink(t.link.terminator)
	}
	return out
}

// openLinkClose is the sequence closing the hyperlink left open at the end of
// prefix, or nothing when none is.
func openLinkClose(prefix string) string {
	if !strings.Contains(prefix, "\x1b]8;") {
		return ""
	}
	var t styleTracker
	t.processText(prefix)
	if t.link == nil {
		return ""
	}
	return closeHyperlink(t.link.terminator)
}
