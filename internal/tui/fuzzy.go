package tui

import (
	"regexp"
	"sort"
	"strings"
)

// FuzzyMatch reports whether query's characters appear in text in order,
// ignoring case, and how well: a lower score is a better match. Consecutive
// characters, characters at word starts and early matches score better,
// gaps worse, and an exact match best of all. A query of letters then digits
// also matches with the two swapped, a little worse — "codex52" finds
// "gpt-5.2-codex" — as Pi's fuzzyMatch does.
func FuzzyMatch(query, text string) (matches bool, score float64) {
	q, t := []rune(strings.ToLower(query)), []rune(strings.ToLower(text))
	if ok, s := fuzzyMatchRunes(q, t); ok {
		return true, s
	}
	var swapped string
	if m := lettersDigits.FindStringSubmatch(string(q)); m != nil {
		swapped = m[2] + m[1]
	} else if m := digitsLetters.FindStringSubmatch(string(q)); m != nil {
		swapped = m[2] + m[1]
	}
	if swapped == "" {
		return false, 0
	}
	if ok, s := fuzzyMatchRunes([]rune(swapped), t); ok {
		return true, s + 5
	}
	return false, 0
}

var (
	lettersDigits = regexp.MustCompile(`^([a-z]+)([0-9]+)$`)
	digitsLetters = regexp.MustCompile(`^([0-9]+)([a-z]+)$`)
)

func fuzzyMatchRunes(q, t []rune) (bool, float64) {
	if len(q) == 0 {
		return true, 0
	}
	if len(q) > len(t) {
		return false, 0
	}
	qi, last, consecutive := 0, -1, 0
	score := 0.0
	for i := 0; i < len(t) && qi < len(q); i++ {
		if t[i] != q[qi] {
			continue
		}
		if last == i-1 {
			consecutive++
			score -= float64(consecutive * 5)
		} else {
			consecutive = 0
			if last >= 0 {
				score += float64((i - last - 1) * 2)
			}
		}
		if i == 0 || strings.ContainsRune(" \t\n\r\f\v-_./:", t[i-1]) {
			score -= 10
		}
		score += float64(i) * 0.1
		last = i
		qi++
	}
	if qi < len(q) {
		return false, 0
	}
	if string(q) == string(t) {
		score -= 100
	}
	return true, score
}

// FuzzyFilter keeps the items whose text matches every whitespace- or
// slash-separated token of query, best matches first. An empty query keeps
// everything in order.
func FuzzyFilter[T any](items []T, query string, text func(T) string) []T {
	tokens := strings.FieldsFunc(query, func(r rune) bool { return r == '/' || jsSpace(r) })
	if len(tokens) == 0 {
		return items
	}
	type scored struct {
		item  T
		score float64
	}
	var results []scored
	for _, item := range items {
		s := text(item)
		total, all := 0.0, true
		for _, token := range tokens {
			ok, score := FuzzyMatch(token, s)
			if !ok {
				all = false
				break
			}
			total += score
		}
		if all {
			results = append(results, scored{item, total})
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].score < results[j].score })
	out := make([]T, len(results))
	for i, r := range results {
		out[i] = r.item
	}
	return out
}
