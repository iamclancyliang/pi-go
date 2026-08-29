package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultLsLimit is how many entries one listing returns unasked.
const DefaultLsLimit = 500

// Ls lists what a directory holds.
//
// Bounded twice, like every other tool's output: by a count of entries and by
// the shared byte budget. A directory of a hundred thousand files is not
// something a model can act on, and sending it costs a context window to say so.
type Ls struct {
	// Root is what a relative path is relative to.
	Root string

	// Limits bound one call's output. The zero value uses the defaults.
	Limits Limits
}

func (l *Ls) Name() string { return "ls" }

func (l *Ls) Description() string {
	return fmt.Sprintf("List directory contents. Returns entries sorted alphabetically, with '/' "+
		"suffix for directories. Includes dotfiles. Output is truncated to %d entries or %s "+
		"(whichever is hit first).", DefaultLsLimit, FormatSize(DefaultMaxBytes))
}

func (l *Ls) Execution() Execution {
	return Execution{ReadOnly: true, Replay: ReplaySafe}
}

// Prompt is what this tool tells the model about itself.
//
// The wording is Pi's, kept because it is what its models were given: a
// rephrasing is a different instruction, and the difference would show up as
// a behaviour change nobody could trace to a decision.
func (l *Ls) Prompt() Contribution {
	return Contribution{
		Snippet: "List directory contents",
	}
}

func (l *Ls) Parameters() *Schema {
	return &Schema{Parameters: []Parameter{
		{
			Name:        "path",
			Kind:        KindString,
			Description: "Directory to list (default: current directory)",
		},
		{
			Name:        "limit",
			Kind:        KindNumber,
			Description: fmt.Sprintf("Maximum number of entries to return (default: %d)", DefaultLsLimit),
		},
	}}
}

type lsArgs struct {
	Path string `json:"path"`
	// A pointer because a limit of zero is a request for nothing, which is not
	// the same as not having asked.
	Limit *int `json:"limit"`
}

func (l *Ls) Call(ctx context.Context, args string) (Result, error) {
	var in lsArgs
	if strings.TrimSpace(args) != "" {
		if err := json.Unmarshal([]byte(args), &in); err != nil {
			return Result{}, fmt.Errorf("ls: invalid arguments %q: %w", args, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	asked := in.Path
	if strings.TrimSpace(asked) == "" {
		asked = "."
	}
	dir, err := resolvePath(l.Root, asked)
	if err != nil {
		return Result{}, fmt.Errorf("ls: %s: %w", asked, err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		return Result{}, fmt.Errorf("ls: %s: %w", asked, err)
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("ls: %s is not a directory", asked)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Result{}, fmt.Errorf("ls: %s: %w", asked, err)
	}

	limit := DefaultLsLimit
	if in.Limit != nil {
		limit = *in.Limit
		if limit < 0 {
			limit = 0
		}
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	// Case-insensitively, so a listing reads the way a person expects rather
	// than with every capitalised name grouped ahead of the lowercase ones.
	// Ties fall back to the case-sensitive order, because two names differing
	// only in case must still come out in a stable order.
	sort.Slice(names, func(i, j int) bool {
		li, lj := strings.ToLower(names[i]), strings.ToLower(names[j])
		if li != lj {
			return li < lj
		}
		return names[i] < names[j]
	})

	shown := make([]string, 0, len(names))
	limitReached := false
	for _, name := range names {
		if len(shown) >= limit {
			limitReached = true
			break
		}
		// Stat rather than trust the directory entry: a symlink's own type says
		// nothing about whether following it lands on a directory, and the
		// suffix is what tells the model where it can descend.
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			// An entry that cannot be examined is skipped rather than guessed
			// at. A broken symlink is the ordinary case.
			continue
		}
		if info.IsDir() {
			name += "/"
		}
		shown = append(shown, name)
	}

	if len(shown) == 0 {
		// Said outright, because an empty string reads as a tool that failed
		// quietly rather than a directory with nothing in it.
		return Result{Content: "(empty directory)"}, nil
	}

	// Only the byte budget applies here: the entry count is already capped, and
	// a second line limit would cut the listing at a different place for a
	// reason the notice could not name.
	cut := TruncateHead(strings.Join(shown, "\n"), Limits{
		MaxLines: len(shown) + 1,
		MaxBytes: l.Limits.MaxBytes,
	})

	var notices []string
	if limitReached {
		notices = append(notices,
			fmt.Sprintf("%d entries limit reached. Use limit=%d for more", limit, limit*2))
	}
	if cut.Truncated {
		notices = append(notices, fmt.Sprintf("%s limit reached", FormatSize(cut.MaxBytes)))
	}
	if len(notices) == 0 {
		return Result{Content: cut.Content}, nil
	}
	return Result{Content: cut.Content + "\n\n[" + strings.Join(notices, ". ") + "]"}, nil
}
