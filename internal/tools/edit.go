package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Edit replaces exact regions of one file.
//
// The rule that makes multiple edits in one call safe: every oldText is matched
// against the ORIGINAL file, not against the result of the edits before it.
// Applying them to a mutating buffer instead would pass the obvious tests and
// silently diverge the moment two edits are near each other — the second would
// be matched against text the first had already changed.
type Edit struct {
	// Root is what a relative path is relative to.
	Root string
}

func (e *Edit) Name() string { return "edit" }

func (e *Edit) Description() string {
	return "Edit a single file using exact text replacement. Every edits[].oldText must match a " +
		"unique, non-overlapping region of the original file. If two changes affect the same block " +
		"or nearby lines, merge them into one edit instead of emitting overlapping edits. Do not " +
		"include large unchanged regions just to connect distant changes."
}

func (e *Edit) Execution() Execution {
	// Not read-only, and not repeatable: replaying an edit whose outcome was
	// lost would match against a file that may already carry the change, or a
	// later one, and either way the model never asked for the result.
	return Execution{}
}

// Prompt is what this tool tells the model about itself.
//
// The wording is Pi's, kept because it is what its models were given: a
// rephrasing is a different instruction, and the difference would show up as
// a behaviour change nobody could trace to a decision.
func (e *Edit) Prompt() Contribution {
	return Contribution{
		Snippet: "Make precise file edits with exact text replacement, including multiple disjoint edits in one call",
		Guidelines: []string{
			"Use edit for precise changes (edits[].oldText must match exactly)",
			"When changing multiple separate locations in one file, use one edit call with " +
				"multiple entries in edits[] instead of multiple edit calls",
			"Each edits[].oldText is matched against the original file, not after earlier " +
				"edits are applied. Do not emit overlapping or nested edits. Merge nearby " +
				"changes into one edit.",
			"Keep edits[].oldText as small as possible while still being unique in the file. " +
				"Do not pad with large unchanged regions.",
		},
	}
}

func (e *Edit) Parameters() *Schema {
	return &Schema{Parameters: []Parameter{
		{
			Name:        "path",
			Kind:        KindString,
			Description: "Path to the file to edit (relative or absolute)",
			Required:    true,
		},
		{
			Name:     "edits",
			Kind:     KindArray,
			Required: true,
			Description: "One or more targeted replacements. Each edit is matched against the " +
				"original file, not incrementally. Do not include overlapping or nested edits. If " +
				"two changes touch the same block or nearby lines, merge them into one edit instead.",
			Elements: &Value{Kind: KindObject, Fields: []Parameter{
				{
					Name:     "oldText",
					Kind:     KindString,
					Required: true,
					Description: "Exact text for one targeted replacement. It must be unique in " +
						"the original file and must not overlap with any other edits[].oldText in " +
						"the same call.",
				},
				{
					Name:        "newText",
					Kind:        KindString,
					Required:    true,
					Description: "Replacement text for this targeted edit.",
				},
			}},
		},
	}}
}

type replacement struct {
	OldText *string `json:"oldText"`
	NewText *string `json:"newText"`
}

type editArgs struct {
	Path  string        `json:"path"`
	Edits []replacement `json:"edits"`
}

// matched is one replacement located in the original content.
type matched struct {
	index  int // where in edits[] it came from, for the message a model reads
	at     int
	length int
	with   string
}

func (e *Edit) Call(ctx context.Context, args string) (Result, error) {
	var in editArgs
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return Result{}, fmt.Errorf("edit: invalid arguments %q: %w", args, err)
	}
	if in.Path == "" {
		return Result{}, fmt.Errorf("edit: path is required")
	}
	if len(in.Edits) == 0 {
		return Result{}, fmt.Errorf("edit: edits must contain at least one replacement")
	}
	for i, r := range in.Edits {
		if r.OldText == nil || r.NewText == nil {
			return Result{}, fmt.Errorf("edit: edits[%d] must carry both oldText and newText", i)
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	path, err := resolvePath(e.Root, in.Path)
	if err != nil {
		return Result{}, fmt.Errorf("edit: %s: %w", in.Path, err)
	}

	err = fileMutations.do(path, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("could not edit file: %s: %w", in.Path, err)
		}

		// The byte-order mark is stripped before matching and put back after:
		// a model will not have included an invisible character in oldText, and
		// dropping it would rewrite the file's encoding as a side effect of an
		// unrelated edit.
		bom, text := stripBOM(string(raw))
		ending := detectLineEnding(text)
		normalized := normalizeToLF(text)

		edited, err := applyEdits(normalized, in.Edits, in.Path)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(bom+restoreLineEndings(edited, ending)), 0o644)
	})
	if err != nil {
		return Result{}, fmt.Errorf("edit: %w", err)
	}

	return Result{Content: fmt.Sprintf("Successfully replaced %d block(s) in %s",
		len(in.Edits), in.Path)}, nil
}

// applyEdits locates every replacement in the original content, refuses the
// combinations that cannot mean one thing, and applies what is left.
func applyEdits(content string, edits []replacement, path string) (string, error) {
	total := len(edits)
	located := make([]matched, 0, total)

	for i, r := range edits {
		oldText := normalizeToLF(*r.OldText)
		newText := normalizeToLF(*r.NewText)
		if oldText == "" {
			// An empty match is at every position at once, so "replace it" has
			// no single meaning. Refused rather than resolved by a convention
			// nobody would predict.
			if total == 1 {
				return "", fmt.Errorf("oldText must not be empty in %s", path)
			}
			return "", fmt.Errorf("edits[%d].oldText must not be empty in %s", i, path)
		}

		at := strings.Index(content, oldText)
		if at < 0 {
			if total == 1 {
				return "", fmt.Errorf("could not find the exact text in %s; the old text must "+
					"match exactly including all whitespace and newlines", path)
			}
			return "", fmt.Errorf("could not find edits[%d] in %s; the oldText must match exactly "+
				"including all whitespace and newlines", i, path)
		}
		// Counted on the ORIGINAL content, like the match itself: a text that
		// appears twice identifies no single region, and picking the first
		// would edit somewhere the model did not look at.
		if n := strings.Count(content, oldText); n > 1 {
			if total == 1 {
				return "", fmt.Errorf("found %d occurrences of the text in %s; the text must be "+
					"unique, so provide more context to make it unique", n, path)
			}
			return "", fmt.Errorf("found %d occurrences of edits[%d] in %s; each oldText must be "+
				"unique, so provide more context to make it unique", n, i, path)
		}
		located = append(located, matched{index: i, at: at, length: len(oldText), with: newText})
	}

	// Sorted by position so an overlap is a comparison with the neighbour
	// rather than a search. Ties keep the earlier edit first, which makes the
	// message name them in the order the model wrote them.
	sort.SliceStable(located, func(a, b int) bool { return located[a].at < located[b].at })
	for i := 1; i < len(located); i++ {
		prev, cur := located[i-1], located[i]
		if prev.at+prev.length > cur.at {
			return "", fmt.Errorf("edits[%d] and edits[%d] overlap in %s; merge them into one edit "+
				"or target disjoint regions", prev.index, cur.index, path)
		}
	}

	// Applied from the end, so each splice leaves the offsets of the ones
	// before it untouched. Going forwards would need every later position
	// adjusted by the length change of every earlier edit.
	edited := content
	for i := len(located) - 1; i >= 0; i-- {
		m := located[i]
		edited = edited[:m.at] + m.with + edited[m.at+m.length:]
	}

	if edited == content {
		// Every edit matched and nothing changed, which means each newText
		// equals its oldText. Reported rather than written, because a silent
		// success here tells the model its change landed.
		if total == 1 {
			return "", fmt.Errorf("no changes made to %s; the replacement produced identical "+
				"content, which may mean the text does not exist as expected", path)
		}
		return "", fmt.Errorf("no changes made to %s; the replacements produced identical content", path)
	}
	return edited, nil
}

// stripBOM separates a leading byte-order mark from the text after it.
func stripBOM(content string) (string, string) {
	// U+FEFF, written as an escape: a literal one here is invisible in a diff
	// and illegal in Go source outside the first byte of a file.
	const bom = "\ufeff"
	if strings.HasPrefix(content, bom) {
		return bom, strings.TrimPrefix(content, bom)
	}
	return "", content
}

// detectLineEnding reports the ending the FIRST line break in the file uses.
//
// One answer for the whole file, because the file is rewritten with one. A file
// that mixes them keeps whichever came first, which is the convention its next
// line was most likely written with.
func detectLineEnding(content string) string {
	lf := strings.Index(content, "\n")
	if lf < 0 {
		return "\n"
	}
	crlf := strings.Index(content, "\r\n")
	if crlf < 0 || crlf > lf {
		return "\n"
	}
	return "\r\n"
}

func normalizeToLF(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

func restoreLineEndings(text, ending string) string {
	if ending != "\r\n" {
		return text
	}
	return strings.ReplaceAll(text, "\n", "\r\n")
}
