package tools_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/tools"
)

// tree builds a directory from a path->content map, creating parents.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

func lines(s string) []string {
	body := strings.SplitN(s, "\n\n[", 2)[0]
	if body == "" {
		return nil
	}
	out := strings.Split(body, "\n")
	return out
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// TestAPatternWithoutASlashMatchesAtAnyDepth. `*.go` meaning only the top level
// would make the tool useless on any real tree, and it is the pattern a model
// reaches for first.
func TestAPatternWithoutASlashMatchesAtAnyDepth(t *testing.T) {
	root := tree(t, map[string]string{
		"main.go":         "x",
		"src/deep/app.go": "x",
		"README.md":       "x",
	})
	got := lines(call(t, &tools.Find{Root: root}, `{"pattern":"*.go"}`))

	if len(got) != 2 || !contains(got, "main.go") || !contains(got, "src/deep/app.go") {
		t.Fatalf("*.go matched %v", got)
	}
}

// TestAPatternWithASlashMatchesThePath, with an implicit prefix so a model's
// `src/**/*_test.go` reaches below the first level.
func TestAPatternWithASlashMatchesThePath(t *testing.T) {
	root := tree(t, map[string]string{
		"src/a/b/x_test.go": "x",
		"src/y_test.go":     "x",
		"other/z_test.go":   "x",
	})
	got := lines(call(t, &tools.Find{Root: root}, `{"pattern":"src/**/*_test.go"}`))

	if !contains(got, "src/a/b/x_test.go") || !contains(got, "src/y_test.go") {
		t.Fatalf("the pattern missed something it should match: %v", got)
	}
	if contains(got, "other/z_test.go") {
		t.Fatalf("the pattern matched outside src: %v", got)
	}
}

// TestIgnoredFilesStayOut is the behaviour that makes the tool usable: without
// it a search of any real repository returns build output and dependencies.
func TestIgnoredFilesStayOut(t *testing.T) {
	root := tree(t, map[string]string{
		".gitignore":          "build/\n*.tmp\n",
		"keep.go":             "x",
		"build/generated.go":  "x",
		"scratch.tmp":         "x",
		"src/nested/keep2.go": "x",
	})
	got := lines(call(t, &tools.Find{Root: root}, `{"pattern":"**/*"}`))

	if !contains(got, "keep.go") || !contains(got, "src/nested/keep2.go") {
		t.Fatalf("a tracked file was hidden: %v", got)
	}
	for _, hidden := range []string{"build/generated.go", "scratch.tmp"} {
		if contains(got, hidden) {
			t.Fatalf("%s was ignored by .gitignore but returned: %v", hidden, got)
		}
	}
}

// TestANestedIgnoreAppliesBelowItselfOnly. A rule in a subdirectory that leaked
// upward would hide files its author never named.
func TestANestedIgnoreAppliesBelowItselfOnly(t *testing.T) {
	root := tree(t, map[string]string{
		"notes.txt":            "x",
		"sub/.gitignore":       "notes.txt\n",
		"sub/notes.txt":        "x",
		"sub/deeper/notes.txt": "x",
	})
	got := lines(call(t, &tools.Find{Root: root}, `{"pattern":"**/notes.txt"}`))

	if !contains(got, "notes.txt") {
		t.Fatalf("a nested rule hid a file above it: %v", got)
	}
	if contains(got, "sub/notes.txt") || contains(got, "sub/deeper/notes.txt") {
		t.Fatalf("the nested rule did not apply below itself: %v", got)
	}
}

// TestNegationReInclues covers the last-match-wins rule, which is what makes
// `!keep.log` after `*.log` mean anything.
func TestNegationReIncludes(t *testing.T) {
	root := tree(t, map[string]string{
		".gitignore": "*.log\n!keep.log\n",
		"drop.log":   "x",
		"keep.log":   "x",
	})
	got := lines(call(t, &tools.Find{Root: root}, `{"pattern":"*.log"}`))

	if contains(got, "drop.log") {
		t.Fatalf("an ignored file was returned: %v", got)
	}
	if !contains(got, "keep.log") {
		t.Fatalf("a negated rule did not re-include its file: %v", got)
	}
}

// TestDotfilesAreFound: they are where configuration lives, and a searcher that
// hides them reports that a file it was asked about does not exist.
func TestDotfilesAreFound(t *testing.T) {
	root := tree(t, map[string]string{".env.example": "x", "src/.keep": "x"})
	got := lines(call(t, &tools.Find{Root: root}, `{"pattern":"**/*"}`))
	if !contains(got, ".env.example") || !contains(got, "src/.keep") {
		t.Fatalf("hidden files were not searched: %v", got)
	}
}

// TestGitAndNodeModulesAreNeverWalked. Skipping them by rule still pays to walk
// them, and both are large enough for that to be the whole cost of the search.
func TestGitAndNodeModulesAreNeverWalked(t *testing.T) {
	root := tree(t, map[string]string{
		".git/objects/abc":      "x",
		"node_modules/pkg/i.js": "x",
		"src/app.js":            "x",
	})
	got := lines(call(t, &tools.Find{Root: root}, `{"pattern":"**/*"}`))
	if len(got) != 1 || got[0] != "src/app.js" {
		t.Fatalf("the search descended where it should not: %v", got)
	}
}

// TestMatchingNothingSaysSo rather than returning an empty string, which reads
// as a tool that failed quietly.
func TestMatchingNothingSaysSo(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "x"})
	if got := call(t, &tools.Find{Root: root}, `{"pattern":"*.go"}`); got != "No files found matching pattern" {
		t.Fatalf("a search that matched nothing returned %q", got)
	}
}

// TestTheResultLimitIsReported so a model knows the list is partial.
func TestTheResultLimitIsReported(t *testing.T) {
	files := map[string]string{}
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		files[n+".go"] = "x"
	}
	root := tree(t, files)
	got := call(t, &tools.Find{Root: root}, `{"pattern":"*.go","limit":2}`)

	if len(lines(got)) != 2 {
		t.Fatalf("a limit of two returned %v", lines(got))
	}
	if !strings.Contains(got, "2 results limit reached") {
		t.Fatalf("the notice does not say the limit was reached: %q", got)
	}
}

// TestFindStopsWhenCancelled: a walk of a large tree must observe a cancelled
// call rather than finishing it.
func TestFindStopsWhenCancelled(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "x"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&tools.Find{Root: root}).Call(ctx, `{"pattern":"**/*"}`); err == nil {
		t.Fatal("a cancelled search ran to completion")
	}
}

// TestFindRegisters proves the declared schema survives the registry's check.
func TestFindRegisters(t *testing.T) {
	if err := tools.NewRegistry().Register(&tools.Find{Root: t.TempDir()}); err != nil {
		t.Fatalf("find did not register: %v", err)
	}
}
