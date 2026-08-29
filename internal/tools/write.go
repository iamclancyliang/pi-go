package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Write creates a file or replaces one entirely.
//
// Whole-file only. A tool that could also patch would have two ways to change a
// file and two sets of failures; edit owns the targeted change, and this owns
// the case where the previous contents do not matter.
type Write struct {
	// Root is what a relative path is relative to.
	Root string
}

func (w *Write) Name() string { return "write" }

func (w *Write) Description() string {
	return "Write content to a file. Creates the file if it doesn't exist, overwrites if it does. " +
		"Automatically creates parent directories."
}

func (w *Write) Execution() Execution {
	// Not read-only, so a policy that denies mutation stops it before it runs.
	//
	// Not repeatable either. Replaying a write whose outcome was lost would
	// overwrite whatever stands there now, which may be a later change rather
	// than the half-finished state the crash left. The two mistakes are not
	// symmetric: refusing to repeat leaves visible work undone, while repeating
	// destroys something silently.
	//
	// Not Sequential: that would serialise every call in the round, including
	// ones touching unrelated files. Two writes to the SAME file are serialised
	// by the per-file lock instead, which is the narrower answer.
	return Execution{}
}

// Prompt is what this tool tells the model about itself.
//
// The wording is Pi's, kept because it is what its models were given: a
// rephrasing is a different instruction, and the difference would show up as
// a behaviour change nobody could trace to a decision.
func (w *Write) Prompt() Contribution {
	return Contribution{
		Snippet:    "Create or overwrite files",
		Guidelines: []string{"Use write only for new files or complete rewrites."},
	}
}

func (w *Write) Parameters() *Schema {
	return &Schema{Parameters: []Parameter{
		{
			Name:        "path",
			Kind:        KindString,
			Description: "Path to the file to write (relative or absolute)",
			Required:    true,
		},
		{
			Name:        "content",
			Kind:        KindString,
			Description: "Content to write to the file",
			Required:    true,
		},
	}}
}

type writeArgs struct {
	Path string `json:"path"`
	// A pointer distinguishes a request to write an empty file from a call that
	// forgot the argument. Truncating a file because a field was missing is not
	// something to infer.
	Content *string `json:"content"`
}

func (w *Write) Call(ctx context.Context, args string) (Result, error) {
	var in writeArgs
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return Result{}, fmt.Errorf("write: invalid arguments %q: %w", args, err)
	}
	if in.Path == "" {
		return Result{}, fmt.Errorf("write: path is required")
	}
	if in.Content == nil {
		return Result{}, fmt.Errorf("write: content is required; pass an empty string to write an empty file")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	path, err := resolvePath(w.Root, in.Path)
	if err != nil {
		return Result{}, fmt.Errorf("write: %s: %w", in.Path, err)
	}
	content := *in.Content

	err = fileMutations.do(path, func() error {
		// Checked inside the lock, so a cancelled call releases it rather than
		// abandoning a half-finished write with the lock still held.
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(content), 0o644)
	})
	if err != nil {
		return Result{}, fmt.Errorf("write: %s: %w", in.Path, err)
	}

	// The path the model wrote, not the resolved one: it is the name the model
	// will use again in the same conversation.
	return Result{Content: fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), in.Path)}, nil
}
