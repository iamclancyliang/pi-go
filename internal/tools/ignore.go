package tools

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// ignoreRules is what the .gitignore files along a path say about it.
//
// Ported because without it a search of any real repository returns build
// output, dependencies and caches — the answer is technically correct and
// useless, and a model then spends its context reading vendored code.
//
// What is supported: comments and blank lines, `!` negation, a trailing `/` for
// directory-only rules, a leading or embedded `/` to anchor a rule to the file
// that declared it, `**`, and the rule that the LAST match wins. Rules are read
// from every directory walked into, so a nested .gitignore applies below itself.
//
// What is NOT supported, and so behaves as though the rule were absent: the
// escaping of a literal `#`, `!` or trailing space with a backslash, and
// `.git/info/exclude` and the global core.excludesFile. A repository relying on
// those will see files a git-aware search would have hidden.
type ignoreRules struct {
	// dir is the directory whose .gitignore produced these, used to anchor a
	// rule that carries a slash.
	dir      string
	patterns []ignorePattern
}

type ignorePattern struct {
	pattern string

	// negate un-ignores what an earlier rule ignored.
	negate bool

	// dirOnly restricts the rule to directories, which is what a trailing
	// slash means.
	dirOnly bool

	// anchored ties the rule to the declaring directory rather than letting it
	// match at any depth.
	anchored bool
}

// loadIgnore reads one directory's .gitignore, returning nil when there is none.
func loadIgnore(dir string) *ignoreRules {
	f, err := os.Open(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return nil
	}
	defer f.Close()

	rules := &ignoreRules{dir: dir}
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimRight(scan.Text(), " ")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var p ignorePattern
		if strings.HasPrefix(line, "!") {
			p.negate = true
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			p.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		if line == "" {
			continue
		}
		// A slash anywhere but the end anchors the rule; a bare name matches at
		// any depth. This is git's rule, and getting it backwards makes
		// `build/` in one package hide every build directory in the tree.
		trimmed := strings.TrimPrefix(line, "/")
		p.anchored = strings.Contains(trimmed, "/") || strings.HasPrefix(line, "/")
		p.pattern = trimmed
		rules.patterns = append(rules.patterns, p)
	}
	return rules
}

// ignoreStack is the set of rules in force at one point in a walk.
type ignoreStack []*ignoreRules

// ignored reports whether a path is excluded, where relPath is slash-separated
// and relative to the root of the walk.
//
// Every level is consulted and the last match wins, so a nested .gitignore can
// re-include what an outer one excluded.
func (s ignoreStack) ignored(root, path string, isDir bool) bool {
	decided, ignore := false, false
	for _, rules := range s {
		if rules == nil {
			continue
		}
		rel, err := filepath.Rel(rules.dir, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		rel = filepath.ToSlash(rel)
		for _, p := range rules.patterns {
			if p.dirOnly && !isDir {
				continue
			}
			if !p.matches(rel) {
				continue
			}
			decided, ignore = true, !p.negate
		}
	}
	_ = root
	return decided && ignore
}

func (p ignorePattern) matches(rel string) bool {
	if p.anchored {
		if matchGlob(p.pattern, rel) {
			return true
		}
		// An ignored directory takes everything under it, which is what makes
		// one `node_modules/` line enough.
		return matchGlob(p.pattern+"/**", rel)
	}
	// Unanchored rules match at any depth, on the entry itself or on anything
	// below a directory they matched.
	return matchGlob(p.pattern, rel) ||
		matchGlob("**/"+p.pattern, rel) ||
		matchGlob("**/"+p.pattern+"/**", rel) ||
		matchGlob(p.pattern+"/**", rel)
}
