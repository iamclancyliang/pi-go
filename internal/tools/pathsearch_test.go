package tools_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/tools"
)

func searchNames(base, query string, limit int) []string {
	var out []string
	for _, m := range tools.SearchPaths(context.Background(), base, query, limit) {
		name := m.Path
		if m.IsDir {
			name += "/"
		}
		out = append(out, name)
	}
	return out
}

func TestSearchPathsAppliesGitignoreOnlyInARepository(t *testing.T) {
	base := tree(t, map[string]string{".gitignore": "build/\n", "build/out.js": "", "src/a.js": ""})
	if got := searchNames(base, "out", 100); !slices.Contains(got, "build/out.js") {
		t.Fatalf("outside a repository .gitignore was applied: %q", got)
	}
	if err := os.Mkdir(filepath.Join(base, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := searchNames(base, "", 100); slices.Contains(got, "build/out.js") || slices.Contains(got, "build/") || !slices.Contains(got, "src/a.js") {
		t.Fatalf("inside a repository: %q", got)
	}
}

func TestSearchPathsIsCaseSmartAndNearestFirst(t *testing.T) {
	base := tree(t, map[string]string{"Readme.md": "", "deep/er/readme.txt": ""})
	if got := searchNames(base, "readme", 100); !slices.Equal(got, []string{"Readme.md", "deep/er/readme.txt"}) {
		t.Fatalf("lower-case query: %q", got)
	}
	if got := searchNames(base, "Readme", 100); !slices.Equal(got, []string{"Readme.md"}) {
		t.Fatalf("an upper-case letter makes case matter: %q", got)
	}
	if got := searchNames(base, "", 2); len(got) != 2 {
		t.Fatalf("limit: %q", got)
	}
	if got := searchNames(base, "(", 100); got != nil {
		t.Fatalf("an invalid pattern found %q", got)
	}
}
