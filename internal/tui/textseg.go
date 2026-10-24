package tui

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// segment is one grapheme cluster or word of a line, at a byte offset.
// wordLike marks a word that holds letters or digits rather than spaces,
// punctuation or symbols — Intl.Segmenter's isWordLike, which Pi's word
// motion is defined in terms of.
type segment struct {
	text     string
	index    int
	wordLike bool
}

func graphemeSegments(s string) []segment {
	var out []segment
	state, at := -1, 0
	for len(s) > 0 {
		var cluster string
		cluster, s, _, state = uniseg.FirstGraphemeClusterInString(s, state)
		out = append(out, segment{text: cluster, index: at})
		at += len(cluster)
	}
	return out
}

// wordSegments splits s into words by Unicode's word boundaries.
//
// Pi's segmenter is ICU's, which splits Chinese and Japanese text into
// dictionary words; Unicode's rules, without a dictionary, make every
// ideograph a word of its own. A run of ideographs and kana is kept together
// here instead — the closer of the two approximations, since a dictionary
// word never crosses punctuation and a clause rarely holds many: in
// "你好，世界" the words are 你好, ， and 世界, as Pi has them.
func wordSegments(s string) []segment {
	var out []segment
	state, at := -1, 0
	for len(s) > 0 {
		var word string
		word, s, state = uniseg.FirstWordInString(s, state)
		seg := segment{text: word, index: at, wordLike: isWordLike(word)}
		at += len(word)
		if n := len(out); n > 0 && ideographic(out[n-1].text) && ideographic(word) {
			out[n-1].text += word
			continue
		}
		out = append(out, seg)
	}
	return out
}

func isWordLike(word string) bool {
	for _, r := range word {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

func ideographic(word string) bool {
	if word == "" {
		return false
	}
	for _, r := range word {
		if !unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) {
			return false
		}
	}
	return true
}

// isWhitespace reports text holding whitespace as JavaScript's \s defines
// it, which is what Pi's editor tests a grapheme against.
func isWhitespace(s string) bool {
	return strings.IndexFunc(s, jsSpace) >= 0
}

func jsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// punctuation is the ASCII punctuation Pi's word motion stops at even inside
// a word: "foo.bar" is one word to the segmenter and two stops to the cursor.
const punctuation = "(){}[]<>.,;:'\"!?+-=*/\\|&%^$#@~`"

// utf16Len is the length of s in UTF-16 code units. Pi's editor measures
// columns in these — they are JavaScript string indices — and the sticky
// column of vertical movement is one, so it is kept in the same unit to
// land where Pi lands.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// utf16Offset is the byte offset of the code unit at units in s, or of the
// start of the character it falls inside.
func utf16Offset(s string, units int) int {
	at := 0
	for at < len(s) && units > 0 {
		r, size := utf8.DecodeRuneInString(s[at:])
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if w > units {
			break
		}
		units -= w
		at += size
	}
	return at
}

// Paste markers stand in the text for large pastes: "[paste #1 +20 lines]"
// or "[paste #2 1234 chars]".
var (
	pasteMarker       = regexp.MustCompile(`\[paste #(\d+)( (\+\d+ lines|\d+ chars))?\]`)
	pasteMarkerSingle = regexp.MustCompile(`^\[paste #(\d+)( (\+\d+ lines|\d+ chars))?\]$`)
)

func isPasteMarker(s string) bool { return len(s) >= 10 && pasteMarkerSingle.MatchString(s) }

// withMarkers merges the segments inside each paste marker whose id is in
// valid into one, so the cursor, deletion and wrapping treat a marker as a
// single unit. A marker-like text with no paste behind it — typed by hand —
// stays ordinary text.
func withMarkers(text string, segments []segment, valid map[int]bool) []segment {
	if len(valid) == 0 || !strings.Contains(text, "[paste #") {
		return segments
	}
	type span struct{ start, end int }
	var markers []span
	for _, m := range pasteMarker.FindAllStringSubmatchIndex(text, -1) {
		id, _ := strconv.Atoi(text[m[2]:m[3]])
		if valid[id] {
			markers = append(markers, span{m[0], m[1]})
		}
	}
	if len(markers) == 0 {
		return segments
	}
	var out []segment
	next := 0
	for _, seg := range segments {
		for next < len(markers) && markers[next].end <= seg.index {
			next++
		}
		if next < len(markers) && seg.index >= markers[next].start && seg.index < markers[next].end {
			if seg.index == markers[next].start {
				out = append(out, segment{text: text[markers[next].start:markers[next].end], index: seg.index})
			}
			continue
		}
		out = append(out, seg)
	}
	return out
}

// textChunk is one visual line of a wrapped logical line, with its byte
// range in that line.
type textChunk struct {
	text       string
	start, end int
}

// wordWrapLine splits a line into chunks no wider than maxWidth, breaking
// after whitespace or beside a CJK character where it can and inside a word
// only where it must — Pi's wordWrapLine. segments are the line's graphemes,
// merged around paste markers; nil means plain graphemes.
func wordWrapLine(line string, maxWidth int, segments []segment) []textChunk {
	if line == "" || maxWidth <= 0 {
		return []textChunk{{}}
	}
	if VisibleWidth(line) <= maxWidth {
		return []textChunk{{text: line, end: len(line)}}
	}
	if segments == nil {
		segments = graphemeSegments(line)
	}

	var chunks []textChunk
	currentWidth, chunkStart := 0, 0
	// The last place a break is allowed: after whitespace that precedes
	// other text, or beside a CJK character.
	wrapAt, wrapWidth := -1, 0

	for i, seg := range segments {
		g := seg.text
		gWidth := VisibleWidth(g)
		marker := isPasteMarker(g)
		ws := !marker && isWhitespace(g)

		if currentWidth+gWidth > maxWidth {
			if wrapAt >= 0 && currentWidth-wrapWidth+gWidth <= maxWidth {
				chunks = append(chunks, textChunk{line[chunkStart:wrapAt], chunkStart, wrapAt})
				chunkStart = wrapAt
				currentWidth -= wrapWidth
			} else if chunkStart < seg.index {
				// No break that helps: the rest plus this grapheme would still
				// overflow, so break right here.
				chunks = append(chunks, textChunk{line[chunkStart:seg.index], chunkStart, seg.index})
				chunkStart = seg.index
				currentWidth = 0
			}
			wrapAt = -1
		}

		if gWidth > maxWidth {
			// One unit wider than the line — a paste marker in a narrow
			// terminal. It is split for display only; editing still treats
			// it as one.
			sub := wordWrapLine(g, maxWidth, nil)
			for _, c := range sub[:len(sub)-1] {
				chunks = append(chunks, textChunk{c.text, seg.index + c.start, seg.index + c.end})
			}
			last := sub[len(sub)-1]
			chunkStart = seg.index + last.start
			currentWidth = VisibleWidth(last.text)
			wrapAt = -1
			continue
		}

		currentWidth += gWidth

		if i+1 < len(segments) {
			next := segments[i+1]
			nextMarker := isPasteMarker(next.text)
			switch {
			case ws && (nextMarker || !isWhitespace(next.text)):
				wrapAt, wrapWidth = next.index, currentWidth
			case !ws && !isWhitespace(next.text):
				if (!marker && isCJK(g)) || (!nextMarker && isCJK(next.text)) {
					wrapAt, wrapWidth = next.index, currentWidth
				}
			}
		}
	}
	return append(chunks, textChunk{line[chunkStart:], chunkStart, len(line)})
}

// findWordBackward is where moving one word left from cursor lands: past
// trailing whitespace, then over one word — stopping at ASCII punctuation
// inside it — or one run of punctuation, or one paste marker.
func findWordBackward(text string, cursor int, segments []segment) int {
	if cursor <= 0 {
		return 0
	}
	at := cursor
	for len(segments) > 0 {
		last := segments[len(segments)-1]
		if isPasteMarker(last.text) || !isWhitespace(last.text) {
			break
		}
		at -= len(last.text)
		segments = segments[:len(segments)-1]
	}
	if len(segments) == 0 {
		return at
	}
	last := segments[len(segments)-1]
	switch {
	case isPasteMarker(last.text):
		at -= len(last.text)
	case last.wordLike:
		if i := strings.LastIndexAny(last.text, punctuation); i >= 0 {
			at -= len(last.text) - (i + 1)
		} else {
			at -= len(last.text)
		}
	default:
		for len(segments) > 0 {
			s := segments[len(segments)-1]
			if isPasteMarker(s.text) || s.wordLike || isWhitespace(s.text) {
				break
			}
			at -= len(s.text)
			segments = segments[:len(segments)-1]
		}
	}
	return at
}

// findWordForward is findWordBackward's mirror.
func findWordForward(text string, cursor int, segments []segment) int {
	if cursor >= len(text) {
		return len(text)
	}
	at := cursor
	for len(segments) > 0 && !isPasteMarker(segments[0].text) && isWhitespace(segments[0].text) {
		at += len(segments[0].text)
		segments = segments[1:]
	}
	if len(segments) == 0 {
		return at
	}
	first := segments[0]
	switch {
	case isPasteMarker(first.text):
		at += len(first.text)
	case first.wordLike:
		if i := strings.IndexAny(first.text, punctuation); i >= 0 {
			at += i
		} else {
			at += len(first.text)
		}
	default:
		for _, s := range segments {
			if isPasteMarker(s.text) || s.wordLike || isWhitespace(s.text) {
				break
			}
			at += len(s.text)
		}
	}
	return at
}
