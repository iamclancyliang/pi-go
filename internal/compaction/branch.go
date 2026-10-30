package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// DefaultBranchReserveTokens is what is held back from a model's window for the
// prompt and the reply when a branch is summarised — Pi's branchSummary.reserveTokens.
const DefaultBranchReserveTokens = 16384

// defaultBranchWindow is assumed for a model with no recorded window, as Pi assumes
// it for one whose catalogue entry has none.
const defaultBranchWindow = 128000

// BranchEntry is one thing recorded on the branch being left.
type BranchEntry struct {
	// Message is what it says. A summary is passed as the message it reaches the
	// model as.
	Message ai.Message

	// IsSummary marks a compaction's or an earlier branch's summary, which is
	// fitted in even when the budget is nearly gone.
	IsSummary bool

	// ReadFiles and ModifiedFiles are what an earlier branch summary recorded, so
	// a line containing it keeps track of them.
	ReadFiles     []string
	ModifiedFiles []string
}

// BranchResult is a branch's summary, and the files it touched.
type BranchResult struct {
	Summary       string
	ReadFiles     []string
	ModifiedFiles []string
}

const branchSummaryPreamble = `The user explored a different conversation branch before returning here.
Summary of that exploration:

`

const branchSummaryPrompt = `Create a structured summary of this conversation branch for context when returning later.

Use this EXACT format:

## Goal
[What was the user trying to accomplish in this branch?]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Work that was started but not finished]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [What should happen next to continue this work]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const branchSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

// prepareBranch walks from newest to oldest, keeping messages until the budget is
// spent, so a long branch keeps its most recent part — Pi's prepareBranchEntries.
// Tool results are left out: what they did is in the calls. The file lists come
// from every entry, including those that did not fit.
func prepareBranch(entries []BranchEntry, budget int) (messages []ai.Message, read, modified map[string]bool) {
	read, modified = map[string]bool{}, map[string]bool{}
	for _, entry := range entries {
		for _, f := range entry.ReadFiles {
			read[f] = true
		}
		for _, f := range entry.ModifiedFiles {
			modified[f] = true
		}
	}
	total := 0
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.Message.Role == ai.RoleTool {
			continue
		}
		collectFileOps(entry.Message, read, modified)
		tokens := EstimateTokens(entry.Message)
		if budget > 0 && total+tokens > budget {
			if entry.IsSummary && float64(total) < float64(budget)*0.9 {
				messages = append(messages, entry.Message)
				total += tokens
			}
			break
		}
		messages = append(messages, entry.Message)
		total += tokens
	}
	slices.Reverse(messages)
	return messages, read, modified
}

// collectFileOps notes what an assistant's tool calls read and changed.
func collectFileOps(m ai.Message, read, modified map[string]bool) {
	if m.Role != ai.RoleAssistant {
		return
	}
	for _, call := range m.ToolCalls {
		var args struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(call.Args), &args) != nil || args.Path == "" {
			continue
		}
		switch call.Name {
		case "read":
			read[args.Path] = true
		case "write", "edit":
			modified[args.Path] = true
		}
	}
}

// fileLists is Pi's computeFileLists: anything changed counts as modified however
// often it was read, and both lists are sorted.
func fileLists(read, modified map[string]bool) (readOnly, changed []string) {
	for f := range modified {
		changed = append(changed, f)
	}
	for f := range read {
		if !modified[f] {
			readOnly = append(readOnly, f)
		}
	}
	slices.Sort(readOnly)
	slices.Sort(changed)
	return readOnly, changed
}

// formatFileOps is Pi's formatFileOperations.
func formatFileOps(readOnly, changed []string) string {
	var sections []string
	if len(readOnly) > 0 {
		sections = append(sections, "<read-files>\n"+strings.Join(readOnly, "\n")+"\n</read-files>")
	}
	if len(changed) > 0 {
		sections = append(sections, "<modified-files>\n"+strings.Join(changed, "\n")+"\n</modified-files>")
	}
	if len(sections) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(sections, "\n\n")
}

// SummarizeBranch says what a branch that is being left was about, so that coming
// back to an earlier point does not lose it — Pi's generateBranchSummary.
// contextWindow is the model's, zero when none is recorded; reserveTokens is held
// back from it, zero meaning the default.
func (c *Compactor) SummarizeBranch(ctx context.Context, entries []BranchEntry, instructions string,
	contextWindow, reserveTokens int) (BranchResult, error) {

	if contextWindow <= 0 {
		contextWindow = defaultBranchWindow
	}
	if reserveTokens <= 0 {
		reserveTokens = DefaultBranchReserveTokens
	}
	messages, read, modified := prepareBranch(entries, contextWindow-reserveTokens)
	if len(messages) == 0 {
		return BranchResult{Summary: "No content to summarize"}, nil
	}

	prompt := branchSummaryPrompt
	if focus := strings.TrimSpace(instructions); focus != "" {
		prompt += "\n\nAdditional focus: " + focus
	}
	text := "<conversation>\n" + serialize(messages) + "\n</conversation>\n\n" + prompt

	if c.Model == nil {
		return BranchResult{}, fmt.Errorf("compaction: no model to summarise with")
	}
	reply, err := c.Model.Generate(ctx, ai.Request{
		Model: c.ModelName,
		Messages: []ai.Message{
			{Role: ai.RoleSystem, Content: branchSystemPrompt},
			{Role: ai.RoleUser, Content: text},
		},
	})
	if err != nil {
		return BranchResult{}, fmt.Errorf("compaction: %w", err)
	}
	readOnly, changed := fileLists(read, modified)
	return BranchResult{
		Summary:       branchSummaryPreamble + reply.Content + formatFileOps(readOnly, changed),
		ReadFiles:     readOnly,
		ModifiedFiles: changed,
	}, nil
}
