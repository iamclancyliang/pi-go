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
	if m.BashExecution != nil && m.BashExecution.ExcludeFromContext {
		return 0
	}
	chars := len(m.Reasoning)
	if m.BashExecution != nil {
		chars += len(ai.BashExecutionToText(*m.BashExecution))
	} else {
		chars += len(m.Content)
	}
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

// Cut says where to compact a conversation.
type Cut struct {
	// Keep is the index of the first message kept verbatim.
	Keep int

	// TurnStart is the index of the user message that opened the turn Keep
	// falls inside, and Split says whether it falls inside one at all. When it
	// does, the messages from TurnStart up to Keep are that turn's prefix,
	// summarised on their own, and only those before TurnStart are history.
	TurnStart int
	Split     bool
}

// FindCut walks back from the end adding up what each message costs and,
// once the budget is used, cuts at the nearest point it is SAFE to cut at —
// Pi's findCutPoint, at the level of messages.
//
// Safe means a user message or an assistant reply, never a tool result: a
// result must follow its call, and a conversation that begins with one is a
// request the provider refuses. Cutting at an assistant reply keeps the
// results that follow it. When that reply is inside a turn, the turn is
// split, and its dropped start is summarised separately so the kept part is
// not an answer to a question nobody can see.
func FindCut(messages []ai.Message, keepRecentTokens int) Cut {
	if keepRecentTokens <= 0 {
		keepRecentTokens = DefaultKeepRecentTokens
	}
	points := cutPoints(messages)
	if len(points) == 0 {
		return Cut{TurnStart: -1}
	}

	// The whole conversation fits in the budget unless the walk below finds
	// otherwise, so by default everything from the first cut point is kept.
	keep := points[0]
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
		// The first safe point at or after here. Going forward rather than
		// back keeps the budget an upper bound on what is retained.
		keep = points[len(points)-1]
		for _, p := range points {
			if p >= i {
				keep = p
				break
			}
		}
		// When nothing safe lies at or after here — the newest tool results
		// alone are over budget — the last safe point is kept rather than
		// everything. Pi keeps everything in that case, which leaves an
		// overflow with nothing to summarise and no way to recover.
		break
	}

	if startsTurn(messages[keep]) {
		return Cut{Keep: keep, TurnStart: -1}
	}
	for i := keep - 1; i >= 0; i-- {
		if startsTurn(messages[i]) {
			return Cut{Keep: keep, TurnStart: i, Split: true}
		}
	}
	return Cut{Keep: keep, TurnStart: -1}
}

// CutPoint is the index of the first message to keep verbatim.
func CutPoint(messages []ai.Message, keepRecentTokens int) int {
	return FindCut(messages, keepRecentTokens).Keep
}

// cutPoints are the indices a cut may land on: what the context shows as a
// user message, and assistant replies.
func cutPoints(messages []ai.Message) []int {
	var out []int
	for i, m := range messages {
		if startsTurn(m) || m.Role == ai.RoleAssistant {
			out = append(out, i)
		}
	}
	return out
}

// startsTurn reports a message that opens a turn: a user message the context
// shows. A shell record kept out of the context opens nothing, since the
// model never sees it.
func startsTurn(m ai.Message) bool {
	return m.Role == ai.RoleUser && (m.BashExecution == nil || !m.BashExecution.ExcludeFromContext)
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
	return c.compact(ctx, truth, "", "there is nothing before the recent part to summarise")
}

// Recompact summarises again, folding what has happened since into an existing
// summary rather than starting over.
//
// Separate from Summarize because the instruction is different: the model is
// told to preserve what the previous summary says and add to it. Starting over
// would ask it to summarise a conversation it can no longer see, and the
// earliest goals and decisions would quietly disappear at the second compaction.
func (c *Compactor) Recompact(ctx context.Context, previous string, since []ai.Message) (string, []ai.Message, error) {
	return c.compact(ctx, since, previous,
		"nothing has happened since the last summary that is worth summarising")
}

// compact summarises what comes before the cut and keeps the rest.
//
// A split turn costs two summaries, joined the way Pi joins them so that one
// checkpoint carries both: the history before the turn, then the turn's
// dropped start under its own heading.
func (c *Compactor) compact(ctx context.Context, messages []ai.Message, previous, nothing string) (string, []ai.Message, error) {
	cut := FindCut(messages, c.KeepRecentTokens)
	historyEnd := cut.Keep
	if cut.Split {
		historyEnd = cut.TurnStart
	}
	if historyEnd <= 0 && !cut.Split {
		return "", nil, &ErrNothingToCompact{Why: nothing}
	}

	var summary string
	if cut.Split {
		// Pi writes "No prior history." whenever nothing precedes the split
		// turn, dropping a previous summary on the floor. Keeping it is the
		// whole point of recompacting rather than starting over.
		history := "No prior history."
		if previous != "" {
			history = previous
		}
		if historyEnd > 0 {
			var err error
			if history, err = c.summarise(ctx, messages[:historyEnd], previous); err != nil {
				return "", nil, err
			}
		}
		prefix, err := c.summarisePrefix(ctx, messages[cut.TurnStart:cut.Keep])
		if err != nil {
			return "", nil, err
		}
		summary = history + "\n\n---\n\n**Turn Context (split turn):**\n\n" + prefix
	} else {
		var err error
		if summary, err = c.summarise(ctx, messages[:historyEnd], previous); err != nil {
			return "", nil, err
		}
	}

	// The tail is copied: the caller keeps its own slice, and a checkpoint that
	// shares memory with the live conversation is not a record of anything.
	kept := make([]ai.Message, len(messages)-cut.Keep)
	copy(kept, messages[cut.Keep:])
	return summary, kept, nil
}

// summarisePrefix summarises the dropped start of a split turn, for the part
// of it that is kept. Pi gives it no custom focus: what it needs is whatever
// makes the kept work make sense.
func (c *Compactor) summarisePrefix(ctx context.Context, messages []ai.Message) (string, error) {
	return c.ask(ctx, "<conversation>\n"+serialize(messages)+"\n</conversation>\n\n"+turnPrefixSummarizationPrompt)
}

func (c *Compactor) summarise(ctx context.Context, messages []ai.Message, previous string) (string, error) {
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
	return c.ask(ctx, prompt.String())
}

// ask sends one summarisation prompt and returns what came back.
func (c *Compactor) ask(ctx context.Context, prompt string) (string, error) {
	if c.Model == nil {
		return "", fmt.Errorf("compaction: no model to summarise with")
	}
	reply, err := c.Model.Generate(ctx, ai.Request{
		Model: c.ModelName,
		Messages: []ai.Message{
			{Role: ai.RoleSystem, Content: summarizationSystemPrompt},
			{Role: ai.RoleUser, Content: prompt},
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
		if m.BashExecution != nil {
			if m.BashExecution.ExcludeFromContext {
				continue
			}
			fmt.Fprintf(&b, "%s: %s\n", m.Role, ai.BashExecutionToText(*m.BashExecution))
			continue
		}
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
