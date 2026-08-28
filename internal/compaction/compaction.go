// Package compaction shortens a conversation without losing what it was about.
//
// A long conversation eventually costs more to send than it is worth. The
// answer is not to drop the beginning — that loses the goal, the constraints
// and the decisions — but to replace it with a summary and keep the recent part
// verbatim. What is recent enough to keep is a token budget, and where exactly
// to cut is the part that has to be right: cutting between a tool call and its
// result leaves a request no provider will accept.
package compaction

import (
	"context"
	"fmt"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// DefaultKeepRecentTokens is how much of the end is kept verbatim.
const DefaultKeepRecentTokens = 20000

// EstimateTokens approximates what one message costs.
//
// Four characters to a token, counted over the text a provider would be sent —
// the content, the reasoning that has to go back with it, and each tool call's
// name and arguments. An estimate rather than a count because only the provider
// knows how it tokenises, and the decision this feeds is "roughly how much of
// the end to keep", which does not need to be exact.
func EstimateTokens(m ai.Message) int {
	chars := len(m.Content) + len(m.Reasoning)
	for _, call := range m.ToolCalls {
		chars += len(call.Name) + len(call.Args)
	}
	return (chars + 3) / 4
}

// EstimateConversation is what a whole conversation costs.
func EstimateConversation(messages []ai.Message) int {
	total := 0
	for _, m := range messages {
		total += EstimateTokens(m)
	}
	return total
}

// CutPoint is the index of the first message to keep verbatim.
//
// Walks back from the end adding up what each message costs, and once the
// budget is used, cuts at the nearest point it is SAFE to cut at.
//
// Safe means the start of a turn. A tool result must follow its call, so a cut
// between them produces a request the provider refuses; and an assistant reply
// separated from the question it answered reads as an answer to whatever came
// before. Pi will cut inside a turn and summarise the part it dropped
// separately; this does not, so it keeps slightly more than Pi would. Keeping
// too much costs tokens, and cutting in the wrong place costs the call.
func CutPoint(messages []ai.Message, keepRecentTokens int) int {
	if keepRecentTokens <= 0 {
		keepRecentTokens = DefaultKeepRecentTokens
	}
	starts := turnStarts(messages)
	if len(starts) == 0 {
		return 0
	}

	accumulated := 0
	for i := len(messages) - 1; i >= 0; i-- {
		cost := EstimateTokens(messages[i])
		if cost == 0 {
			continue
		}
		accumulated += cost
		if accumulated < keepRecentTokens {
			continue
		}
		// The first safe point at or after here. Going forward rather than back
		// keeps the budget an upper bound on what is retained.
		for _, start := range starts {
			if start >= i {
				return start
			}
		}
		// Everything before the last turn is over budget on its own, so keep
		// only that turn rather than nothing.
		return starts[len(starts)-1]
	}
	// The whole conversation fits in the budget, so the first turn is kept and
	// there is nothing before it to summarise.
	return starts[0]
}

// turnStarts are the indices where a turn begins: a message from the user.
//
// Not every message is one. An assistant reply, a tool call and its result all
// belong to the turn the user opened, and a conversation cut anywhere but a
// turn start is one that begins mid-exchange.
func turnStarts(messages []ai.Message) []int {
	var out []int
	for i, m := range messages {
		if m.Role == ai.RoleUser {
			out = append(out, i)
		}
	}
	return out
}

// Compactor turns the front of a conversation into a summary.
type Compactor struct {
	// Model answers the summarisation request. It is asked as an ordinary
	// model call, so it is billed and reported like any other.
	Model ai.Port

	// ModelName is what to ask for. Required, as it is for any request.
	ModelName string

	// KeepRecentTokens bounds what stays verbatim. Zero uses the default.
	KeepRecentTokens int

	// Instructions are added to the prompt when the user gave any.
	Instructions string
}

// ErrNothingToCompact reports a conversation that is already as short as this
// can make it.
//
// Typed, because a caller has to tell "there was nothing to do" from "the
// summariser failed": the first is worth saying once and the second is worth
// retrying.
type ErrNothingToCompact struct{ Why string }

func (e *ErrNothingToCompact) Error() string { return e.Why }

// Summarize shortens a conversation, matching the seam the runtime calls when a
// request is refused for exceeding the model's context.
//
// Returns the summary and the messages to keep verbatim after it.
func (c *Compactor) Summarize(ctx context.Context, truth []ai.Message) (string, []ai.Message, error) {
	cut := CutPoint(truth, c.KeepRecentTokens)
	if cut <= 0 {
		return "", nil, &ErrNothingToCompact{
			Why: "there is nothing before the recent part to summarise"}
	}

	head, tail := truth[:cut], truth[cut:]
	summary, err := c.summarise(ctx, head, "")
	if err != nil {
		return "", nil, err
	}
	// The tail is copied: the caller keeps its own slice, and a checkpoint that
	// shares memory with the live conversation is not a record of anything.
	kept := make([]ai.Message, len(tail))
	copy(kept, tail)
	return summary, kept, nil
}

// Recompact summarises again, folding what has happened since into an existing
// summary rather than starting over.
//
// Separate from Summarize because the instruction is different: the model is
// told to preserve what the previous summary says and add to it. Starting over
// would ask it to summarise a conversation it can no longer see, and the
// earliest goals and decisions would quietly disappear at the second compaction.
func (c *Compactor) Recompact(ctx context.Context, previous string, since []ai.Message) (string, []ai.Message, error) {
	cut := CutPoint(since, c.KeepRecentTokens)
	if cut <= 0 {
		return "", nil, &ErrNothingToCompact{
			Why: "nothing has happened since the last summary that is worth summarising"}
	}
	head, tail := since[:cut], since[cut:]
	summary, err := c.summarise(ctx, head, previous)
	if err != nil {
		return "", nil, err
	}
	kept := make([]ai.Message, len(tail))
	copy(kept, tail)
	return summary, kept, nil
}

func (c *Compactor) summarise(ctx context.Context, messages []ai.Message, previous string) (string, error) {
	if c.Model == nil {
		return "", fmt.Errorf("compaction: no model to summarise with")
	}
	base := summarizationPrompt
	if previous != "" {
		base = updateSummarizationPrompt
	}
	if strings.TrimSpace(c.Instructions) != "" {
		base = base + "\n\nAdditional focus: " + strings.TrimSpace(c.Instructions)
	}

	// The conversation is sent as TEXT inside one message rather than as the
	// messages themselves. Handed the real turns, a model continues them: it
	// answers the last question instead of summarising the exchange.
	var prompt strings.Builder
	prompt.WriteString("<conversation>\n")
	prompt.WriteString(serialize(messages))
	prompt.WriteString("\n</conversation>\n\n")
	if previous != "" {
		prompt.WriteString("<previous-summary>\n")
		prompt.WriteString(previous)
		prompt.WriteString("\n</previous-summary>\n\n")
	}
	prompt.WriteString(base)

	reply, err := c.Model.Generate(ctx, ai.Request{
		Model: c.ModelName,
		Messages: []ai.Message{
			{Role: ai.RoleSystem, Content: summarizationSystemPrompt},
			{Role: ai.RoleUser, Content: prompt.String()},
		},
	})
	if err != nil {
		return "", fmt.Errorf("compaction: %w", err)
	}
	summary := strings.TrimSpace(reply.Content)
	if summary == "" {
		// Recording an empty summary would replace the conversation with
		// nothing and report that as a success.
		return "", fmt.Errorf("compaction: the summariser returned nothing")
	}
	return summary, nil
}

// serialize renders a conversation as text.
func serialize(messages []ai.Message) string {
	var b strings.Builder
	for _, m := range messages {
		role := string(m.Role)
		if strings.TrimSpace(m.Content) != "" {
			fmt.Fprintf(&b, "%s: %s\n", role, m.Content)
		}
		for _, call := range m.ToolCalls {
			// Tool calls are part of what happened and often the only record of
			// which files were touched.
			fmt.Fprintf(&b, "%s: [called %s with %s]\n", role, call.Name, call.Args)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
