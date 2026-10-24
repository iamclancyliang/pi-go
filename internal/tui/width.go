package tui

import (
	"strings"
	"unicode"

	"github.com/rivo/uniseg"
)

// tabWidth is how many cells a tab takes. Pi fixes it at three rather than
// letting the terminal expand it to the next stop, because a tab whose width
// depends on its column cannot be measured before the line is placed.
const tabWidth = 3

// eachGrapheme calls fn with every grapheme cluster of plain text — no control
// sequences — and the cells it takes, stopping when fn returns false.
func eachGrapheme(s string, fn func(cluster string, width int) bool) {
	state := -1
	for len(s) > 0 {
		var cluster string
		cluster, s, _, state = uniseg.FirstGraphemeClusterInString(s, state)
		if !fn(cluster, clusterWidth(cluster)) {
			return
		}
	}
}

// clusterWidth is the cells one grapheme cluster takes, by Pi's rules
// (graphemeWidth, utils.ts). uniseg finds the clusters; the width is decided
// here, because terminals give cells to more than a cluster's first
// character: an Indic conjunct, a Myanmar spacing mark and a Thai or Lao AM
// vowel each take one of their own.
func clusterWidth(cluster string) int {
	if cluster == "\t" {
		return tabWidth
	}
	runes := []rune(cluster)
	if allRunes(runes, isTerminalSpacingMark) {
		return len(runes)
	}
	if allRunes(runes, isZeroWidth) {
		return 0
	}
	if couldBeEmoji(runes) && uniseg.StringWidth(cluster) == 2 {
		return 2
	}

	base := runes
	for len(base) > 0 && isNonPrinting(base[0]) {
		base = base[1:]
	}
	if len(base) == 0 {
		return 0
	}
	// A lone regional indicator — half a flag, as a stream delivers it — is
	// drawn two cells wide; counting it as one makes the line drift when the
	// other half arrives.
	if base[0] >= 0x1F1E6 && base[0] <= 0x1F1FF {
		return 2
	}

	width := eastAsianWidth(base[0])
	followsMark := false
	for _, r := range base[1:] {
		switch {
		case isTerminalSpacingMark(r):
			width++
			followsMark = false
		case unicode.Is(unicode.M, r):
			followsMark = true
		case !isNonPrinting(r):
			if followsMark || (r >= 0xFF00 && r <= 0xFFEF) {
				width += eastAsianWidth(r)
			} else if r == 0x0E33 || r == 0x0EB3 {
				width++
			}
			followsMark = false
		}
	}
	return width
}

func allRunes(runes []rune, pred func(rune) bool) bool {
	for _, r := range runes {
		if !pred(r) {
			return false
		}
	}
	return len(runes) > 0
}

// eastAsianWidth is two for wide and fullwidth characters and one otherwise,
// ambiguous ones included, as get-east-asian-width answers by default.
func eastAsianWidth(r rune) int {
	if uniseg.StringWidth(string(r)) == 2 {
		return 2
	}
	return 1
}

// couldBeEmoji is Pi's cheap pre-check before the emoji test: the blocks
// emoji live in, an emoji presentation selector, or a sequence longer than
// one UTF-16 surrogate pair.
func couldBeEmoji(runes []rune) bool {
	r := runes[0]
	if (r >= 0x1F000 && r <= 0x1FBFF) || (r >= 0x2300 && r <= 0x23FF) ||
		(r >= 0x2600 && r <= 0x27BF) || (r >= 0x2B50 && r <= 0x2B55) {
		return true
	}
	units := 0
	for _, c := range runes {
		if c == 0xFE0F {
			return true
		}
		units++
		if c > 0xFFFF {
			units++
		}
	}
	return units > 2
}

// isTerminalSpacingMark reports a mark terminals give a cell to: the Unicode
// spacing marks less three that legacy width tables treat as zero, plus the
// non-spacing exceptions those tables count as one.
func isTerminalSpacingMark(r rune) bool {
	switch r {
	case 0x1734, 0x302E, 0x302F:
		return false
	case 0x065F, 0x0F7F, 0x102B, 0x102C, 0x1031, 0x1038:
		return true
	}
	if (r >= 0x1033 && r <= 0x1035) || (r >= 0x103A && r <= 0x103E) {
		return true
	}
	return unicode.Is(unicode.Mc, r)
}

func isZeroWidth(r rune) bool {
	return isDefaultIgnorable(r) || unicode.Is(unicode.Cc, r) || unicode.Is(unicode.M, r) || isSurrogate(r)
}

func isNonPrinting(r rune) bool {
	return isZeroWidth(r) || unicode.Is(unicode.Cf, r)
}

func isSurrogate(r rune) bool { return r >= 0xD800 && r <= 0xDFFF }

// isDefaultIgnorable is Unicode's Default_Ignorable_Code_Point, which Go's
// unicode package does not carry as one table: the derived property's
// ranges, from DerivedCoreProperties.txt.
func isDefaultIgnorable(r rune) bool {
	switch {
	case r == 0x00AD, r == 0x034F, r == 0x061C, r == 0x3164, r == 0xFEFF, r == 0xFFA0:
		return true
	case r >= 0x115F && r <= 0x1160, r >= 0x17B4 && r <= 0x17B5, r >= 0x180B && r <= 0x180F,
		r >= 0x200B && r <= 0x200F, r >= 0x202A && r <= 0x202E, r >= 0x2060 && r <= 0x206F,
		r >= 0xFE00 && r <= 0xFE0F, r >= 0xFFF0 && r <= 0xFFF8, r >= 0x1BCA0 && r <= 0x1BCA3,
		r >= 0x1D173 && r <= 0x1D17A, r >= 0xE0000 && r <= 0xE0FFF:
		return true
	}
	return false
}

func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// VisibleWidth is how many terminal cells s takes, ignoring the control
// sequences in it.
func VisibleWidth(s string) int {
	if isPrintableASCII(s) {
		return len(s)
	}
	width := 0
	forEachText(s, func(text string) {
		eachGrapheme(text, func(_ string, w int) bool {
			width += w
			return true
		})
	})
	return width
}

// forEachText calls fn with each run of s between control sequences.
func forEachText(s string, fn func(text string)) {
	start := 0
	for i := 0; i < len(s); {
		if code, ok := extractANSI(s, i); ok {
			if i > start {
				fn(s[start:i])
			}
			i += len(code)
			start = i
			continue
		}
		i++
	}
	if start < len(s) {
		fn(s[start:])
	}
}

// StripSequences is s without its control sequences.
func StripSequences(s string) string {
	if !strings.Contains(s, esc) {
		return s
	}
	var b strings.Builder
	forEachText(s, func(text string) { b.WriteString(text) })
	return b.String()
}

// normalizeOutput prepares a rendered line for the terminal, as Pi's
// normalizeTerminalOutput does: tabs become three spaces outside control
// sequences, so the terminal draws exactly the width that was measured; and
// the Thai and Lao AM vowels are written decomposed, the form terminals
// allocate the measured cells for.
func normalizeOutput(s string) string {
	if strings.ContainsRune(s, 0x0E33) || strings.ContainsRune(s, 0x0EB3) {
		s = strings.NewReplacer(string(rune(0x0E33)), string([]rune{0x0E4D, 0x0E32}),
			string(rune(0x0EB3)), string([]rune{0x0ECD, 0x0EB2})).Replace(s)
	}
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if code, ok := extractANSI(s, i); ok {
			b.WriteString(code)
			i += len(code)
			continue
		}
		if s[i] == '\t' {
			b.WriteString(strings.Repeat(" ", tabWidth))
		} else {
			b.WriteByte(s[i])
		}
		i++
	}
	return b.String()
}

// TruncateToWidth fits text into maxWidth cells, ending in ellipsis when it
// had to cut, and padded with spaces to exactly maxWidth when pad is set —
// Pi's truncateToWidth.
//
// What is kept is a contiguous prefix: a wide character that does not fit
// ends the text there, rather than being skipped for narrower ones after it.
// A cut text is closed with a style reset and its hyperlink closed, so the
// ellipsis is drawn plain and nothing runs on past it.
func TruncateToWidth(text string, maxWidth int, ellipsis string, pad bool) string {
	if maxWidth <= 0 {
		return ""
	}
	padTo := func(s string, width int) string {
		if !pad || width >= maxWidth {
			return s
		}
		return s + strings.Repeat(" ", maxWidth-width)
	}
	if text == "" {
		return padTo("", 0)
	}

	ellipsisWidth := VisibleWidth(ellipsis)
	if ellipsisWidth >= maxWidth {
		if w := VisibleWidth(text); w <= maxWidth {
			return padTo(text, w)
		}
		clipped, w := truncateFragment(ellipsis, maxWidth)
		if w == 0 {
			return padTo("", 0)
		}
		return finishTruncated("", 0, clipped, w, maxWidth, pad)
	}

	if isPrintableASCII(text) {
		if len(text) <= maxWidth {
			return padTo(text, len(text))
		}
		target := maxWidth - ellipsisWidth
		return finishTruncated(text[:target], target, ellipsis, ellipsisWidth, maxWidth, pad)
	}

	target := maxWidth - ellipsisWidth
	var kept, pending strings.Builder
	keptWidth, seen := 0, 0
	contiguous, overflowed := true, false

	take := func(piece string, width int) {
		if contiguous && keptWidth+width <= target {
			kept.WriteString(pending.String())
			pending.Reset()
			kept.WriteString(piece)
			keptWidth += width
		} else {
			// Styling before a piece that is not kept is not kept either:
			// it would style nothing and could leave an attribute open.
			contiguous = false
			pending.Reset()
		}
		seen += width
		if seen > maxWidth {
			overflowed = true
		}
	}

	for i := 0; i < len(text) && !overflowed; {
		if code, ok := extractANSI(text, i); ok {
			pending.WriteString(code)
			i += len(code)
			continue
		}
		if text[i] == '\t' {
			take("\t", tabWidth)
			i++
			continue
		}
		end := i
		for end < len(text) && text[end] != '\t' {
			if _, ok := extractANSI(text, end); ok {
				break
			}
			end++
		}
		eachGrapheme(text[i:end], func(cluster string, w int) bool {
			take(cluster, w)
			return !overflowed
		})
		i = end
	}

	if !overflowed {
		return padTo(text, seen)
	}
	return finishTruncated(kept.String(), keptWidth, ellipsis, ellipsisWidth, maxWidth, pad)
}

// truncateFragment keeps as much of text as fits in maxWidth cells, with no
// ellipsis.
func truncateFragment(text string, maxWidth int) (string, int) {
	var b, pending strings.Builder
	width := 0
	full := false
	for i := 0; i < len(text) && !full; {
		if code, ok := extractANSI(text, i); ok {
			pending.WriteString(code)
			i += len(code)
			continue
		}
		end := i
		for end < len(text) {
			if _, ok := extractANSI(text, end); ok {
				break
			}
			end++
		}
		eachGrapheme(text[i:end], func(cluster string, w int) bool {
			if width+w > maxWidth {
				full = true
				return false
			}
			b.WriteString(pending.String())
			pending.Reset()
			b.WriteString(cluster)
			width += w
			return true
		})
		i = end
	}
	return b.String(), width
}

func finishTruncated(prefix string, prefixWidth int, ellipsis string, ellipsisWidth, maxWidth int, pad bool) string {
	const reset = "\x1b[0m"
	out := prefix + openLinkClose(prefix) + reset
	if ellipsis != "" {
		out += ellipsis + reset
	}
	if width := prefixWidth + ellipsisWidth; pad && width < maxWidth {
		out += strings.Repeat(" ", maxWidth-width)
	}
	return out
}

// PadToWidth fills line with spaces to width cells; a line already that wide
// or wider is returned as it is.
func PadToWidth(line string, width int) string {
	if w := VisibleWidth(line); w < width {
		return line + strings.Repeat(" ", width-w)
	}
	return line
}

// WrapText breaks text into lines of at most width cells, at spaces where it
// can and inside a word only when the word alone is too long — Pi's
// wrapTextWithAnsi. It does no padding. Styles and hyperlinks in force at a
// break are reopened at the start of the next line, and an explicit line
// break carries them too. A run of Chinese, Japanese or Korean text may break
// between any two of its characters, as it is written without spaces.
func WrapText(text string, width int) []string {
	if text == "" {
		return []string{""}
	}
	var out []string
	var carried styleTracker
	for _, line := range splitLines(text) {
		prefix := ""
		if len(out) > 0 {
			prefix = carried.active()
		}
		out = append(out, wrapLine(prefix+line, width)...)
		carried.processText(line)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

func wrapLine(line string, width int) []string {
	if line == "" {
		return []string{""}
	}
	if VisibleWidth(line) <= width {
		return []string{line}
	}

	var wrapped []string
	var tracker styleTracker
	current, currentWidth := "", 0

	for _, token := range wrapTokens(line) {
		tokenWidth := VisibleWidth(token)
		// Blank as Pi judges it: a space carrying a style code is not, so the
		// style is not dropped with it at a line start.
		blank := strings.TrimSpace(token) == ""

		if tokenWidth > width && !blank {
			if current != "" {
				wrapped = append(wrapped, current+tracker.lineEnd())
				current, currentWidth = "", 0
			}
			broken := breakWord(token, width, &tracker)
			wrapped = append(wrapped, broken[:len(broken)-1]...)
			current = broken[len(broken)-1]
			currentWidth = VisibleWidth(current)
			continue
		}

		if currentWidth+tokenWidth > width && currentWidth > 0 {
			wrapped = append(wrapped, trimRightSpace(current)+tracker.lineEnd())
			if blank {
				// A line does not start with the space it broke at.
				current, currentWidth = tracker.active(), 0
			} else {
				current, currentWidth = tracker.active()+token, tokenWidth
			}
		} else {
			current += token
			currentWidth += tokenWidth
		}
		tracker.processText(token)
	}
	if current != "" {
		wrapped = append(wrapped, current)
	}
	// A trailing space would otherwise push a line past the width.
	for i, l := range wrapped {
		wrapped[i] = trimRightSpace(l)
	}
	if len(wrapped) == 0 {
		return []string{""}
	}
	return wrapped
}

func trimRightSpace(s string) string { return strings.TrimRightFunc(s, unicode.IsSpace) }

// isCJK reports a cluster from a script written without spaces between
// words, where a line may break between any two characters.
func isCJK(cluster string) bool {
	r := firstRune(cluster)
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Bopomofo)
}

// wrapTokens splits a line into runs of spaces and runs of other text, each
// CJK character its own token, with control sequences attached to the
// visible text they precede.
func wrapTokens(line string) []string {
	var tokens []string
	current, pending := "", ""
	kind := 0 // 0 none, 1 space, 2 word
	flush := func() {
		if current != "" {
			tokens = append(tokens, current)
			current, kind = "", 0
		}
	}
	for i := 0; i < len(line); {
		if code, ok := extractANSI(line, i); ok {
			pending += code
			i += len(code)
			continue
		}
		end := i
		for end < len(line) {
			if _, ok := extractANSI(line, end); ok {
				break
			}
			end++
		}
		eachGrapheme(line[i:end], func(cluster string, _ int) bool {
			space := cluster == " "
			if !space && isCJK(cluster) {
				flush()
				tokens = append(tokens, pending+cluster)
				pending = ""
				return true
			}
			k := 2
			if space {
				k = 1
			}
			if current != "" && kind != k {
				flush()
			}
			current += pending + cluster
			pending, kind = "", k
			return true
		})
		i = end
	}
	if pending != "" {
		switch {
		case current != "":
			current += pending
		case len(tokens) > 0:
			tokens[len(tokens)-1] += pending
		default:
			current = pending
		}
	}
	flush()
	return tokens
}

// breakWord splits a token too long for any line at grapheme boundaries.
func breakWord(word string, width int, tracker *styleTracker) []string {
	var lines []string
	current, currentWidth := tracker.active(), 0
	for i := 0; i < len(word); {
		if code, ok := extractANSI(word, i); ok {
			current += code
			tracker.process(code)
			i += len(code)
			continue
		}
		end := i
		for end < len(word) {
			if _, ok := extractANSI(word, end); ok {
				break
			}
			end++
		}
		eachGrapheme(word[i:end], func(cluster string, w int) bool {
			if currentWidth+w > width {
				lines = append(lines, current+tracker.lineEnd())
				current, currentWidth = tracker.active(), 0
			}
			current += cluster
			currentWidth += w
			return true
		})
		i = end
	}
	if current != "" {
		lines = append(lines, current)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}
