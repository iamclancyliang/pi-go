package tools

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// PathMatch is a file or directory SearchPaths found, relative to the
// directory searched and slash-separated.
type PathMatch struct {
	Path  string
	IsDir bool
}

// SearchPaths finds files and directories below base for an @-mention, as
// Pi's completion asks fd to: hidden entries included, anything named .git
// left out, symbolic links followed, and .gitignore rules applied inside a
// git repository. The query is a regular expression, case-insensitive unless
// it has an upper-case letter, matched against each entry's name — or, when
// it contains a slash, against its full path, with each slash standing for
// either separator. An empty query matches everything. At most limit
// entries are returned, nearest first.
//
// fd walks in parallel and returns its first hundred in no fixed order; the
// walk here is breadth-first, so the same tree gives the same answer and
// shallow entries, the likelier targets, come first.
func SearchPaths(ctx context.Context, base, query string, limit int) []PathMatch {
	match, ok := pathQuery(query)
	if !ok {
		return nil
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return nil
	}
	fullPath := strings.Contains(toSlash(query), "/")

	var stack ignoreStack
	if insideGitRepository(absBase) {
		stack = ignoreStack{loadIgnore(absBase)}
	}

	type dir struct {
		path  string
		stack ignoreStack
	}
	queue := []dir{{absBase, stack}}
	visited := map[string]bool{}
	if real, err := filepath.EvalSymlinks(absBase); err == nil {
		visited[real] = true
	}

	var out []PathMatch
	for len(queue) > 0 {
		if ctx.Err() != nil {
			return nil
		}
		d := queue[0]
		queue = queue[1:]
		entries, err := os.ReadDir(d.path)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if name == ".git" {
				continue
			}
			full := filepath.Join(d.path, name)
			isDir := entry.IsDir()
			if entry.Type()&os.ModeSymlink != 0 {
				info, err := os.Stat(full)
				if err != nil {
					continue
				}
				isDir = info.IsDir()
			}
			if d.stack.ignored(absBase, full, isDir) {
				continue
			}
			rel, err := filepath.Rel(absBase, full)
			if err != nil {
				continue
			}
			target := name
			if fullPath {
				target = filepath.ToSlash(full)
			}
			if match(target) {
				out = append(out, PathMatch{Path: filepath.ToSlash(rel), IsDir: isDir})
				if len(out) >= limit {
					return out
				}
			}
			if !isDir {
				continue
			}
			// A link back up the tree would be walked forever; each real
			// directory is entered once.
			real, err := filepath.EvalSymlinks(full)
			if err != nil || visited[real] {
				continue
			}
			visited[real] = true
			nested := d.stack
			if d.stack != nil {
				if rules := loadIgnore(full); rules != nil {
					nested = append(append(ignoreStack(nil), d.stack...), rules)
				}
			}
			queue = append(queue, dir{full, nested})
		}
	}
	return out
}

func toSlash(s string) string { return strings.ReplaceAll(s, `\`, "/") }

// pathQuery compiles a query the way Pi builds fd's pattern: a query without
// a slash is used as written; one with slashes has each segment taken
// literally, joined by a pattern for either separator.
func pathQuery(query string) (func(string) bool, bool) {
	if query == "" {
		return func(string) bool { return true }, true
	}
	pattern := toSlash(query)
	if strings.Contains(pattern, "/") {
		trailing := strings.HasSuffix(pattern, "/")
		var segments []string
		for _, s := range strings.Split(strings.Trim(pattern, "/"), "/") {
			if s != "" {
				segments = append(segments, regexp.QuoteMeta(s))
			}
		}
		if len(segments) > 0 {
			pattern = strings.Join(segments, `[\\/]`)
			if trailing {
				pattern += `[\\/]`
			}
		}
	}
	if !strings.ContainsFunc(query, unicode.IsUpper) {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, false
	}
	return re.MatchString, true
}

// insideGitRepository reports whether dir is in a git work tree, which is
// when fd applies .gitignore files.
func insideGitRepository(dir string) bool {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}
