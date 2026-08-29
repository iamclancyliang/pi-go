package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DefaultGrepLimit is how many matches one search returns unasked.
const DefaultGrepLimit = 100

// Grep searches file contents.
//
// Like find, it honours ignore rules: searching a repository's build output and
// dependencies returns matches a model cannot act on and buries the ones it can.
type Grep struct {
	// Root is what a relative search path is relative to.
	Root string

	// Limits bound one call's output. The zero value uses the defaults.
	Limits Limits
}

func (g *Grep) Name() string { return "grep" }

func (g *Grep) Description() string {
	return fmt.Sprintf("Search file contents for a pattern. Returns matching lines with file and "+
		"line number. Respects .gitignore. Output is truncated to %d matches or %s (whichever is "+
		"hit first).", DefaultGrepLimit, FormatSize(DefaultMaxBytes))
}

func (g *Grep) Execution() Execution {
	return Execution{ReadOnly: true, Replay: ReplaySafe}
}

// Prompt is what this tool tells the model about itself.
//
// The wording is Pi's, kept because it is what its models were given: a
// rephrasing is a different instruction, and the difference would show up as
// a behaviour change nobody could trace to a decision.
func (g *Grep) Prompt() Contribution {
	return Contribution{
		Snippet: "Search file contents for patterns (respects .gitignore)",
	}
}

func (g *Grep) Parameters() *Schema {
	return &Schema{Parameters: []Parameter{
		{
			Name:        "pattern",
			Kind:        KindString,
			Description: "Search pattern (regex or literal string)",
			Required:    true,
		},
		{
			Name:        "path",
			Kind:        KindString,
			Description: "Directory or file to search (default: current directory)",
		},
		{
			Name:        "glob",
			Kind:        KindString,
			Description: "Filter files by glob pattern, e.g. '*.ts' or '**/*.spec.ts'",
		},
		{
			Name:        "ignoreCase",
			Kind:        KindBoolean,
			Description: "Case-insensitive search (default: false)",
		},
		{
			Name:        "literal",
			Kind:        KindBoolean,
			Description: "Treat pattern as literal string instead of regex (default: false)",
		},
		{
			Name:        "context",
			Kind:        KindNumber,
			Description: "Number of lines to show before and after each match (default: 0)",
		},
		{
			Name:        "limit",
			Kind:        KindNumber,
			Description: fmt.Sprintf("Maximum number of matches to return (default: %d)", DefaultGrepLimit),
		},
	}}
}

type grepArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	IgnoreCase bool   `json:"ignoreCase"`
	Literal    bool   `json:"literal"`
	Context    *int   `json:"context"`
	Limit      *int   `json:"limit"`
}

type grepMatch struct {
	path string
	line int
}

func (g *Grep) Call(ctx context.Context, args string) (Result, error) {
	var in grepArgs
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return Result{}, fmt.Errorf("grep: invalid arguments %q: %w", args, err)
	}
	if in.Pattern == "" {
		return Result{}, fmt.Errorf("grep: pattern is required")
	}

	expr := in.Pattern
	if in.Literal {
		// Escaped rather than searched for with a substring scan, so that one
		// code path handles both and the case-insensitive flag keeps working.
		expr = regexp.QuoteMeta(expr)
	}
	if in.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		// Named as a pattern problem: a model that mistyped a regex must not be
		// told the file could not be read.
		return Result{}, fmt.Errorf("grep: invalid pattern %q: %w", in.Pattern, err)
	}

	asked := in.Path
	if strings.TrimSpace(asked) == "" {
		asked = "."
	}
	target, err := resolvePath(g.Root, asked)
	if err != nil {
		return Result{}, fmt.Errorf("grep: %s: %w", asked, err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return Result{}, fmt.Errorf("grep: %s: %w", asked, err)
	}

	// At least one: a limit of zero would search and then report nothing found,
	// which is indistinguishable from a pattern that does not match.
	limit := DefaultGrepLimit
	if in.Limit != nil {
		limit = *in.Limit
	}
	if limit < 1 {
		limit = 1
	}
	around := 0
	if in.Context != nil && *in.Context > 0 {
		around = *in.Context
	}

	var matches []grepMatch
	contents := map[string][]string{}
	limitReached := false

	search := func(label, path string) bool {
		lines, err := readLines(path)
		if err != nil {
			// Binary or unreadable files are skipped rather than reported: a
			// search of a tree hits them constantly, and each one is noise.
			return true
		}
		contents[label] = lines
		for n, line := range lines {
			if !re.MatchString(line) {
				continue
			}
			matches = append(matches, grepMatch{path: label, line: n + 1})
			if len(matches) >= limit {
				limitReached = true
				return false
			}
		}
		return true
	}

	if info.IsDir() {
		err = walkFiles(ctx, target, func(rel string, _ fs.DirEntry) bool {
			if in.Glob != "" && !matchPathPattern(in.Glob, rel) {
				return true
			}
			return search(rel, filepath.Join(target, rel))
		})
		if err != nil {
			return Result{}, fmt.Errorf("grep: %s: %w", asked, err)
		}
	} else {
		// A single file is labelled by its base name: there is no search root to
		// be relative to, and the full path is noise the model already has.
		search(filepath.Base(target), target)
	}

	if len(matches) == 0 {
		return Result{Content: "No matches found"}, nil
	}

	var out []string
	shortened := false
	for _, m := range matches {
		lines := contents[m.path]
		first, last := m.line, m.line
		if around > 0 {
			first = max(1, m.line-around)
			last = min(len(lines), m.line+around)
		}
		for n := first; n <= last; n++ {
			text := ""
			if n-1 < len(lines) {
				text = lines[n-1]
			}
			cut, was := TruncateLine(text)
			if was {
				shortened = true
			}
			// The separator says which line matched: a block of context in
			// which every line looks alike makes the model guess.
			if n == m.line {
				out = append(out, fmt.Sprintf("%s:%d: %s", m.path, n, cut))
			} else {
				out = append(out, fmt.Sprintf("%s-%d- %s", m.path, n, cut))
			}
		}
	}

	// Only the byte budget applies: the match count is already capped.
	cut := TruncateHead(strings.Join(out, "\n"), Limits{
		MaxLines: len(out) + 1,
		MaxBytes: g.Limits.MaxBytes,
	})

	var notices []string
	if limitReached {
		notices = append(notices, fmt.Sprintf(
			"%d matches limit reached. Use limit=%d for more, or refine pattern", limit, limit*2))
	}
	if cut.Truncated {
		notices = append(notices, fmt.Sprintf("%s limit reached", FormatSize(cut.MaxBytes)))
	}
	if shortened {
		notices = append(notices, fmt.Sprintf(
			"Some lines truncated to %d chars. Use read tool to see full lines", GrepMaxLineLength))
	}
	if len(notices) == 0 {
		return Result{Content: cut.Content}, nil
	}
	return Result{Content: cut.Content + "\n\n[" + strings.Join(notices, ". ") + "]"}, nil
}

// readLines reads a text file, normalising the line endings a match's line
// number is counted in. A file written on Windows must not report every line
// number one off, or carry a stray carriage return into the model's view.
func readLines(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Split(text, "\n"), nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
