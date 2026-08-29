package tools

import (
	"os"
	"path/filepath"
)

// ContextFileNames are the files a directory may carry instructions in, in the
// order they are tried. The first that exists wins, so an override sits ahead
// of the file it overrides.
//
// Both names are recognised because both are in use: a repository written for
// one agent should not have to be rewritten to be understood by another.
var ContextFileNames = []string{
	"AGENTS.override.md",
	"AGENTS.md",
	"AGENTS.MD",
	"CLAUDE.md",
	"CLAUDE.MD",
}

// LoadContextFiles gathers a project's own instructions.
//
// The agent directory's file comes first, then every ancestor from the
// filesystem root down to the working directory — so the NEAREST file is last.
// Order is the whole point: a model reads later instructions as the more
// specific ones, and a repository must be able to narrow what its parent
// directory said without arguing with something read afterwards.
//
// A path already collected is skipped, so one file cannot apply twice.
//
// Pi additionally shadows a main repository's file when running in a linked
// git worktree nested under it, since both occupy one logical scope. That is
// NOT ported: it requires reading git's worktree layout, and without it such a
// setup applies the same context twice rather than losing any — the safer of
// the two ways to be wrong.
func LoadContextFiles(agentDir, workingDir string) []ContextFile {
	var files []ContextFile
	seen := map[string]bool{}

	if found, ok := contextFileIn(agentDir); ok {
		files = append(files, found)
		seen[found.Path] = true
	}

	// Walked upward and then reversed, because the walk is naturally
	// nearest-first and the prompt wants nearest-last.
	var ancestors []ContextFile
	dir, err := filepath.Abs(workingDir)
	if err != nil {
		dir = workingDir
	}
	for {
		if found, ok := contextFileIn(dir); ok && !seen[found.Path] {
			ancestors = append(ancestors, found)
			seen[found.Path] = true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	for i := len(ancestors) - 1; i >= 0; i-- {
		files = append(files, ancestors[i])
	}
	return files
}

// contextFileIn reads the first context file a directory carries.
//
// An unreadable file is skipped rather than failing the run: a permission
// problem in one ancestor must not stop the agent from starting, and the
// instructions it would have carried are not worth refusing to work over.
func contextFileIn(dir string) (ContextFile, bool) {
	if dir == "" {
		return ContextFile{}, false
	}
	for _, name := range ContextFileNames {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		return ContextFile{Path: path, Content: string(content)}, true
	}
	return ContextFile{}, false
}
