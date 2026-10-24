package tui_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/tools"
	"github.com/iamclancyliang/pi-go/internal/tui"
)

// These follow Pi's autocomplete.test.ts at the pin. Pi's @-mention tests
// run fd; here they run tools.SearchPaths, which stands in for it.

func searchPaths(ctx context.Context, base, query string, limit int) []tui.PathMatch {
	var out []tui.PathMatch
	for _, m := range tools.SearchPaths(ctx, base, query, limit) {
		out = append(out, tui.PathMatch{Path: m.Path, IsDir: m.IsDir})
	}
	return out
}

func suggest(p *tui.CombinedAutocompleteProvider, line string, col int, force bool) *tui.AutocompleteSuggestions {
	return p.Suggestions(context.Background(), []string{line}, 0, col, force)
}

func values(s *tui.AutocompleteSuggestions) []string {
	if s == nil {
		return nil
	}
	var out []string
	for _, item := range s.Items {
		out = append(out, item.Value)
	}
	return out
}

func setupFolder(t *testing.T, base string, dirs []string, files map[string]string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		full := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPathPrefixExtraction(t *testing.T) {
	p := tui.NewCombinedAutocompleteProvider(nil, "/tmp", nil)
	if s := suggest(p, "hey /", 5, true); s == nil || s.Prefix != "/" {
		t.Fatalf("'hey /' forced: %+v", s)
	}
	if s := suggest(p, "/A", 2, true); s != nil && s.Prefix != "/A" {
		t.Fatalf("'/A' forced: prefix %q", s.Prefix)
	}
	if s := suggest(p, "/model", 6, true); s != nil {
		t.Fatalf("a slash command was completed as a path: %+v", s)
	}
	if s := suggest(p, "/command /", 10, true); s == nil || s.Prefix != "/" {
		t.Fatalf("a path argument was not completed: %+v", s)
	}
}

func newMentionTree(t *testing.T) (root, base, outside string) {
	root = t.TempDir()
	base, outside = filepath.Join(root, "cwd"), filepath.Join(root, "outside")
	setupFolder(t, root, []string{"cwd", "outside"}, nil)
	return root, base, outside
}

func mention(t *testing.T, base, line string) []string {
	t.Helper()
	p := tui.NewCombinedAutocompleteProvider(nil, base, searchPaths)
	return values(suggest(p, line, len(line), false))
}

func TestMentionSuggestions(t *testing.T) {
	t.Run("an empty query lists everything", func(t *testing.T) {
		_, base, _ := newMentionTree(t)
		setupFolder(t, base, []string{"src"}, map[string]string{"README.md": "readme"})
		got := mention(t, base, "@")
		sort.Strings(got)
		if !slices.Equal(got, []string{"@README.md", "@src/"}) {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("extension, case and nesting", func(t *testing.T) {
		_, base, _ := newMentionTree(t)
		setupFolder(t, base, []string{"src"}, map[string]string{"file.txt": "c", "README.md": "r", "src/index.ts": "x"})
		if got := mention(t, base, "@file.txt"); !slices.Contains(got, "@file.txt") {
			t.Fatalf("extension: %q", got)
		}
		if got := mention(t, base, "@re"); !slices.Equal(got, []string{"@README.md"}) {
			t.Fatalf("case: %q", got)
		}
		if got := mention(t, base, "@index"); !slices.Contains(got, "@src/index.ts") {
			t.Fatalf("nested: %q", got)
		}
	})
	t.Run("directories rank first", func(t *testing.T) {
		_, base, _ := newMentionTree(t)
		setupFolder(t, base, []string{"src"}, map[string]string{"src.txt": "t"})
		got := mention(t, base, "@src")
		if len(got) == 0 || got[0] != "@src/" || !slices.Contains(got, "@src.txt") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("paths match across directories", func(t *testing.T) {
		_, base, _ := newMentionTree(t)
		setupFolder(t, base, nil, map[string]string{
			"packages/tui/src/autocomplete.ts": "", "packages/ai/src/autocomplete.ts": "",
			"src/components/Button.tsx": "", "src/utils/helpers.ts": "",
		})
		got := mention(t, base, "@tui/src/auto")
		if !slices.Contains(got, "@packages/tui/src/autocomplete.ts") || slices.Contains(got, "@packages/ai/src/autocomplete.ts") {
			t.Fatalf("deep path: %q", got)
		}
		got = mention(t, base, "@components/")
		if !slices.Contains(got, "@src/components/Button.tsx") || slices.Contains(got, "@src/utils/helpers.ts") {
			t.Fatalf("middle directory: %q", got)
		}
	})
	t.Run("a relative directory scopes the search", func(t *testing.T) {
		_, base, outside := newMentionTree(t)
		setupFolder(t, outside, nil, map[string]string{"nested/alpha.ts": "", "nested/deeper/also-alpha.ts": "", "nested/deeper/zzz.ts": ""})
		got := mention(t, base, "@../outside/a")
		if !slices.Contains(got, "@../outside/nested/alpha.ts") || !slices.Contains(got, "@../outside/nested/deeper/also-alpha.ts") ||
			slices.Contains(got, "@../outside/nested/deeper/zzz.ts") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("spaces are quoted", func(t *testing.T) {
		_, base, _ := newMentionTree(t)
		setupFolder(t, base, []string{"my folder"}, map[string]string{"my folder/test.txt": "c"})
		if got := mention(t, base, "@my"); !slices.Contains(got, `@"my folder/"`) {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("hidden entries are found and .git is not", func(t *testing.T) {
		_, base, _ := newMentionTree(t)
		setupFolder(t, base, []string{".pi", ".github", ".git"}, map[string]string{".pi/config.json": "{}", ".github/workflows/ci.yml": "", ".git/config": ""})
		got := mention(t, base, "@")
		if !slices.Contains(got, "@.pi/") || !slices.Contains(got, "@.github/") {
			t.Fatalf("got %q", got)
		}
		for _, v := range got {
			if v == "@.git" || strings.HasPrefix(v, "@.git/") {
				t.Fatalf(".git leaked: %q", got)
			}
		}
	})
	t.Run("symbolic links are followed", func(t *testing.T) {
		_, base, outside := newMentionTree(t)
		setupFolder(t, base, nil, map[string]string{"dir/some_file.txt": "real", "original.txt": "c"})
		setupFolder(t, outside, nil, map[string]string{"some_file.txt": "linked", "nested/file.txt": "x"})
		if err := os.Symlink("../outside", filepath.Join(base, "symlinked_dir")); err != nil {
			t.Skip("no symbolic links here:", err)
		}
		if err := os.Symlink("original.txt", filepath.Join(base, "link.txt")); err != nil {
			t.Fatal(err)
		}
		got := mention(t, base, "@some")
		if !slices.Contains(got, "@dir/some_file.txt") || !slices.Contains(got, "@symlinked_dir/some_file.txt") {
			t.Fatalf("through a link: %q", got)
		}
		if got := mention(t, base, "@symlinked"); !slices.Contains(got, "@symlinked_dir/") {
			t.Fatalf("the link itself: %q", got)
		}
		if got := mention(t, base, "@link"); !slices.Contains(got, "@link.txt") {
			t.Fatalf("a linked file: %q", got)
		}
	})
	t.Run("the working directory's own path does not match", func(t *testing.T) {
		root, _, _ := newMentionTree(t)
		structure := map[string]string{
			"packages/coding-agent/examples/extensions/plan-mode/README.md": "readme",
			"packages/tui/docs/plan.md":                                     "plan",
		}
		normal, inPath := filepath.Join(root, "cwd-normal"), filepath.Join(root, "cwd-plan-repro")
		setupFolder(t, normal, nil, structure)
		setupFolder(t, inPath, nil, structure)
		describe := func(base string) []string {
			p := tui.NewCombinedAutocompleteProvider(nil, base, searchPaths)
			var out []string
			if s := suggest(p, "@plan", 5, false); s != nil {
				for _, item := range s.Items {
					out = append(out, item.Label+" :: "+item.Description)
				}
			}
			sort.Strings(out)
			return out
		}
		a, b := describe(normal), describe(inPath)
		if !slices.Equal(a, b) || !slices.Contains(a, "plan-mode/ :: packages/coding-agent/examples/extensions/plan-mode") ||
			!slices.Contains(a, "plan.md :: packages/tui/docs/plan.md") {
			t.Fatalf("%q vs %q", a, b)
		}
	})
	t.Run("quoted mentions continue and close once", func(t *testing.T) {
		_, base, _ := newMentionTree(t)
		setupFolder(t, base, nil, map[string]string{"my folder/test.txt": "c", "my folder/other.txt": "c"})
		p := tui.NewCombinedAutocompleteProvider(nil, base, searchPaths)
		line := `@"my folder/"`
		got := values(suggest(p, line, len(line)-1, false))
		if !slices.Contains(got, `@"my folder/test.txt"`) || !slices.Contains(got, `@"my folder/other.txt"`) {
			t.Fatalf("got %q", got)
		}
		line = `@"my folder/te"`
		s := suggest(p, line, len(line)-1, false)
		item := findItem(t, s, `@"my folder/test.txt"`)
		lines, _, _ := p.ApplyCompletion([]string{line}, 0, len(line)-1, item, s.Prefix)
		if lines[0] != `@"my folder/test.txt" ` {
			t.Fatalf("applied %q", lines[0])
		}
	})
	t.Run("no finder, no mentions", func(t *testing.T) {
		_, base, _ := newMentionTree(t)
		setupFolder(t, base, nil, map[string]string{"a.txt": ""})
		p := tui.NewCombinedAutocompleteProvider(nil, base, nil)
		if s := suggest(p, "@a", 2, false); s != nil {
			t.Fatalf("got %+v", s)
		}
	})
}

func findItem(t *testing.T, s *tui.AutocompleteSuggestions, value string) tui.AutocompleteItem {
	t.Helper()
	if s != nil {
		for _, item := range s.Items {
			if item.Value == value {
				return item
			}
		}
	}
	t.Fatalf("%q not among %q", value, values(s))
	return tui.AutocompleteItem{}
}

func TestPathCompletion(t *testing.T) {
	base := t.TempDir()
	setupFolder(t, base, []string{"src", "my folder"}, map[string]string{
		"update.sh": "", "utils.ts": "", "src/index.ts": "", "my folder/test.txt": "", "my folder/other.txt": "",
	})
	p := tui.NewCombinedAutocompleteProvider(nil, base, nil)
	if got := values(suggest(p, "./up", 4, true)); !slices.Contains(got, "./update.sh") {
		t.Fatalf("./ file: %q", got)
	}
	if got := values(suggest(p, "./sr", 4, true)); !slices.Contains(got, "./src/") {
		t.Fatalf("./ directory: %q", got)
	}
	if got := values(suggest(p, "my", 2, true)); !slices.Contains(got, `"my folder/"`) {
		t.Fatalf("quoted: %q", got)
	}
	line := `"my folder/"`
	if got := values(suggest(p, line, len(line)-1, true)); !slices.Contains(got, `"my folder/test.txt"`) || !slices.Contains(got, `"my folder/other.txt"`) {
		t.Fatalf("inside quotes: %q", got)
	}
	line = `"my folder/te"`
	s := suggest(p, line, len(line)-1, true)
	lines, _, _ := p.ApplyCompletion([]string{line}, 0, len(line)-1, findItem(t, s, `"my folder/test.txt"`), s.Prefix)
	if lines[0] != `"my folder/test.txt"` {
		t.Fatalf("applied %q", lines[0])
	}
	// Directories come first, then names in order regardless of case — a
	// directory is known by its value's trailing slash, so a quoted one sorts
	// among the files, as in Pi.
	if got := values(suggest(p, "", 0, true)); !slices.Equal(got, []string{"src/", `"my folder/"`, "update.sh", "utils.ts"}) {
		t.Fatalf("order %q", got)
	}
}

func TestSlashCommandSuggestions(t *testing.T) {
	p := tui.NewCombinedAutocompleteProvider([]tui.SlashCommand{
		{Name: "model", Description: "Switch model", ArgumentHint: "<name>"},
		{Name: "help", Description: "Show help"},
		{Name: "hotkeys"},
	}, "/tmp", nil)
	s := suggest(p, "/h", 2, false)
	if s == nil || s.Prefix != "/h" || !slices.Equal(values(s), []string{"help", "hotkeys"}) {
		t.Fatalf("got %+v", s)
	}
	s = suggest(p, "/", 1, false)
	if s.Items[0].Description != "<name> — Switch model" {
		t.Fatalf("description %q", s.Items[0].Description)
	}
	lines, _, col := p.ApplyCompletion([]string{"/he"}, 0, 3, tui.AutocompleteItem{Value: "help", Label: "help"}, "/he")
	if lines[0] != "/help " || col != 6 {
		t.Fatalf("applied %q at %d", lines[0], col)
	}
	if s := suggest(p, "/zzz", 4, false); s != nil {
		t.Fatalf("no match should be nil: %+v", s)
	}
}
