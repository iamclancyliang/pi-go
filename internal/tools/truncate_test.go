package tools_test

import (
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/tools"
)

// TestOutputUnderBothLimitsIsUntouched: truncation must not alter what fits.
func TestOutputUnderBothLimitsIsUntouched(t *testing.T) {
	content := "one\ntwo\nthree"
	got := tools.TruncateHead(content, tools.Limits{})
	if got.Truncated || got.Content != content {
		t.Fatalf("content that fits came back as %+v", got)
	}
	if got.TotalLines != 3 || got.OutputLines != 3 {
		t.Fatalf("three lines counted as %d total, %d shown", got.TotalLines, got.OutputLines)
	}
}

// TestATrailingNewlineDoesNotInventALine pins the count the continuation
// notices quote back. A file of ten lines reported as eleven sends the model to
// an offset that reads nothing.
func TestATrailingNewlineDoesNotInventALine(t *testing.T) {
	got := tools.TruncateHead("one\ntwo\n", tools.Limits{})
	if got.TotalLines != 2 {
		t.Fatalf("two lines and a trailing newline counted as %d", got.TotalLines)
	}
}

// TestTheLineLimitStopsAtWholeLines covers the limit reached first when lines
// are short.
func TestTheLineLimitStopsAtWholeLines(t *testing.T) {
	content := strings.Repeat("x\n", 100)
	got := tools.TruncateHead(content, tools.Limits{MaxLines: 10})
	if !got.Truncated || got.By != tools.TruncatedByLines {
		t.Fatalf("a hundred lines under a ten-line limit came back as %+v", got)
	}
	if got.OutputLines != 10 {
		t.Fatalf("a ten-line limit produced %d lines", got.OutputLines)
	}
	if lines := strings.Split(got.Content, "\n"); len(lines) != 10 {
		t.Fatalf("the content holds %d lines, not 10", len(lines))
	}
}

// TestTheByteLimitNeverCutsALineInHalf is the property the whole design rests
// on: a model shown half a line may quote it back as though it were whole.
func TestTheByteLimitNeverCutsALineInHalf(t *testing.T) {
	content := strings.Repeat("aaaaaaaaa\n", 100) // ten bytes per line
	got := tools.TruncateHead(content, tools.Limits{MaxBytes: 35})
	if !got.Truncated || got.By != tools.TruncatedByBytes {
		t.Fatalf("output over the byte budget came back as %+v", got)
	}
	for _, line := range strings.Split(got.Content, "\n") {
		if line != "aaaaaaaaa" {
			t.Fatalf("a line came back cut: %q", line)
		}
	}
	// The joined output is what the budget bounds, newlines included. Counting
	// only the lines lets the thing actually sent exceed the limit.
	if len(got.Content) > 35 {
		t.Fatalf("output is %d bytes, over the 35-byte budget: %q", len(got.Content), got.Content)
	}
}

// TestNothingWholeFitsIsSaidOutright: no offset helps here, so the caller must
// be able to tell this from an ordinary truncation.
func TestNothingWholeFitsIsSaidOutright(t *testing.T) {
	got := tools.TruncateHead(strings.Repeat("x", 500)+"\nnext", tools.Limits{MaxBytes: 100})
	if !got.FirstLineExceedsLimit {
		t.Fatalf("a first line over the budget came back as %+v", got)
	}
	if got.Content != "" {
		t.Fatalf("nothing whole fit, yet content is %q", got.Content)
	}
	if got.OutputLines != 0 {
		t.Fatalf("nothing whole fit, yet %d lines were reported shown", got.OutputLines)
	}
}

// TestEmptyOutputIsNotTruncated guards the boundary where there is nothing to
// count.
func TestEmptyOutputIsNotTruncated(t *testing.T) {
	got := tools.TruncateHead("", tools.Limits{})
	if got.Truncated || got.TotalLines != 0 || got.Content != "" {
		t.Fatalf("empty output came back as %+v", got)
	}
}

// TestFormatSizeIsWhatTheNoticesQuote pins the spellings a model is shown.
func TestFormatSizeIsWhatTheNoticesQuote(t *testing.T) {
	for bytes, want := range map[int]string{
		0:           "0B",
		1023:        "1023B",
		1024:        "1.0KB",
		51200:       "50.0KB",
		1024 * 1024: "1.0MB",
	} {
		if got := tools.FormatSize(bytes); got != want {
			t.Fatalf("%d bytes rendered %q, want %q", bytes, got, want)
		}
	}
}
