package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Read shows the contents of a file.
//
// Bounded on purpose: a file larger than the limits comes back as its first
// whole lines plus the offset to continue from, rather than as everything. An
// unbounded read spends a context window on one file and leaves the model
// unable to act on what it just read.
type Read struct {
	// Root is what a relative path is relative to.
	Root string

	// Limits bound one call's output. The zero value uses the defaults.
	Limits Limits
}

func (r *Read) Name() string { return "read" }

func (r *Read) Description() string {
	return "Read the contents of a file. Prefer this over shell commands like cat or sed."
}

func (r *Read) Execution() Execution {
	// Reading changes nothing, so a call whose outcome was lost may simply be
	// made again.
	return Execution{ReadOnly: true, Replay: ReplaySafe}
}

func (r *Read) Parameters() *Schema {
	return &Schema{Parameters: []Parameter{
		{
			Name:        "path",
			Kind:        KindString,
			Description: "Path to the file to read (relative or absolute)",
			Required:    true,
		},
		{
			Name:        "offset",
			Kind:        KindNumber,
			Description: "Line number to start reading from (1-indexed)",
		},
		{
			Name:        "limit",
			Kind:        KindNumber,
			Description: "Maximum number of lines to read",
		},
	}}
}

type readArgs struct {
	Path string `json:"path"`
	// Pointers because absent and zero differ: offset 0 is not a line, and a
	// limit of 0 is a request for nothing rather than a request for the default.
	Offset *int `json:"offset"`
	Limit  *int `json:"limit"`
}

func (r *Read) Call(ctx context.Context, args string) (Result, error) {
	var in readArgs
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return Result{}, fmt.Errorf("read: invalid arguments %q: %w", args, err)
	}
	if strings.TrimSpace(in.Path) == "" {
		return Result{}, fmt.Errorf("read: path is required")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	path, err := r.resolve(in.Path)
	if err != nil {
		return Result{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		// Both paths: the one the model wrote, which is what it can act on,
		// and the resolved one the operating system reports, which is how a
		// path that quietly resolved somewhere unexpected becomes visible. Pi
		// surfaces the resolved path here too.
		return Result{}, fmt.Errorf("read: %s: %w", in.Path, err)
	}

	// Split plainly rather than the way the limits count lines: these are the
	// addresses offset and limit refer to, so a trailing newline does yield a
	// final empty line here. Counting and addressing are different questions.
	lines := strings.Split(string(raw), "\n")

	start := 0
	if in.Offset != nil && *in.Offset > 0 {
		start = *in.Offset - 1
	}
	if start >= len(lines) {
		return Result{}, fmt.Errorf("read: offset %d is beyond the end of %s (%d lines total)",
			start+1, in.Path, len(lines))
	}
	firstLine := start + 1

	selected := lines[start:]
	limited := -1
	if in.Limit != nil {
		end := start + *in.Limit
		if end > len(lines) {
			end = len(lines)
		}
		if end < start {
			end = start
		}
		selected = lines[start:end]
		limited = end - start
	}

	cut := TruncateHead(strings.Join(selected, "\n"), r.Limits)
	return Result{Content: r.render(in.Path, lines, firstLine, limited, cut)}, nil
}

// render is what the model reads: the content, and when anything was withheld,
// the exact offset that continues from where this stopped.
//
// The continuation is the point. A model told only that output was truncated
// has to guess how to see the rest, and guessing wrong costs another call that
// returns the same prefix.
func (r *Read) render(path string, lines []string, firstLine, limited int, cut Truncation) string {
	total := len(lines)

	if cut.FirstLineExceedsLimit {
		// No whole line fits, so there is nothing to show and no offset that
		// would help. A byte-bounded shell read is the way through, and naming
		// it is more use than reporting the size alone.
		size := FormatSize(len(lines[firstLine-1]))
		return fmt.Sprintf("[Line %d is %s, exceeds the %s limit. Use bash: sed -n '%dp' %s | head -c %d]",
			firstLine, size, FormatSize(cut.MaxBytes), firstLine, path, cut.MaxBytes)
	}

	if cut.Truncated {
		lastLine := firstLine + cut.OutputLines - 1
		if cut.By == TruncatedByBytes {
			return fmt.Sprintf("%s\n\n[Showing lines %d-%d of %d (%s limit). Use offset=%d to continue.]",
				cut.Content, firstLine, lastLine, total, FormatSize(cut.MaxBytes), lastLine+1)
		}
		return fmt.Sprintf("%s\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]",
			cut.Content, firstLine, lastLine, total, lastLine+1)
	}

	// The limits were not reached, but the model's own limit stopped short of
	// the end. It asked for this, so it is not truncation — it still needs the
	// offset, because otherwise nothing says there is more.
	if limited >= 0 && firstLine-1+limited < total {
		remaining := total - (firstLine - 1 + limited)
		return fmt.Sprintf("%s\n\n[%d more lines in file. Use offset=%d to continue.]",
			cut.Content, remaining, firstLine+limited)
	}

	return cut.Content
}

// resolve turns the model's path into one on this machine.
//
// A leading ~ is expanded because a model writes paths the way a person does.
// The macOS filename fallbacks Pi tries when a path does not exist — the
// narrow no-break space before AM/PM, the decomposed and curly-quote variants
// of screenshot names — are NOT ported here, so a path that needs one fails as
// a missing file rather than silently resolving to a different one.
func (r *Read) resolve(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("read: cannot expand %q: %w", path, err)
		}
		return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/")), nil
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Join(r.Root, path), nil
}
