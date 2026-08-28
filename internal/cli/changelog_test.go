package cli_test

import (
	"os"
	"testing"
)

// TestTheEmbeddedChangelogIsTheRealOne. go:embed cannot reach the repository
// root, so the package holds a copy — and a copy that can drift silently would
// eventually show users a changelog the repository has moved past. This turns
// drift into a failing gate: whoever edits one file is told about the other.
func TestTheEmbeddedChangelogIsTheRealOne(t *testing.T) {
	root, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatalf("reading the root changelog: %v", err)
	}
	embedded, err := os.ReadFile("CHANGELOG.md")
	if err != nil {
		t.Fatalf("reading the embedded copy: %v", err)
	}
	if string(root) != string(embedded) {
		t.Fatal("internal/cli/CHANGELOG.md has drifted from CHANGELOG.md; copy the root file over it")
	}
}
