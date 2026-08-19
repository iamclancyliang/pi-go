// Package arch holds no product code. It enforces where the framework may be
// named, because a boundary that only a reader can check is a claim rather than a
// constraint.
package arch

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// einoIsAllowedIn lists every directory permitted to name the framework.
//
// The runtime composes the framework, so it may import it. The model port in
// `internal/ai` may not: a port whose signatures carry framework types has hidden
// nothing, it has moved the dependency into every caller that compiles against it.
//
// `spikes/` is not part of the product — it exists to try the framework out, and
// nothing imports it.
var einoIsAllowedIn = []string{
	"internal/runtime",
	"spikes",
}

const einoModule = "github.com/cloudwego/eino"

func TestOnlyTheRuntimeNamesTheFramework(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("repository root: %v", err)
	}

	var offenders []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name := entry.Name(); name == ".git" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if allowed(relative) {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range file.Imports {
			if strings.Contains(imported.Path.Value, einoModule) {
				offenders = append(offenders, relative+" imports "+imported.Path.Value)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}

	for _, offender := range offenders {
		t.Errorf("outside the framework boundary: %s", offender)
	}
	if len(offenders) > 0 {
		t.Log("the framework may be named only in " + strings.Join(einoIsAllowedIn, ", "))
	}
}

func allowed(relative string) bool {
	for _, dir := range einoIsAllowedIn {
		if relative == dir || strings.HasPrefix(relative, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
