package conformance

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestTheObligationsDocNamesRealTests.
//
// The obligations list is only worth anything if every line still points at a
// control that exists. A document naming a deleted or renamed test reads as
// coverage that is not there — which is worse than no document, because it
// invites someone to trust it.
func TestTheObligationsDocNamesRealTests(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "docs", "specs", "provider-port-obligations.md"))
	if err != nil {
		t.Fatalf("reading the obligations doc: %v", err)
	}
	named := regexp.MustCompile("`(Test[A-Za-z0-9]+)`").FindAllStringSubmatch(string(doc), -1)
	if len(named) == 0 {
		t.Fatal("the obligations doc names no tests at all")
	}

	defined := map[string]bool{}
	declaration := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9]+)`)
	err = filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range declaration.FindAllStringSubmatch(string(source), -1) {
			defined[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking for tests: %v", err)
	}

	for _, m := range named {
		if !defined[m[1]] {
			t.Errorf("the obligations doc cites %s, which no longer exists", m[1])
		}
	}
}
