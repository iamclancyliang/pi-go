package tools

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// alwaysSkipped are directories no search should descend into.
//
// Pi passes these to its searcher explicitly rather than relying on a
// .gitignore listing them, because the cost of walking them is paid even when a
// rule would have excluded the results. They are skipped whether or not the
// tree is a repository.
var alwaysSkipped = map[string]bool{
	".git":         true,
	"node_modules": true,
}

// walkFiles visits every file under root that the ignore rules allow, in
// directory order, passing each path relative to root and slash-separated.
//
// Visiting stops when visit returns false, which is how a result limit avoids
// walking a tree it has already stopped reading.
//
// Hidden files are INCLUDED. A coding agent needs to see dotfiles — they are
// where configuration lives — and a searcher that hides them reports that a
// file it was asked about does not exist.
func walkFiles(ctx context.Context, root string, visit func(rel string, entry fs.DirEntry) bool) error {
	stack := ignoreStack{loadIgnore(root)}
	err := walkDir(ctx, root, root, stack, visit)
	if _, stopped := err.(stopWalk); stopped {
		// Stopping early is how a caller says it has enough, not a failure.
		return nil
	}
	return err
}

func walkDir(ctx context.Context, root, dir string, stack ignoreStack,
	visit func(rel string, entry fs.DirEntry) bool) error {

	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// A directory that cannot be read is skipped rather than failing the
		// whole search: one unreadable subtree must not hide every result
		// elsewhere.
		return nil
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		full := filepath.Join(dir, name)
		isDir := entry.IsDir()
		if isDir && alwaysSkipped[name] {
			continue
		}
		if stack.ignored(root, full, isDir) {
			continue
		}

		rel, err := filepath.Rel(root, full)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)

		if isDir {
			// A nested .gitignore joins the rules for its own subtree only.
			nested := stack
			if rules := loadIgnore(full); rules != nil {
				nested = append(append(ignoreStack(nil), stack...), rules)
			}
			if err := walkDir(ctx, root, full, nested, visit); err != nil {
				return err
			}
			continue
		}
		if !visit(rel, entry) {
			return errStopWalk
		}
	}
	return nil
}

// errStopWalk unwinds a walk that a caller ended early. It never leaves the
// package: walkFiles turns it back into a normal return.
var errStopWalk = stopWalk{}

type stopWalk struct{}

func (stopWalk) Error() string { return "tools: walk stopped" }

// matchPathPattern applies a find pattern the way Pi's searcher does.
//
// A pattern with no separator matches the BASE NAME at any depth, so `*.go`
// finds every Go file rather than only those at the top. A pattern containing
// one matches the whole relative path, with an implicit `**/` prefix unless it
// is already anchored — otherwise `src/**/*.go` would match nothing below the
// first level.
func matchPathPattern(pattern, rel string) bool {
	if !strings.Contains(pattern, "/") {
		return matchGlob(pattern, filepath.Base(rel))
	}
	anchored := strings.HasPrefix(pattern, "/")
	pattern = strings.TrimPrefix(pattern, "/")
	if matchGlob(pattern, rel) {
		return true
	}
	if anchored || strings.HasPrefix(pattern, "**/") || pattern == "**" {
		return false
	}
	return matchGlob("**/"+pattern, rel)
}
