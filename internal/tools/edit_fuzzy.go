package tools

import (
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Fuzzy matching lets an edit land when the model wrote what it would
// naturally type — ASCII quotes, a plain hyphen, no trailing spaces — and the
// file holds a typographic or padded form of the same text.
//
// It is a fallback, never a preference: an edit whose oldText appears exactly
// is applied exactly, and a call drops into normalised space only when some
// edit in it cannot be found any other way.

// fuzzyFolds maps the characters a model is least likely to reproduce onto the
// ASCII it will write instead. Applied after NFKC, which already folds most
// compatibility forms (fullwidth letters and punctuation, most special spaces),
// so these are the typographic ones NFKC deliberately leaves alone.
var fuzzyFolds = strings.NewReplacer(
	// Smart single quotes.
	"\u2018", "'", "\u2019", "'", "\u201a", "'", "\u201b", "'",
	// Smart double quotes.
	"\u201c", `"`, "\u201d", `"`, "\u201e", `"`, "\u201f", `"`,
	// Hyphen, non-breaking hyphen, figure dash, en and em dash, horizontal
	// bar, minus sign.
	"\u2010", "-", "\u2011", "-", "\u2012", "-", "\u2013", "-", "\u2014", "-", "\u2015", "-", "\u2212", "-",
	// No-break space, the U+2002-U+200A spaces, narrow no-break, medium
	// mathematical and ideographic space.
	"\u00a0", " ", "\u2002", " ", "\u2003", " ", "\u2004", " ", "\u2005", " ", "\u2006", " ",
	"\u2007", " ", "\u2008", " ", "\u2009", " ", "\u200a", " ", "\u202f", " ", "\u205f", " ", "\u3000", " ",
)

// normalizeForFuzzyMatch is the view of a text both sides are compared in.
//
// It never adds or removes a line break, which is what lets a match found here
// be mapped back onto the original file line for line.
func normalizeForFuzzyMatch(text string) string {
	lines := strings.Split(norm.NFKC.String(text), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRightFunc(line, isTrailingSpace)
	}
	return fuzzyFolds.Replace(strings.Join(lines, "\n"))
}

// isTrailingSpace is the set of characters stripped from a line's end: the
// whitespace and line terminators JavaScript's trimEnd removes, which is what
// Pi strips. Go's unicode.IsSpace differs in both directions — it includes
// U+0085 and excludes U+FEFF — so a line that matches under Pi would not here.
func isTrailingSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', '\u2028', '\u2029', '\ufeff':
		return true
	}
	return unicode.Is(unicode.Zs, r)
}

// applyPreservingUnchangedLines applies replacements located in base, the
// normalised view of original, while keeping every line no replacement
// touches exactly as original has it.
//
// Writing base back would be simpler and wrong: normalising strips trailing
// whitespace and folds quotes on every line of the file, so an edit to one
// line would silently rewrite all the others. Each replacement is widened to
// the whole lines it touches; those lines come from base with the replacement
// applied, and everything between them is copied from original.
//
// Which lines to keep is decided from each replacement's own range, never by
// comparing lines: a replacement can make its line identical to a neighbour,
// and alignment by content would then keep the wrong one.
func applyPreservingUnchangedLines(original, base string, located []matched) (string, error) {
	originalLines := splitLinesWithEndings(original)
	baseLines := lineSpans(base)
	if len(originalLines) != len(baseLines) {
		return "", fmt.Errorf("cannot preserve unchanged lines: the normalised content has %d lines "+
			"and the original %d", len(baseLines), len(originalLines))
	}

	// located is sorted by position, so replacements touching shared lines
	// are adjacent and a group only ever grows at its end.
	type group struct {
		start, end int // line range, end exclusive
		members    []matched
	}
	var groups []group
	for _, m := range located {
		start, end, err := touchedLines(baseLines, m)
		if err != nil {
			return "", err
		}
		if n := len(groups); n > 0 && start < groups[n-1].end {
			groups[n-1].end = max(groups[n-1].end, end)
			groups[n-1].members = append(groups[n-1].members, m)
			continue
		}
		groups = append(groups, group{start: start, end: end, members: []matched{m}})
	}

	var out strings.Builder
	next := 0
	for _, g := range groups {
		out.WriteString(strings.Join(originalLines[next:g.start], ""))
		from, to := baseLines[g.start].start, baseLines[g.end-1].end
		out.WriteString(splice(base[from:to], g.members, from))
		next = g.end
	}
	out.WriteString(strings.Join(originalLines[next:], ""))
	return out.String(), nil
}

// splice applies located replacements to content, whose first byte sits at
// offset in the text they were located in. Applied from the end, so each
// splice leaves the offsets of the ones before it untouched.
func splice(content string, located []matched, offset int) string {
	for i := len(located) - 1; i >= 0; i-- {
		m := located[i]
		at := m.at - offset
		content = content[:at] + m.with + content[at+m.length:]
	}
	return content
}

type span struct{ start, end int }

// splitLinesWithEndings splits content into lines that keep their "\n", so
// joining them gives content back byte for byte. A final line without one is
// kept as it is; an empty content has no lines.
func splitLinesWithEndings(content string) []string {
	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func lineSpans(content string) []span {
	lines := splitLinesWithEndings(content)
	spans := make([]span, len(lines))
	offset := 0
	for i, line := range lines {
		spans[i] = span{start: offset, end: offset + len(line)}
		offset += len(line)
	}
	return spans
}

// touchedLines is the range of whole lines a replacement overlaps, end
// exclusive.
func touchedLines(lines []span, m matched) (int, int, error) {
	from, to := m.at, m.at+m.length
	start := -1
	for i, l := range lines {
		if from >= l.start && from < l.end {
			start = i
			break
		}
	}
	if start < 0 {
		return 0, 0, fmt.Errorf("a replacement at byte %d lies outside the content", from)
	}
	end := start
	for end < len(lines) && lines[end].end < to {
		end++
	}
	if end >= len(lines) {
		return 0, 0, fmt.Errorf("a replacement ending at byte %d lies outside the content", to)
	}
	return start, end + 1, nil
}
