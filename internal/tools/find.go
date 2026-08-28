package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// DefaultFindLimit is how many paths one search returns unasked.
const DefaultFindLimit = 1000

// Find locates files by glob pattern.
//
// Ignored files stay out of the results. Without that, a search of any real
// repository returns build output, dependencies and caches — an answer that is
// technically correct and useless, and one a model then spends its context
// reading.
type Find struct {
	// Root is what a relative search path is relative to.
	Root string

	// Limits bound one call's output. The zero value uses the defaults.
	Limits Limits
}

func (f *Find) Name() string { return "find" }

func (f *Find) Description() string {
	return fmt.Sprintf("Search for files by glob pattern. Returns matching file paths relative to "+
		"the search directory. Respects .gitignore. Output is truncated to %d results or %s "+
		"(whichever is hit first).", DefaultFindLimit, FormatSize(DefaultMaxBytes))
}

func (f *Find) Execution() Execution {
	return Execution{ReadOnly: true, Replay: ReplaySafe}
}

func (f *Find) Parameters() *Schema {
	return &Schema{Parameters: []Parameter{
		{
			Name:        "pattern",
			Kind:        KindString,
			Description: "Glob pattern to match files, e.g. '*.ts', '**/*.json', or 'src/**/*.spec.ts'",
			Required:    true,
		},
		{
			Name:        "path",
			Kind:        KindString,
			Description: "Directory to search in (default: current directory)",
		},
		{
			Name:        "limit",
			Kind:        KindNumber,
			Description: fmt.Sprintf("Maximum number of results (default: %d)", DefaultFindLimit),
		},
	}}
}

type findArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Limit   *int   `json:"limit"`
}

func (f *Find) Call(ctx context.Context, args string) (Result, error) {
	var in findArgs
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return Result{}, fmt.Errorf("find: invalid arguments %q: %w", args, err)
	}
	if strings.TrimSpace(in.Pattern) == "" {
		return Result{}, fmt.Errorf("find: pattern is required")
	}

	asked := in.Path
	if strings.TrimSpace(asked) == "" {
		asked = "."
	}
	dir, err := resolvePath(f.Root, asked)
	if err != nil {
		return Result{}, fmt.Errorf("find: %s: %w", asked, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return Result{}, fmt.Errorf("find: %s: %w", asked, err)
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("find: %s is not a directory", asked)
	}

	limit := DefaultFindLimit
	if in.Limit != nil {
		limit = *in.Limit
		if limit < 0 {
			limit = 0
		}
	}

	var found []string
	limitReached := false
	err = walkFiles(ctx, dir, func(rel string, _ fs.DirEntry) bool {
		if !matchPathPattern(in.Pattern, rel) {
			return true
		}
		found = append(found, rel)
		if len(found) >= limit {
			limitReached = true
			return false
		}
		return true
	})
	if err != nil {
		return Result{}, fmt.Errorf("find: %s: %w", asked, err)
	}

	if len(found) == 0 {
		// Said outright: an empty result reads as a tool that failed quietly
		// rather than a search that ran and matched nothing.
		return Result{Content: "No files found matching pattern"}, nil
	}

	// Only the byte budget applies: the result count is already capped, so a
	// line limit would cut the list somewhere no notice could explain.
	cut := TruncateHead(strings.Join(found, "\n"), Limits{
		MaxLines: len(found) + 1,
		MaxBytes: f.Limits.MaxBytes,
	})

	var notices []string
	if limitReached {
		notices = append(notices, fmt.Sprintf("%d results limit reached", limit))
	}
	if cut.Truncated {
		notices = append(notices, fmt.Sprintf("%s limit reached", FormatSize(cut.MaxBytes)))
	}
	if len(notices) == 0 {
		return Result{Content: cut.Content}, nil
	}
	return Result{Content: cut.Content + "\n\n[" + strings.Join(notices, ". ") + "]"}, nil
}
