package tui

import (
	"strconv"
	"strings"
	"testing"
)

// vt is a terminal small enough to keep with the tests: it applies what the
// renderer writes — printable text, carriage return, line feed with
// scrolling into scrollback, cursor up, down and to a column, line, screen
// and scrollback clears — to a grid, so a test asserts what a user would see
// rather than which bytes produced it. Styling, hyperlinks and mode switches
// are accepted and ignored; text that would run past the right edge of a row
// fails the test, because a real terminal would wrap it and every row after
// it would be off.
type vt struct {
	t             *testing.T
	width, height int
	rows          [][]string // each cell holds a cluster; "" continues a wide one
	scrollback    []string
	row, col      int
	cursorShown   bool
	pending       string
}

func newVT(t *testing.T, width, height int) *vt {
	v := &vt{t: t, width: width, height: height}
	v.rows = make([][]string, height)
	for i := range v.rows {
		v.rows[i] = v.blank()
	}
	return v
}

func (v *vt) blank() []string {
	row := make([]string, v.width)
	for i := range row {
		row[i] = " "
	}
	return row
}

func (v *vt) Write(p []byte) (int, error) {
	s := v.pending + string(p)
	v.pending = ""
	for i := 0; i < len(s); {
		switch s[i] {
		case 0x1b:
			n, complete := v.escape(s[i:])
			if !complete {
				v.pending = s[i:]
				return len(p), nil
			}
			i += n
		case '\r':
			v.col = 0
			i++
		case '\n':
			v.lineFeed()
			i++
		default:
			end := i
			for end < len(s) && s[end] != 0x1b && s[end] != '\r' && s[end] != '\n' {
				end++
			}
			eachGrapheme(s[i:end], func(cluster string, w int) bool {
				v.put(cluster, w)
				return true
			})
			i = end
		}
	}
	return len(p), nil
}

// put writes one cluster. Like xterm, a terminal whose last column was just
// filled holds the cursor there and wraps only when the next character
// arrives; a cluster that could not fit even on a fresh line fails the test.
func (v *vt) put(cluster string, w int) {
	if w == 0 {
		return
	}
	if v.col+w > v.width {
		if w > v.width || v.col < v.width {
			v.t.Errorf("vt: %q written at column %d of a %d-column terminal", cluster, v.col, v.width)
			return
		}
		v.col = 0
		v.lineFeed()
	}
	v.rows[v.row][v.col] = cluster
	for k := 1; k < w; k++ {
		v.rows[v.row][v.col+k] = ""
	}
	v.col += w
}

func (v *vt) lineFeed() {
	if v.row < v.height-1 {
		v.row++
		return
	}
	v.scrollback = append(v.scrollback, render(v.rows[0]))
	copy(v.rows, v.rows[1:])
	v.rows[v.height-1] = v.blank()
}

// escape applies one control sequence and reports its length.
func (v *vt) escape(s string) (int, bool) {
	if len(s) < 2 {
		return 0, false
	}
	switch s[1] {
	case '[':
		end := 2
		for end < len(s) && (s[end] < 0x40 || s[end] > 0x7e) {
			end++
		}
		if end >= len(s) {
			return 0, false
		}
		v.csi(s[2:end], s[end])
		return end + 1, true
	case ']', '_':
		for j := 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1, true
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2, true
			}
		}
		return 0, false
	}
	return 2, true
}

func (v *vt) csi(params string, final byte) {
	if strings.HasPrefix(params, "?") {
		if params == "?25" {
			v.cursorShown = final == 'h'
		}
		return
	}
	n := 1
	if params != "" {
		if parsed, err := strconv.Atoi(strings.Split(params, ";")[0]); err == nil {
			n = parsed
		}
	}
	switch final {
	case 'A':
		v.row = max(0, v.row-n)
	case 'B':
		v.row = min(v.height-1, v.row+n)
	case 'G':
		v.col = min(v.width-1, max(0, n-1))
	case 'H':
		v.row, v.col = 0, 0
	case 'K':
		start := v.col
		if params == "2" {
			start = 0
		}
		for k := start; k < v.width; k++ {
			v.rows[v.row][k] = " "
		}
	case 'J':
		switch params {
		case "2":
			for i := range v.rows {
				v.rows[i] = v.blank()
			}
		case "3":
			v.scrollback = nil
		}
	}
}

func render(row []string) string { return strings.TrimRight(strings.Join(row, ""), " ") }

// screen is the visible rows, trailing spaces trimmed.
func (v *vt) screen() []string {
	out := make([]string, len(v.rows))
	for i, row := range v.rows {
		out[i] = render(row)
	}
	return out
}

// transcript is scrollback then screen, with trailing blank rows dropped:
// everything the user could scroll back through.
func (v *vt) transcript() []string {
	all := append(append([]string(nil), v.scrollback...), v.screen()...)
	for len(all) > 0 && all[len(all)-1] == "" {
		all = all[:len(all)-1]
	}
	return all
}

// resize changes the size the way xterm does: shrinking pushes rows off the
// top into scrollback so the cursor's row stays on screen, growing pulls rows
// back down from scrollback, and a narrower terminal clips each row.
func (v *vt) resize(width, height int) {
	for v.height > height {
		if v.row >= height {
			v.scrollback = append(v.scrollback, render(v.rows[0]))
			v.rows = v.rows[1:]
			v.row--
		} else {
			v.rows = v.rows[:len(v.rows)-1]
		}
		v.height--
	}
	for v.height < height {
		if n := len(v.scrollback); n > 0 {
			pulled := make([]string, v.width)
			for k := range pulled {
				pulled[k] = " "
			}
			for k, cluster := range []rune(v.scrollback[n-1]) {
				if k < v.width {
					pulled[k] = string(cluster)
				}
			}
			v.scrollback = v.scrollback[:n-1]
			v.rows = append([][]string{pulled}, v.rows...)
			v.row++
		} else {
			v.rows = append(v.rows, v.blank())
		}
		v.height++
	}
	for i, row := range v.rows {
		resized := make([]string, width)
		for k := range resized {
			resized[k] = " "
			if k < len(row) {
				resized[k] = row[k]
			}
		}
		v.rows[i] = resized
	}
	v.width = width
	v.col = min(v.col, width-1)
}

func TestTheTestTerminalScrollsIntoScrollback(t *testing.T) {
	v := newVT(t, 10, 2)
	v.Write([]byte("one\r\ntwo\r\nthree"))
	if got := strings.Join(v.transcript(), "|"); got != "one|two|three" {
		t.Fatalf("transcript %q", got)
	}
	v.Write([]byte("\x1b[1A\r\x1b[2Kfour"))
	if got := strings.Join(v.screen(), "|"); got != "four|three" {
		t.Fatalf("screen %q", got)
	}
}
