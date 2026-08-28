package tools

import (
	"fmt"
	"strings"
)

// The limits a tool's output is held to.
//
// Two independent limits, and whichever is reached first wins. A line count
// alone lets one enormous line through; a byte budget alone cuts a file of
// short lines at an arbitrary point. Both are needed to bound what a model is
// shown without ever handing it half a line.
const (
	DefaultMaxLines = 2000
	DefaultMaxBytes = 50 * 1024
)

// Truncation is what holding output to those limits produced.
//
// The counts are carried rather than recomputed by a caller: the notice a tool
// appends has to name the exact line it stopped at, and a second count derived
// from the truncated text cannot know how many lines the original had.
type Truncation struct {
	// Content is the output, always a whole number of lines.
	Content string

	// Truncated says whether anything was withheld.
	Truncated bool

	// By names the limit that was reached, and is empty when nothing was.
	By TruncatedBy

	TotalLines  int
	TotalBytes  int
	OutputLines int
	OutputBytes int

	// FirstLineExceedsLimit says the first line alone is over the byte budget,
	// so no whole line could be shown at all. A caller must say something other
	// than "here is the start of the file", because none of it is here.
	FirstLineExceedsLimit bool

	MaxLines int
	MaxBytes int
}

// TruncatedBy names which limit stopped the output.
type TruncatedBy string

const (
	TruncatedByLines TruncatedBy = "lines"
	TruncatedByBytes TruncatedBy = "bytes"
)

// Limits are the bounds one truncation is held to. A zero field takes the
// default, so a caller that cares about only one of them says only that one.
type Limits struct {
	MaxLines int
	MaxBytes int
}

func (l Limits) resolve() (int, int) {
	maxLines, maxBytes := l.MaxLines, l.MaxBytes
	if maxLines <= 0 {
		maxLines = DefaultMaxLines
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return maxLines, maxBytes
}

// countableLines splits content the way the limits count it.
//
// A trailing newline ends the last line rather than starting an empty one:
// counting the empty remainder as a line would report a file of ten lines as
// having eleven, and the notices a tool appends quote that number back to the
// model as a line to continue from.
func countableLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if strings.HasSuffix(content, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// TruncateHead keeps the beginning of content, within both limits.
//
// Whole lines only. A file is read from the top, so what a model needs is the
// start intact rather than as much text as fits — a final line cut mid-token is
// something it may quote back as though it were complete.
func TruncateHead(content string, limits Limits) Truncation {
	maxLines, maxBytes := limits.resolve()
	lines := countableLines(content)
	result := Truncation{
		TotalLines: len(lines),
		TotalBytes: len(content),
		MaxLines:   maxLines,
		MaxBytes:   maxBytes,
	}

	if result.TotalLines <= maxLines && result.TotalBytes <= maxBytes {
		result.Content = content
		result.OutputLines = result.TotalLines
		result.OutputBytes = result.TotalBytes
		return result
	}

	// Nothing whole fits. Reported rather than solved here, because what to say
	// instead is the calling tool's to decide.
	if len(lines[0]) > maxBytes {
		result.Truncated = true
		result.By = TruncatedByBytes
		result.FirstLineExceedsLimit = true
		return result
	}

	kept := make([]string, 0, maxLines)
	bytesKept := 0
	by := TruncatedByLines
	for i, line := range lines {
		if i >= maxLines {
			break
		}
		// The newline that joins this line to the one before it is part of what
		// keeping it costs. Counting only the lines themselves lets the joined
		// output exceed a budget every line was checked against.
		cost := len(line)
		if i > 0 {
			cost++
		}
		if bytesKept+cost > maxBytes {
			by = TruncatedByBytes
			break
		}
		kept = append(kept, line)
		bytesKept += cost
	}
	if len(kept) >= maxLines && bytesKept <= maxBytes {
		by = TruncatedByLines
	}

	result.Content = strings.Join(kept, "\n")
	result.Truncated = true
	result.By = by
	result.OutputLines = len(kept)
	result.OutputBytes = len(result.Content)
	return result
}

// FormatSize renders a byte count the way the notices quote it back.
func FormatSize(bytes int) string {
	switch {
	case bytes < 1024:
		return fmt.Sprintf("%dB", bytes)
	case bytes < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	}
}

// GrepMaxLineLength bounds one matched line.
//
// Separate from the output budget because it solves a different problem: one
// minified bundle line can be the whole budget on its own, and a search that
// spends its output on a single unreadable line has reported nothing useful
// about the other matches.
const GrepMaxLineLength = 500

// TruncateLine shortens one line, saying so in the line itself.
//
// The marker is part of the text rather than a flag beside it, because the line
// is going to the model as text: a caller that quotes a shortened line without
// the marker is presenting a fragment as though it were the whole line.
func TruncateLine(line string) (string, bool) {
	if len(line) <= GrepMaxLineLength {
		return line, false
	}
	return line[:GrepMaxLineLength] + "... [truncated]", true
}
