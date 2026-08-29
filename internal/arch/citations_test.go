package arch_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEveryTestCitedByTheParityMatrixExists.
//
// The parity matrix's whole value is that its acceptance evidence can be run.
// A renamed or deleted test leaves a citation pointing at nothing, and the
// matrix then claims evidence that cannot be produced — which is worse than
// claiming none, because a reader has no reason to doubt it. The audit at
// docs/research/provider-contract-source-audit.md found citation errors in
// this repository before; this makes that class of error a failing gate.
func TestEveryTestCitedByTheParityMatrixExists(t *testing.T) {
	root := repoRoot(t)
	matrix, err := os.ReadFile(filepath.Join(root, "docs", "product", "parity-matrix.md"))
	if err != nil {
		t.Fatalf("reading the parity matrix: %v", err)
	}

	// Cited as `TestSomething` in backticks, which is how the matrix writes
	// them and how a reader would search for one.
	cited := regexp.MustCompile("`(Test[A-Za-z0-9_]+)`").FindAllStringSubmatch(string(matrix), -1)
	if len(cited) == 0 {
		t.Fatal("the parity matrix cites no tests; its rows carry no runnable evidence")
	}

	declared := declaredTests(t, root)
	var missing []string
	for _, match := range cited {
		if !declared[match[1]] {
			missing = append(missing, match[1])
		}
	}
	if len(missing) > 0 {
		t.Fatalf("the parity matrix cites tests that do not exist: %s\n"+
			"rename the citation or restore the test; a row whose evidence cannot be run claims more than it has",
			strings.Join(missing, ", "))
	}
}

// declaredTests is every Test function in the repository, by name.
func declaredTests(t *testing.T, root string) map[string]bool {
	t.Helper()
	out, err := exec.Command("grep", "-rhoE", `^func (Test[A-Za-z0-9_]+)`,
		filepath.Join(root, "internal"), filepath.Join(root, "conformance")).Output()
	if err != nil {
		t.Fatalf("listing tests: %v", err)
	}
	declared := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if name := strings.TrimPrefix(strings.TrimSpace(line), "func "); name != "" {
			declared[name] = true
		}
	}
	return declared
}

// repoRoot walks up from the test's directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}
