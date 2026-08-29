// Package arch holds no product code. It enforces where the framework may be
// named, because a boundary that only a reader can check is a claim rather than a
// constraint.
package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
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
	"internal/provider/qwen",
	"internal/provider/openrouter",

	// A provider off the shared dialect is on the same footing: it drives its
	// own framework adapter and exposes none of it. Being off the dialect
	// changes what the port can SEE — the capture cannot read this wire's
	// bytes — not what it may export.
	"internal/provider/ollama",
	"internal/provider/claude",

	// The shared implementation for one dialect is on the same footing as the
	// ports that use it: it drives the framework's adapters, and the same rule
	// applies to what it exposes. Adding it here rather than exempting the
	// whole tree keeps the list what it is — a decision per package, so a new
	// one has to be argued for rather than inherited.
	"internal/provider/chatcompletions",
}

const einoModule = "github.com/cloudwego/eino"

// modulePath is this repository's own import path.
const modulePath = "github.com/iamclancyliang/pi-go"

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
	// Every provider that is allowed to name the framework, so a new one is
	// covered by being added to that list rather than by remembering to add it
	// here as well.
	for _, allowed := range einoIsAllowedIn {
		if !strings.HasPrefix(allowed, "internal/provider/") {
			continue
		}
		if allowed == dialectPackage {
			// The shared dialect is INSIDE the boundary rather than at it: the
			// only packages that compile against it are ports already allowed
			// to name the framework, so a framework type in its signature moves
			// the dependency nowhere. That it stays inside is not taken on
			// trust — TestNothingOutsideTheBoundaryImportsTheDialect checks it.
			continue
		}
		dir := filepath.Join(root, filepath.FromSlash(allowed))

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
						t.Errorf("%s/%s: exported %s has a framework type in its signature",
							allowed, filepath.Base(path), offence)
					}
				}
			}
		}
	}
}

// rejectedAdapters are framework adapters this repository has tried and found
// unable to carry something it promises. They may be named where the evidence
// lives and nowhere else: a rejected adapter that reaches the product is one
// somebody wired in believing the earlier finding no longer applied.
var rejectedAdapters = map[string]string{
	"github.com/cloudwego/eino-ext/components/model/agenticqwen": "loses the identity of " +
		"interleaved tool calls; see the probes that record it",
}

// ruleDeclaration marks the file holding the list above.
const ruleDeclaration = "rejectedAdapters = map[string]" + "string{"

// TestARejectedAdapterStaysOutOfTheProduct.
func TestARejectedAdapterStaysOutOfTheProduct(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("repository root: %v", err)
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor", "spikes":
				// The evidence lives in spikes: showing what an adapter does
				// wrong means importing it.
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(source), ruleDeclaration) {
			// The file that lists them for this rule has to name them, and
			// naming them there is what stops them being used elsewhere.
			return nil
		}
		for adapter, why := range rejectedAdapters {
			if strings.Contains(string(source), adapter) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s names %s, which %s", rel, adapter, why)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking: %v", err)
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

// dialectPackage is the shared implementation ports of one wire dialect use.
const dialectPackage = "internal/provider/chatcompletions"

// TestNothingOutsideTheBoundaryImportsTheDialect is what lets the dialect
// package name framework types in what it exports.
//
// The rule was never "no exported signature anywhere mentions eino" — it is
// that the framework choice stays reversible, which means nothing outside the
// provider boundary compiles against it. The dialect sits inside that boundary
// and hands framework types only to ports that already depend on them.
//
// The moment something else imports it, that stops being true, and this is
// where it is caught rather than in a review.
func TestNothingOutsideTheBoundaryImportsTheDialect(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("repository root: %v", err)
	}
	dialect := modulePath + "/" + dialectPackage

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		if strings.HasPrefix(relative, ".git/") || allowed(filepath.FromSlash(relative)) {
			return nil
		}

		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return nil
		}
		for _, imported := range file.Imports {
			if strings.Trim(imported.Path.Value, `"`) == dialect {
				t.Errorf("%s imports the shared dialect from outside the provider boundary; "+
					"it would compile against framework types", relative)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}
}
