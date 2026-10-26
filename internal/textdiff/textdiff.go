// Package textdiff compares texts by lines and by words, as the jsdiff
// library does — version 8.0.4, which Pi uses for the diffs its edit tool
// reports and its interface draws. Ported rather than replaced by another
// diff, because where a change could be aligned more than one way, which
// way is chosen shows in every diff a user reads.
//
// Ported from jsdiff (https://github.com/kpdecker/jsdiff), under its
// licence:
//
// Copyright (c) 2009-2015, Kevin Decker <kpdecker@gmail.com>
// All rights reserved.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are
// met:
//
//  1. Redistributions of source code must retain the above copyright
//     notice, this list of conditions and the following disclaimer.
//  2. Redistributions in binary form must reproduce the above copyright
//     notice, this list of conditions and the following disclaimer in the
//     documentation and/or other materials provided with the distribution.
//  3. Neither the name of the copyright holder nor the names of its
//     contributors may be used to endorse or promote products derived from
//     this software without specific prior written permission.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS
// IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO,
// THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR
// PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR
// CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL,
// EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO,
// PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR
// PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF
// LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING
// NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
// SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
package textdiff

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Change is one run of a diff: text both sides share, or text only the old
// (Removed) or only the new (Added) side has.
type Change struct {
	Value   string `json:"value"`
	Count   int    `json:"count"`
	Added   bool   `json:"added"`
	Removed bool   `json:"removed"`
}

type component struct {
	count          int
	added, removed bool
	previous       *component
}

type path struct {
	oldPos int
	last   *component
}

type differ struct {
	tokenize func(string) []string
	equals   func(a, b string) bool
	join     func([]string) string
}

func (d differ) diff(oldText, newText string) []Change {
	return d.diffTokens(removeEmpty(d.tokenize(oldText)), removeEmpty(d.tokenize(newText)))
}

func removeEmpty(tokens []string) []string {
	out := tokens[:0:0]
	for _, t := range tokens {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// diffTokens is Myers' algorithm as jsdiff implements it, including its
// preference, between two equally good paths, for the one further along
// the old text, and its pruning of diagonals past the graph's edge.
func (d differ) diffTokens(oldTokens, newTokens []string) []Change {
	newLen, oldLen := len(newTokens), len(oldTokens)
	maxEdit := newLen + oldLen
	offset := maxEdit + 1
	best := make([]*path, 2*offset+1)
	at := func(k int) *path { return best[k+offset] }
	set := func(k int, p *path) { best[k+offset] = p }

	set(0, &path{oldPos: -1})
	newPos := d.extractCommon(at(0), newTokens, oldTokens, 0)
	if at(0).oldPos+1 >= oldLen && newPos+1 >= newLen {
		return d.buildValues(at(0).last, newTokens, oldTokens)
	}

	minDiagonal, maxDiagonal := -(1 << 30), 1<<30
	for edit := 1; edit <= maxEdit; edit++ {
		for k := max(minDiagonal, -edit); k <= min(maxDiagonal, edit); k += 2 {
			removePath, addPath := at(k-1), at(k+1)
			if removePath != nil {
				set(k-1, nil)
			}
			canAdd := false
			if addPath != nil {
				addNewPos := addPath.oldPos - k
				canAdd = 0 <= addNewPos && addNewPos < newLen
			}
			canRemove := removePath != nil && removePath.oldPos+1 < oldLen
			if !canAdd && !canRemove {
				set(k, nil)
				continue
			}
			var base *path
			if !canRemove || (canAdd && removePath.oldPos < addPath.oldPos) {
				base = addToPath(addPath, true, false, 0)
			} else {
				base = addToPath(removePath, false, true, 1)
			}
			newPos = d.extractCommon(base, newTokens, oldTokens, k)
			if base.oldPos+1 >= oldLen && newPos+1 >= newLen {
				return d.buildValues(base.last, newTokens, oldTokens)
			}
			set(k, base)
			if base.oldPos+1 >= oldLen {
				maxDiagonal = min(maxDiagonal, k-1)
			}
			if newPos+1 >= newLen {
				minDiagonal = max(minDiagonal, k+1)
			}
		}
	}
	return nil
}

func addToPath(p *path, added, removed bool, oldPosInc int) *path {
	last := p.last
	if last != nil && last.added == added && last.removed == removed {
		return &path{oldPos: p.oldPos + oldPosInc, last: &component{count: last.count + 1, added: added, removed: removed, previous: last.previous}}
	}
	return &path{oldPos: p.oldPos + oldPosInc, last: &component{count: 1, added: added, removed: removed, previous: last}}
}

func (d differ) extractCommon(base *path, newTokens, oldTokens []string, k int) int {
	oldPos := base.oldPos
	newPos := oldPos - k
	common := 0
	for newPos+1 < len(newTokens) && oldPos+1 < len(oldTokens) && d.equals(oldTokens[oldPos+1], newTokens[newPos+1]) {
		newPos++
		oldPos++
		common++
	}
	if common > 0 {
		base.last = &component{count: common, previous: base.last}
	}
	base.oldPos = oldPos
	return newPos
}

func (d differ) buildValues(last *component, newTokens, oldTokens []string) []Change {
	var components []*component
	for c := last; c != nil; c = c.previous {
		components = append(components, c)
	}
	changes := make([]Change, len(components))
	newPos, oldPos := 0, 0
	for i := range components {
		c := components[len(components)-1-i]
		ch := Change{Count: c.count, Added: c.added, Removed: c.removed}
		if !c.removed {
			ch.Value = d.join(newTokens[newPos : newPos+c.count])
			newPos += c.count
			if !c.added {
				oldPos += c.count
			}
		} else {
			ch.Value = d.join(oldTokens[oldPos : oldPos+c.count])
			oldPos += c.count
		}
		changes[i] = ch
	}
	return changes
}

var lineSplit = regexp.MustCompile(`\r?\n`)

// tokenizeLines splits text into lines, each keeping its line break.
func tokenizeLines(text string) []string {
	var parts []string
	last := 0
	for _, m := range lineSplit.FindAllStringIndex(text, -1) {
		parts = append(parts, text[last:m[0]], text[m[0]:m[1]])
		last = m[1]
	}
	parts = append(parts, text[last:])
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	var lines []string
	for i, p := range parts {
		if i%2 == 1 {
			lines[len(lines)-1] += p
		} else {
			lines = append(lines, p)
		}
	}
	return lines
}

var lineDiffer = differ{
	tokenize: tokenizeLines,
	equals:   func(a, b string) bool { return a == b },
	join:     func(tokens []string) string { return strings.Join(tokens, "") },
}

// Lines compares two texts line by line — jsdiff's diffLines.
func Lines(oldText, newText string) []Change { return lineDiffer.diff(oldText, newText) }

// isWordChar is jsdiff's extended word character: ASCII letters, digits and
// underscore, the soft hyphen, and the Latin letters of Latin-1 and the
// Latin Extended blocks, without the few symbols among them.
func isWordChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == 0xAD:
		return true
	case r >= 0xC0 && r <= 0xD6, r >= 0xD8 && r <= 0xF6, r >= 0xF8 && r <= 0x2C6,
		r >= 0x2C8 && r <= 0x2D7, r >= 0x2DE && r <= 0x2FF, r >= 0x1E00 && r <= 0x1EFF:
		return true
	}
	return false
}

// jsSpace is JavaScript's \s.
func jsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func hasSpace(s string) bool { return strings.IndexFunc(s, jsSpace) >= 0 }

// wordParts splits text into runs of word characters, runs of whitespace,
// and single other characters.
func wordParts(text string) []string {
	var parts []string
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		j := i + size
		switch {
		case isWordChar(r):
			for j < len(text) {
				r2, s2 := utf8.DecodeRuneInString(text[j:])
				if !isWordChar(r2) {
					break
				}
				j += s2
			}
		case jsSpace(r):
			for j < len(text) {
				r2, s2 := utf8.DecodeRuneInString(text[j:])
				if !jsSpace(r2) {
					break
				}
				j += s2
			}
		}
		parts = append(parts, text[i:j])
		i = j
	}
	return parts
}

// tokenizeWords makes tokens of words and punctuation, each carrying the
// whitespace around it.
func tokenizeWords(text string) []string {
	var tokens []string
	prev := ""
	hasPrev := false
	for _, part := range wordParts(text) {
		switch {
		case hasSpace(part):
			if !hasPrev {
				tokens = append(tokens, part)
			} else {
				tokens[len(tokens)-1] += part
			}
		case hasPrev && hasSpace(prev):
			if tokens[len(tokens)-1] == prev {
				tokens[len(tokens)-1] += part
			} else {
				tokens = append(tokens, prev+part)
			}
		default:
			tokens = append(tokens, part)
		}
		prev, hasPrev = part, true
	}
	return tokens
}

func trimJS(s string) string { return strings.TrimFunc(s, jsSpace) }

var wordDiffer = differ{
	tokenize: tokenizeWords,
	equals:   func(a, b string) bool { return trimJS(a) == trimJS(b) },
	join: func(tokens []string) string {
		var b strings.Builder
		for i, t := range tokens {
			if i > 0 {
				t = strings.TrimLeftFunc(t, jsSpace)
			}
			b.WriteString(t)
		}
		return b.String()
	},
}

// Words compares two texts word by word, whitespace riding along with the
// words — jsdiff's diffWords.
func Words(oldText, newText string) []Change {
	changes := wordDiffer.diff(oldText, newText)
	var lastKeep, insertion, deletion *Change
	for i := range changes {
		c := &changes[i]
		switch {
		case c.Added:
			insertion = c
		case c.Removed:
			deletion = c
		default:
			if insertion != nil || deletion != nil {
				dedupeWhitespace(lastKeep, deletion, insertion, c)
			}
			lastKeep, insertion, deletion = c, nil, nil
		}
	}
	if insertion != nil || deletion != nil {
		dedupeWhitespace(lastKeep, deletion, insertion, nil)
	}
	return changes
}

func leadingWs(s string) string { return s[:len(s)-len(strings.TrimLeftFunc(s, jsSpace))] }

func trailingWs(s string) string { return s[len(strings.TrimRightFunc(s, jsSpace)):] }

func commonPrefix(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return a[:i]
}

func commonSuffix(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[len(a)-1-i] == b[len(b)-1-i] {
		i++
	}
	return a[len(a)-i:]
}

func replacePrefix(s, oldPrefix, newPrefix string) string {
	return newPrefix + strings.TrimPrefix(s, oldPrefix)
}

func replaceSuffix(s, oldSuffix, newSuffix string) string {
	return strings.TrimSuffix(s, oldSuffix) + newSuffix
}

// maximumOverlap is the longest suffix of a that is a prefix of b.
func maximumOverlap(a, b string) string {
	for n := min(len(a), len(b)); n > 0; n-- {
		if a[len(a)-n:] == b[:n] {
			return b[:n]
		}
	}
	return ""
}

// dedupeWhitespace tidies the whitespace a word diff leaves duplicated
// between neighbouring changes, as jsdiff does after diffing words.
func dedupeWhitespace(startKeep, deletion, insertion, endKeep *Change) {
	switch {
	case deletion != nil && insertion != nil:
		oldPrefix, oldSuffix := leadingWs(deletion.Value), trailingWs(deletion.Value)
		newPrefix, newSuffix := leadingWs(insertion.Value), trailingWs(insertion.Value)
		if startKeep != nil {
			common := commonPrefix(oldPrefix, newPrefix)
			startKeep.Value = replaceSuffix(startKeep.Value, newPrefix, common)
			deletion.Value = strings.TrimPrefix(deletion.Value, common)
			insertion.Value = strings.TrimPrefix(insertion.Value, common)
		}
		if endKeep != nil {
			common := commonSuffix(oldSuffix, newSuffix)
			endKeep.Value = replacePrefix(endKeep.Value, newSuffix, common)
			deletion.Value = strings.TrimSuffix(deletion.Value, common)
			insertion.Value = strings.TrimSuffix(insertion.Value, common)
		}
	case insertion != nil:
		if startKeep != nil {
			insertion.Value = strings.TrimLeftFunc(insertion.Value, jsSpace)
		}
		if endKeep != nil {
			endKeep.Value = strings.TrimLeftFunc(endKeep.Value, jsSpace)
		}
	case startKeep != nil && endKeep != nil:
		newWsFull := leadingWs(endKeep.Value)
		delStart, delEnd := leadingWs(deletion.Value), trailingWs(deletion.Value)
		newWsStart := commonPrefix(newWsFull, delStart)
		deletion.Value = strings.TrimPrefix(deletion.Value, newWsStart)
		newWsEnd := commonSuffix(strings.TrimPrefix(newWsFull, newWsStart), delEnd)
		deletion.Value = strings.TrimSuffix(deletion.Value, newWsEnd)
		endKeep.Value = replacePrefix(endKeep.Value, newWsFull, newWsEnd)
		startKeep.Value = replaceSuffix(startKeep.Value, newWsFull, newWsFull[:len(newWsFull)-len(newWsEnd)])
	case endKeep != nil:
		overlap := maximumOverlap(trailingWs(deletion.Value), leadingWs(endKeep.Value))
		deletion.Value = strings.TrimSuffix(deletion.Value, overlap)
	case startKeep != nil:
		overlap := maximumOverlap(trailingWs(startKeep.Value), leadingWs(deletion.Value))
		deletion.Value = strings.TrimPrefix(deletion.Value, overlap)
	}
}
