package interactive

import (
	"regexp"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/textdiff"
)

var diffLine = regexp.MustCompile(`^([+\-\s])(\s*\d*)\s(.*)$`)

type parsedDiffLine struct{ prefix, num, content string }

func parseDiffLine(line string) (parsedDiffLine, bool) {
	m := diffLine.FindStringSubmatch(line)
	if m == nil {
		return parsedDiffLine{}, false
	}
	return parsedDiffLine{m[1], m[2], m[3]}, true
}

// renderDiff colours an edit's diff — Pi's renderDiff: context grey,
// removals red, additions green, and when one line was changed into one
// other, the words that changed in reverse video.
func renderDiff(t *Theme, diff string) string {
	lines := strings.Split(diff, "\n")
	var out []string
	for i := 0; i < len(lines); {
		p, ok := parseDiffLine(lines[i])
		if !ok {
			out = append(out, t.Fg("toolDiffContext", lines[i]))
			i++
			continue
		}
		switch p.prefix {
		case "-":
			var removed, added []parsedDiffLine
			for i < len(lines) {
				q, ok := parseDiffLine(lines[i])
				if !ok || q.prefix != "-" {
					break
				}
				removed = append(removed, q)
				i++
			}
			for i < len(lines) {
				q, ok := parseDiffLine(lines[i])
				if !ok || q.prefix != "+" {
					break
				}
				added = append(added, q)
				i++
			}
			if len(removed) == 1 && len(added) == 1 {
				oldLine, newLine := intraLineDiff(replaceTabs(removed[0].content), replaceTabs(added[0].content))
				out = append(out, t.Fg("toolDiffRemoved", "-"+removed[0].num+" "+oldLine))
				out = append(out, t.Fg("toolDiffAdded", "+"+added[0].num+" "+newLine))
				continue
			}
			for _, r := range removed {
				out = append(out, t.Fg("toolDiffRemoved", "-"+r.num+" "+replaceTabs(r.content)))
			}
			for _, a := range added {
				out = append(out, t.Fg("toolDiffAdded", "+"+a.num+" "+replaceTabs(a.content)))
			}
		case "+":
			out = append(out, t.Fg("toolDiffAdded", "+"+p.num+" "+replaceTabs(p.content)))
			i++
		default:
			out = append(out, t.Fg("toolDiffContext", " "+p.num+" "+replaceTabs(p.content)))
			i++
		}
	}
	return strings.Join(out, "\n")
}

// intraLineDiff marks the words that differ between two versions of a line,
// leaving the indentation before the first change unmarked.
func intraLineDiff(oldContent, newContent string) (string, string) {
	var removed, added strings.Builder
	firstRemoved, firstAdded := true, true
	mark := func(b *strings.Builder, first *bool, value string) {
		if *first {
			lead := value[:len(value)-len(strings.TrimLeftFunc(value, isJSSpace))]
			value = value[len(lead):]
			b.WriteString(lead)
			*first = false
		}
		if value != "" {
			b.WriteString(Inverse(value))
		}
	}
	for _, part := range textdiff.Words(oldContent, newContent) {
		switch {
		case part.Removed:
			mark(&removed, &firstRemoved, part.Value)
		case part.Added:
			mark(&added, &firstAdded, part.Value)
		default:
			removed.WriteString(part.Value)
			added.WriteString(part.Value)
		}
	}
	return removed.String(), added.String()
}

func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}
