// Package arch holds no product code. It enforces where the framework may be
// named, because a boundary that only a reader can check is a claim rather than a
// constraint.
package arch

import (
	"go/ast"
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

	// A provider implementation may drive a framework adapter, because that is
	// what the adapter is for: it speaks a provider's wire format so this
	// repository does not have to. What it may NOT do is let those types reach
	// its own callers — that is the part the rule exists to prevent, and it is
	// enforced separately by TestAProviderExposesNoFrameworkType rather than by
	// this list.
	"internal/provider/openai",
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

// TestAProviderExposesNoFrameworkType keeps the guarantee the directory list
// gives up.
//
// A provider may use the framework inside itself. If a framework type appears
// in what it exports, the dependency has not been hidden — it has been moved
// into everything that compiles against the provider, which is exactly what the
// port boundary exists to stop.
func TestAProviderExposesNoFrameworkType(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("repository root: %v", err)
	}
	dir := filepath.Join(root, "internal", "provider", "openai")

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", dir, err)
	}

	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			aliases := frameworkAliases(file)
			if len(aliases) == 0 {
				continue
			}
			for _, decl := range file.Decls {
				for _, offence := range exportedUsesFramework(decl, aliases) {
					t.Errorf("%s: exported %s has a framework type in its signature",
						filepath.Base(path), offence)
				}
			}
		}
	}
}

// frameworkAliases is the local name each framework import goes by in a file.
func frameworkAliases(file *ast.File) map[string]bool {
	aliases := map[string]bool{}
	for _, imported := range file.Imports {
		path := strings.Trim(imported.Path.Value, `"`)
		if !strings.HasPrefix(path, einoModule) {
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if imported.Name != nil {
			name = imported.Name.Name
		}
		aliases[name] = true
	}
	return aliases
}

// exportedUsesFramework names every exported declaration whose signature or
// fields mention one of the framework aliases.
func exportedUsesFramework(decl ast.Decl, aliases map[string]bool) []string {
	mentions := func(node ast.Node) bool {
		found := false
		ast.Inspect(node, func(n ast.Node) bool {
			selector, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := selector.X.(*ast.Ident)
			if ok && aliases[ident.Name] {
				found = true
			}
			return true
		})
		return found
	}

	var offences []string
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if !d.Name.IsExported() || d.Type == nil {
			return nil
		}
		// A method on an unexported receiver is not part of the package's API.
		if d.Recv != nil && !receiverIsExported(d.Recv) {
			return nil
		}
		if mentions(d.Type) {
			offences = append(offences, "func "+d.Name.Name)
		}
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			switch sp := spec.(type) {
			case *ast.TypeSpec:
				if sp.Name.IsExported() && mentions(sp.Type) {
					offences = append(offences, "type "+sp.Name.Name)
				}
			case *ast.ValueSpec:
				for _, name := range sp.Names {
					if name.IsExported() && sp.Type != nil && mentions(sp.Type) {
						offences = append(offences, "value "+name.Name)
					}
				}
			}
		}
	}
	return offences
}

func receiverIsExported(recv *ast.FieldList) bool {
	if len(recv.List) == 0 {
		return false
	}
	name := ""
	switch t := recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if ident, ok := t.X.(*ast.Ident); ok {
			name = ident.Name
		}
	case *ast.Ident:
		name = t.Name
	}
	return name != "" && ast.IsExported(name)
}
