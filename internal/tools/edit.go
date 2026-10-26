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

	var details EditDetails
	err = fileMutations.do(path, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		planned, err := planEdit(path, in)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		details = planned.details()
		return os.WriteFile(path, []byte(planned.bom+restoreLineEndings(planned.edited, planned.ending)), 0o644)
	})
	if err != nil {
		return Result{}, fmt.Errorf("edit: %w", err)
	}

	return Result{
		Content: fmt.Sprintf("Successfully replaced %d block(s) in %s", len(in.Edits), in.Path),
		Details: details,
	}, nil
}

// EditDetails is what an edit changed, for display.
type EditDetails struct {
	// Diff is the change with line numbers and a little context, in the
	// form the interface draws: "+12 added", "-12 removed", " 12 context",
	// and " ..." where unchanged lines are left out.
	Diff string
	// FirstChangedLine is the first changed line in the new file, or 0.
	FirstChangedLine int
}

type plannedEdit struct {
	bom, ending        string
	normalized, edited string
}

func (p plannedEdit) details() EditDetails {
	diff, first := DiffString(p.normalized, p.edited, 4)
	return EditDetails{Diff: diff, FirstChangedLine: first}
}

// planEdit reads the file and works out the edit without writing it.
func planEdit(path string, in editArgs) (plannedEdit, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return plannedEdit{}, fmt.Errorf("could not edit file: %s: %w", in.Path, err)
	}
	// The byte-order mark is stripped before matching and put back after: a
	// model will not have included an invisible character in oldText, and
	// dropping it would rewrite the file's encoding as a side effect of an
	// unrelated edit.
	bom, text := stripBOM(string(raw))
	p := plannedEdit{bom: bom, ending: detectLineEnding(text), normalized: normalizeToLF(text)}
	if p.edited, err = applyEdits(p.normalized, in.Edits, in.Path); err != nil {
		return plannedEdit{}, err
	}
	return p, nil
}

// PreviewEdit works out what an edit call would change, without changing
// anything, so the interface can show the diff while the call is pending.
// root is the directory relative paths resolve against.
func PreviewEdit(root, args string) (EditDetails, error) {
	var in editArgs
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return EditDetails{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if in.Path == "" || len(in.Edits) == 0 {
		return EditDetails{}, fmt.Errorf("edit: path and edits are required")
	}
	for i, r := range in.Edits {
		if r.OldText == nil || r.NewText == nil {
			return EditDetails{}, fmt.Errorf("edits[%d] must carry both oldText and newText", i)
		}
	}
	path, err := resolvePath(root, in.Path)
	if err != nil {
		return EditDetails{}, fmt.Errorf("%s: %w", in.Path, err)
	}
	planned, err := planEdit(path, in)
	if err != nil {
		return EditDetails{}, err
	}
	return planned.details(), nil
}

// applyEdits locates every replacement in the original content, refuses the
// combinations that cannot mean one thing, and applies what is left.
//
// Every edit is matched exactly when it can be. Only when some edit cannot be
// found that way does the whole call move into the normalised view of the
// file (see normalizeForFuzzyMatch), so all its edits are still located in one
// coherent text; the result is then mapped back so that lines no edit touched
// keep their original bytes.
func applyEdits(content string, edits []replacement, path string) (string, error) {
	total := len(edits)
	olds := make([]string, total)
	news := make([]string, total)
	for i, r := range edits {
		olds[i] = normalizeToLF(*r.OldText)
		news[i] = normalizeToLF(*r.NewText)
		if olds[i] == "" {
			// An empty match is at every position at once, so "replace it" has
			// no single meaning. Refused rather than resolved by a convention
			// nobody would predict.
			if total == 1 {
				return "", fmt.Errorf("oldText must not be empty in %s", path)
			}
			return "", fmt.Errorf("edits[%d].oldText must not be empty in %s", i, path)
		}
	}

	normalized := normalizeForFuzzyMatch(content)
	fuzzy := false
	for _, old := range olds {
		if !strings.Contains(content, old) && strings.Contains(normalized, normalizeForFuzzyMatch(old)) {
			fuzzy = true
			break
		}
	}
	base := content
	if fuzzy {
		base = normalized
	}

	located := make([]matched, 0, total)
	for i, old := range olds {
		at, length := strings.Index(base, old), len(old)
		if at < 0 {
			fuzzyOld := normalizeForFuzzyMatch(old)
			at, length = strings.Index(normalized, fuzzyOld), len(fuzzyOld)
			if !fuzzy {
				// Outside normalised space a fuzzy position indexes a text
				// this call is not editing.
				at = -1
			}
		}
		if at < 0 {
			if total == 1 {
				return "", fmt.Errorf("could not find the exact text in %s; the old text must "+
					"match exactly including all whitespace and newlines", path)
			}
			return "", fmt.Errorf("could not find edits[%d] in %s; the oldText must match exactly "+
				"including all whitespace and newlines", i, path)
		}
		// Counted in the normalised view even when the match itself was exact:
		// text that differs from another region only by trailing whitespace or
		// quote style identifies no single place either, and picking the exact
		// one would edit somewhere the model may not have meant.
		if n := strings.Count(normalized, normalizeForFuzzyMatch(old)); n > 1 {
			if total == 1 {
				return "", fmt.Errorf("found %d occurrences of the text in %s; the text must be "+
					"unique, so provide more context to make it unique", n, path)
			}
			return "", fmt.Errorf("found %d occurrences of edits[%d] in %s; each oldText must be "+
				"unique, so provide more context to make it unique", n, i, path)
		}
		located = append(located, matched{index: i, at: at, length: length, with: news[i]})
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

	// Positions are in base, so in fuzzy mode they index the normalised text
	// and must never be spliced into the original directly.
	edited := content
	if fuzzy {
		var err error
		if edited, err = applyPreservingUnchangedLines(content, base, located); err != nil {
			return "", err
		}
	} else {
		edited = splice(content, located, 0)
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
