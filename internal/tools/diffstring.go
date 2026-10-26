package tools

import (
	"strconv"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/textdiff"
)

// DiffString is the change from oldContent to newContent as the interface
// shows it — Pi's generateDiffString: each changed line with its line
// number, prefixed "+" or "-", and up to contextLines unchanged lines around
// each change, with " ..." where more were left out. It also returns the
// first changed line of the new content, or 0 when nothing changed.
func DiffString(oldContent, newContent string, contextLines int) (string, int) {
	parts := textdiff.Lines(oldContent, newContent)
	width := len(strconv.Itoa(max(strings.Count(oldContent, "\n"), strings.Count(newContent, "\n")) + 1))
	num := func(n int) string { return pad(strconv.Itoa(n), width) }
	ellipsis := " " + strings.Repeat(" ", width) + " ..."

	var out []string
	oldLine, newLine := 1, 1
	lastWasChange := false
	first := 0
	for i, part := range parts {
		lines := strings.Split(part.Value, "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		if part.Added || part.Removed {
			if first == 0 {
				first = newLine
			}
			for _, l := range lines {
				if part.Added {
					out = append(out, "+"+num(newLine)+" "+l)
					newLine++
				} else {
					out = append(out, "-"+num(oldLine)+" "+l)
					oldLine++
				}
			}
			lastWasChange = true
			continue
		}

		context := func(ls []string) {
			for _, l := range ls {
				out = append(out, " "+num(oldLine)+" "+l)
				oldLine++
				newLine++
			}
		}
		skip := func(n int) {
			oldLine += n
			newLine += n
		}
		nextIsChange := i < len(parts)-1 && (parts[i+1].Added || parts[i+1].Removed)
		switch {
		case lastWasChange && nextIsChange:
			if len(lines) <= contextLines*2 {
				context(lines)
			} else {
				context(lines[:contextLines])
				out = append(out, ellipsis)
				skip(len(lines) - 2*contextLines)
				context(lines[len(lines)-contextLines:])
			}
		case lastWasChange:
			shown := lines[:min(contextLines, len(lines))]
			context(shown)
			if skipped := len(lines) - len(shown); skipped > 0 {
				out = append(out, ellipsis)
				skip(skipped)
			}
		case nextIsChange:
			skipped := max(0, len(lines)-contextLines)
			if skipped > 0 {
				out = append(out, ellipsis)
				skip(skipped)
			}
			context(lines[skipped:])
		default:
			skip(len(lines))
		}
		lastWasChange = false
	}
	return strings.Join(out, "\n"), first
}

func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return strings.Repeat(" ", width-len(s)) + s
}
