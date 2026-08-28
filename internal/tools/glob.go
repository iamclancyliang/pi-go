package tools

import "strings"

// matchGlob reports whether a slash-separated path matches a glob pattern.
//
// Go's path.Match cannot express `**`, and `**` is the difference between
// "files in this directory" and "files anywhere below it" — the distinction a
// model relies on when it writes `src/**/*_test.go`. So the matching is done
// here rather than borrowed.
//
// The segment rules are the usual ones: `*` and `?` never cross a separator,
// `**` as a whole segment matches any number of segments including none, and a
// bracket class matches one character.
func matchGlob(pattern, path string) bool {
	return matchSegments(splitPath(pattern), splitPath(path))
}

func splitPath(s string) []string {
	s = strings.Trim(s, "/")
	if s == "" {
		return nil
	}
	return strings.Split(s, "/")
}

func matchSegments(pattern, path []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			// Trailing `**` matches whatever is left, including nothing.
			if len(pattern) == 1 {
				return true
			}
			// Try consuming zero segments first, then more. Consuming greedily
			// and never backing off would fail `**/a` against `x/a`.
			for skip := 0; skip <= len(path); skip++ {
				if matchSegments(pattern[1:], path[skip:]) {
					return true
				}
			}
			return false
		}
		if len(path) == 0 {
			return false
		}
		if !matchSegment(pattern[0], path[0]) {
			return false
		}
		pattern, path = pattern[1:], path[1:]
	}
	return len(path) == 0
}

// matchSegment matches one path segment, where `*` and `?` do not cross a
// separator because there is none left to cross.
func matchSegment(pattern, name string) bool {
	// Index into both, backtracking on the last `*` when a later literal fails.
	// A purely recursive version is simpler to read but goes quadratic on
	// patterns holding several stars, which a model writes routinely.
	var p, n, starP, starN int
	starP = -1
	for n < len(name) {
		switch {
		case p < len(pattern) && pattern[p] == '*':
			starP, starN = p, n
			p++
		case p < len(pattern) && pattern[p] == '?':
			p++
			n++
		case p < len(pattern) && pattern[p] == '[':
			end, ok := matchClass(pattern[p:], name[n])
			if !ok {
				if starP < 0 {
					return false
				}
				starN++
				p, n = starP+1, starN
				continue
			}
			p += end
			n++
		case p < len(pattern) && pattern[p] == name[n]:
			p++
			n++
		case starP >= 0:
			// Give the last star one more character and resume after it.
			starN++
			p, n = starP+1, starN
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// matchClass matches a bracket expression against one byte, returning how much
// of the pattern it consumed. An unterminated class is not a class: it is
// treated as the literal `[` it looks like, which is what a model that wrote a
// filename containing a bracket meant.
func matchClass(pattern string, c byte) (int, bool) {
	i := 1
	negated := false
	if i < len(pattern) && (pattern[i] == '^' || pattern[i] == '!') {
		negated = true
		i++
	}
	matched := false
	first := true
	for i < len(pattern) && (pattern[i] != ']' || first) {
		first = false
		if i+2 < len(pattern) && pattern[i+1] == '-' && pattern[i+2] != ']' {
			if c >= pattern[i] && c <= pattern[i+2] {
				matched = true
			}
			i += 3
			continue
		}
		if pattern[i] == c {
			matched = true
		}
		i++
	}
	if i >= len(pattern) {
		// Never closed, so it was a literal bracket all along. One pattern byte
		// is consumed, not zero: returning zero would leave the cursor where it
		// was and match the same byte forever.
		return 1, pattern[0] == c
	}
	return i + 1, matched != negated
}
