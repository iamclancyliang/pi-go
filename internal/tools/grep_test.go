package tools_test

import (
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/tools"
)

// TestAMatchNamesItsFileAndLine. Without both, the model cannot go read the
// result, and the search has told it something it cannot act on.
func TestAMatchNamesItsFileAndLine(t *testing.T) {
	root := tree(t, map[string]string{"src/app.go": "package main\n\nfunc target() {}\n"})
	got := call(t, &tools.Grep{Root: root}, `{"pattern":"func target"}`)

	if got != "src/app.go:3: func target() {}" {
		t.Fatalf("a match came back as %q", got)
	}
}

// TestTheSeparatorSaysWhichLineMatched. A block of context in which every line
// looks alike makes the model guess which one it asked about.
func TestTheSeparatorSaysWhichLineMatched(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "one\ntwo\nTARGET\nfour\nfive\n"})
	got := call(t, &tools.Grep{Root: root}, `{"pattern":"TARGET","context":1}`)

	want := "a.txt-2- two\na.txt:3: TARGET\na.txt-4- four"
	if got != want {
		t.Fatalf("context came back as:\n%s\nwant:\n%s", got, want)
	}
}

// TestContextStopsAtTheEndsOfTheFile rather than inventing lines before line
// one or past the addressable end.
//
// A trailing newline does leave one empty last line, and that is not a phantom:
// grep numbers lines the way read addresses them, so line 2 of "TARGET\n" is
// the same line 2 a read offset would land on. The two tools disagreeing about
// what line a file has would be far worse than an empty context row.
func TestContextStopsAtTheEndsOfTheFile(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "one\ntwo\nTARGET\n"})
	got := call(t, &tools.Grep{Root: root}, `{"pattern":"TARGET","context":10}`)

	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "a.txt-0-") || strings.HasPrefix(line, "a.txt-5-") {
			t.Fatalf("context reached a line the file does not have:\n%s", got)
		}
	}
	if !strings.HasPrefix(got, "a.txt-1- one") {
		t.Fatalf("context did not start at line one:\n%s", got)
	}
	if !strings.Contains(got, "a.txt:3: TARGET") {
		t.Fatalf("the matched line is not marked as the match:\n%s", got)
	}
}

// TestAPatternIsARegexUnlessToldOtherwise, and literal must actually escape:
// searching for "a.c" literally must not match "abc".
func TestAPatternIsARegexUnlessToldOtherwise(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "abc\na.c\n"})
	tool := &tools.Grep{Root: root}

	if got := call(t, tool, `{"pattern":"a.c"}`); !strings.Contains(got, "abc") {
		t.Fatalf("a regex did not match as one: %q", got)
	}
	got := call(t, tool, `{"pattern":"a.c","literal":true}`)
	if strings.Contains(got, ": abc") {
		t.Fatalf("a literal search matched a regex expansion: %q", got)
	}
	if !strings.Contains(got, "a.c") {
		t.Fatalf("a literal search missed its exact text: %q", got)
	}
}

// TestIgnoreCaseAppliesToLiteralsToo. The two flags are independent, and a
// literal search that lost case-insensitivity would be a silent surprise.
func TestIgnoreCaseAppliesToLiteralsToo(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "Hello World\n"})
	tool := &tools.Grep{Root: root}

	if got := call(t, tool, `{"pattern":"hello","ignoreCase":true}`); !strings.Contains(got, "Hello") {
		t.Fatalf("case-insensitive regex missed: %q", got)
	}
	got := call(t, tool, `{"pattern":"hello world","ignoreCase":true,"literal":true}`)
	if !strings.Contains(got, "Hello World") {
		t.Fatalf("case-insensitive literal missed: %q", got)
	}
}

// TestABadPatternIsReportedAsAPattern, not as a file that could not be read,
// which would send the model looking at permissions.
func TestABadPatternIsReportedAsAPattern(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "x"})
	_, err := (&tools.Grep{Root: root}).Call(t.Context(), `{"pattern":"([unclosed"}`)
	if err == nil {
		t.Fatal("an invalid regex was accepted")
	}
	if !strings.Contains(err.Error(), "invalid pattern") {
		t.Fatalf("the failure blames the wrong thing: %v", err)
	}
}

// TestTheGlobFiltersWhichFilesAreSearched.
func TestTheGlobFiltersWhichFilesAreSearched(t *testing.T) {
	root := tree(t, map[string]string{
		"a.go":  "needle\n",
		"b.txt": "needle\n",
	})
	got := call(t, &tools.Grep{Root: root}, `{"pattern":"needle","glob":"*.go"}`)
	if !strings.Contains(got, "a.go") {
		t.Fatalf("the glob excluded a file it should match: %q", got)
	}
	if strings.Contains(got, "b.txt") {
		t.Fatalf("the glob did not exclude the other file: %q", got)
	}
}

// TestSearchingIgnoresWhatGitIgnores, for the same reason find does: build
// output buries the matches a model can act on.
func TestSearchingIgnoresWhatGitIgnores(t *testing.T) {
	root := tree(t, map[string]string{
		".gitignore":   "build/\n",
		"src/a.go":     "needle\n",
		"build/gen.go": "needle\n",
	})
	got := call(t, &tools.Grep{Root: root}, `{"pattern":"needle"}`)
	if !strings.Contains(got, "src/a.go") {
		t.Fatalf("a tracked match was hidden: %q", got)
	}
	if strings.Contains(got, "build/gen.go") {
		t.Fatalf("an ignored match was returned: %q", got)
	}
}

// TestSearchingOneFileLabelsItByName: there is no search root to be relative
// to, and the absolute path is noise the model already has.
func TestSearchingOneFileLabelsItByName(t *testing.T) {
	root := tree(t, map[string]string{"deep/dir/a.txt": "needle\n"})
	got := call(t, &tools.Grep{Root: root}, `{"pattern":"needle","path":"deep/dir/a.txt"}`)
	if got != "a.txt:1: needle" {
		t.Fatalf("a single-file search came back as %q", got)
	}
}

// TestAVeryLongLineIsShortenedAndSaysSo. One minified line can be the whole
// output budget, and a fragment quoted without the marker reads as a whole line.
func TestAVeryLongLineIsShortenedAndSaysSo(t *testing.T) {
	root := tree(t, map[string]string{"min.js": "needle" + strings.Repeat("x", 2000) + "\n"})
	got := call(t, &tools.Grep{Root: root}, `{"pattern":"needle"}`)

	if !strings.Contains(got, "... [truncated]") {
		t.Fatalf("a long line came back unmarked: %d bytes", len(got))
	}
	if !strings.Contains(got, "Some lines truncated to 500 chars") {
		t.Fatalf("the notice does not say what happened: %q", got[len(got)-200:])
	}
}

// TestTheMatchLimitSaysHowToSeeMore.
func TestTheMatchLimitSaysHowToSeeMore(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "needle\nneedle\nneedle\nneedle\n"})
	got := call(t, &tools.Grep{Root: root}, `{"pattern":"needle","limit":2}`)

	if n := strings.Count(strings.SplitN(got, "\n\n[", 2)[0], "\n") + 1; n != 2 {
		t.Fatalf("a limit of two returned %d lines: %q", n, got)
	}
	if !strings.Contains(got, "2 matches limit reached") || !strings.Contains(got, "limit=4") {
		t.Fatalf("the notice does not say how to see more: %q", got)
	}
}

// TestMatchingNothingSaysSoToo rather than returning an empty string.
func TestMatchingNothingSaysSoToo(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "nothing here\n"})
	if got := call(t, &tools.Grep{Root: root}, `{"pattern":"needle"}`); got != "No matches found" {
		t.Fatalf("a search that matched nothing returned %q", got)
	}
}

// TestWindowsLineEndingsDoNotShiftLineNumbers, and must not carry a stray
// carriage return into the model's view.
func TestWindowsLineEndingsDoNotShiftLineNumbers(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "one\r\ntwo\r\nTARGET\r\n"})
	got := call(t, &tools.Grep{Root: root}, `{"pattern":"TARGET"}`)
	if got != "a.txt:3: TARGET" {
		t.Fatalf("a CRLF file came back as %q", got)
	}
}

// TestGrepRegisters proves the declared schema survives the registry's check.
func TestGrepRegisters(t *testing.T) {
	if err := tools.NewRegistry().Register(&tools.Grep{Root: t.TempDir()}); err != nil {
		t.Fatalf("grep did not register: %v", err)
	}
}
