package events

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// TestAllKindsMatchesTheDeclarations keeps the list honest.
//
// AllKinds exists so consumers can be checked for completeness, which only works
// while the list itself is complete. Maintained by hand it drifts silently: a kind
// declared but left out disappears from every check built on it, and those checks
// go on passing.
//
// The declarations are read as VALUES from the source, not re-listed here. A
// second copy of the mapping would be one more thing to keep in step, which is
// the problem this test exists to catch.
func TestAllKindsMatchesTheDeclarations(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "events.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing events.go: %v", err)
	}

	declared := map[string]string{} // value -> constant name
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		if ident, ok := spec.Type.(*ast.Ident); !ok || ident.Name != "Kind" {
			return true
		}
		for index, name := range spec.Names {
			if index >= len(spec.Values) {
				continue
			}
			literal, ok := spec.Values[index].(*ast.BasicLit)
			if !ok {
				continue
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatalf("%s has a non-literal value: %v", name.Name, err)
			}
			declared[value] = name.Name
		}
		return true
	})
	if len(declared) == 0 {
		t.Fatal("no Kind constants found; the parse is not seeing the declarations")
	}

	listed := map[string]bool{}
	for _, kind := range AllKinds() {
		listed[string(kind)] = true
	}
	for value, name := range declared {
		if !listed[value] {
			t.Errorf("%s (%q) is declared but missing from AllKinds", name, value)
		}
	}
	for value := range listed {
		if _, ok := declared[value]; !ok {
			t.Errorf("AllKinds lists %q, which no declaration defines", value)
		}
	}
}
