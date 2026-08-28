package compaction_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/compaction"
)

func user(text string) ai.Message      { return ai.Message{Role: ai.RoleUser, Content: text} }
func assistant(text string) ai.Message { return ai.Message{Role: ai.RoleAssistant, Content: text} }
func toolResult(id, text string) ai.Message {
	return ai.Message{Role: ai.RoleTool, ToolCallID: id, Content: text}
}
func calling(id, name, args string) ai.Message {
	return ai.Message{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{{ID: id, Name: name, Args: args}}}
}

// TestACutNeverSeparatesAToolCallFromItsResult is the constraint that costs a
// call when it is broken: a provider refuses a conversation whose first message
// is a tool result with nothing to answer.
func TestACutNeverSeparatesAToolCallFromItsResult(t *testing.T) {
	long := strings.Repeat("x", 4000) // 1000 tokens each
	var messages []ai.Message
	for i := 0; i < 30; i++ {
		messages = append(messages,
			user("question "+long),
			calling("call-1", "read", `{"path":"a"}`),
			toolResult("call-1", "contents "+long),
			assistant("answer "+long))
	}

	for _, budget := range []int{500, 2000, 5000, 20000, 100000} {
		cut := compaction.CutPoint(messages, budget)
		if cut < 0 || cut >= len(messages) {
			t.Fatalf("budget %d cut at %d, outside the conversation", budget, cut)
		}
		if got := messages[cut].Role; got != ai.RoleUser {
			t.Fatalf("budget %d cut at a %s message; a kept conversation must start with a question",
				budget, got)
		}
	}
}

// TestTheBudgetBoundsWhatIsKept, or compaction does not shorten anything.
func TestTheBudgetBoundsWhatIsKept(t *testing.T) {
	long := strings.Repeat("x", 4000)
	var messages []ai.Message
	for i := 0; i < 40; i++ {
		messages = append(messages, user("q "+long), assistant("a "+long))
	}

	cut := compaction.CutPoint(messages, 5000)
	kept := compaction.EstimateConversation(messages[cut:])
	whole := compaction.EstimateConversation(messages)
	if kept >= whole {
		t.Fatalf("nothing was dropped: kept %d of %d tokens", kept, whole)
	}
	// The budget is an upper bound on what is retained, give or take the turn
	// the cut lands on.
	if kept > 5000*3 {
		t.Fatalf("a 5000-token budget kept %d tokens", kept)
	}
}

// TestAShortConversationIsLeftAlone. Summarising two turns costs a model call
// and gains nothing.
func TestAShortConversationIsLeftAlone(t *testing.T) {
	messages := []ai.Message{user("hello"), assistant("hi")}
	if cut := compaction.CutPoint(messages, compaction.DefaultKeepRecentTokens); cut != 0 {
		t.Fatalf("a short conversation was cut at %d", cut)
	}

	c := &compaction.Compactor{Model: &refusingModel{}, ModelName: "m"}
	_, _, err := c.Summarize(context.Background(), messages)
	var nothing *compaction.ErrNothingToCompact
	if !errors.As(err, &nothing) {
		t.Fatalf("compacting a short conversation returned %v, want a typed nothing-to-do", err)
	}
}

// TestTheConversationIsSentAsTextNotAsTurns. Handed the real messages, a model
// continues them — it answers the last question instead of summarising.
func TestTheConversationIsSentAsTextNotAsTurns(t *testing.T) {
	long := strings.Repeat("x", 4000)
	var messages []ai.Message
	for i := 0; i < 30; i++ {
		messages = append(messages, user("q "+long), assistant("a "+long))
	}

	captured := &capturingModel{reply: "## Goal\nsomething"}
	c := &compaction.Compactor{Model: captured, ModelName: "m", KeepRecentTokens: 2000}
	if _, _, err := c.Summarize(context.Background(), messages); err != nil {
		t.Fatalf("Summarize: %v", err)
	}

	sent := captured.request
	// One system message and one user message: the conversation is inside the
	// second, not spread across many.
	if len(sent.Messages) != 2 {
		t.Fatalf("the summariser was sent %d messages, want 2", len(sent.Messages))
	}
	if sent.Messages[0].Role != ai.RoleSystem || sent.Messages[1].Role != ai.RoleUser {
		t.Fatalf("the summariser was sent %v", sent.Messages)
	}
	if !strings.Contains(sent.Messages[1].Content, "<conversation>") {
		t.Fatalf("the conversation was not wrapped as text:\n%s", sent.Messages[1].Content)
	}
	if !strings.Contains(sent.Messages[1].Content, "## Goal") {
		t.Fatalf("the prompt does not ask for the checkpoint format")
	}
}

// TestToolCallsSurviveIntoTheSummaryPrompt: they are often the only record of
// which files were touched, and a summary that lost them cannot say what was
// done.
func TestToolCallsSurviveIntoTheSummaryPrompt(t *testing.T) {
	long := strings.Repeat("x", 4000)
	var messages []ai.Message
	for i := 0; i < 30; i++ {
		messages = append(messages,
			user("q "+long),
			calling("c", "edit", `{"path":"important.go"}`),
			toolResult("c", "done"),
			assistant("a "+long))
	}
	captured := &capturingModel{reply: "summary"}
	c := &compaction.Compactor{Model: captured, ModelName: "m", KeepRecentTokens: 2000}
	if _, _, err := c.Summarize(context.Background(), messages); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if !strings.Contains(captured.request.Messages[1].Content, "important.go") {
		t.Fatal("the tool calls were dropped from what the summariser was shown")
	}
}

// TestAnEmptySummaryIsRefused. Recording one would replace the conversation
// with nothing and report it as a success.
func TestAnEmptySummaryIsRefused(t *testing.T) {
	long := strings.Repeat("x", 4000)
	var messages []ai.Message
	for i := 0; i < 30; i++ {
		messages = append(messages, user("q "+long), assistant("a "+long))
	}
	c := &compaction.Compactor{Model: &capturingModel{reply: "   "}, ModelName: "m", KeepRecentTokens: 2000}
	if _, _, err := c.Summarize(context.Background(), messages); err == nil {
		t.Fatal("an empty summary was accepted")
	}
}

// TestRecompactingIsToldToPreserveWhatCameBefore. Starting over would ask the
// model to summarise a conversation it can no longer see, and the earliest
// goals would quietly disappear at the second compaction.
func TestRecompactingIsToldToPreserveWhatCameBefore(t *testing.T) {
	long := strings.Repeat("x", 4000)
	var since []ai.Message
	for i := 0; i < 30; i++ {
		since = append(since, user("q "+long), assistant("a "+long))
	}
	captured := &capturingModel{reply: "updated"}
	c := &compaction.Compactor{Model: captured, ModelName: "m", KeepRecentTokens: 2000}
	if _, _, err := c.Recompact(context.Background(), "the earlier summary", since); err != nil {
		t.Fatalf("Recompact: %v", err)
	}
	prompt := captured.request.Messages[1].Content
	if !strings.Contains(prompt, "<previous-summary>") || !strings.Contains(prompt, "the earlier summary") {
		t.Fatalf("the previous summary was not carried in:\n%s", prompt)
	}
	if !strings.Contains(prompt, "PRESERVE all existing information") {
		t.Fatalf("the update instruction was not used:\n%s", prompt)
	}
}

// TestTheKeptTailIsACopy, because a checkpoint that shares memory with the live
// conversation is not a record of anything.
func TestTheKeptTailIsACopy(t *testing.T) {
	long := strings.Repeat("x", 4000)
	var messages []ai.Message
	for i := 0; i < 30; i++ {
		messages = append(messages, user("q "+long), assistant("a "+long))
	}
	c := &compaction.Compactor{Model: &capturingModel{reply: "s"}, ModelName: "m", KeepRecentTokens: 2000}
	_, kept, err := c.Summarize(context.Background(), messages)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(kept) == 0 {
		t.Fatal("nothing was kept")
	}
	original := kept[0].Content
	messages[len(messages)-len(kept)].Content = "rewritten"
	if kept[0].Content != original {
		t.Fatal("editing the conversation changed what the checkpoint kept")
	}
}

type capturingModel struct {
	reply   string
	request ai.Request
}

func (c *capturingModel) Generate(_ context.Context, req ai.Request) (ai.Response, error) {
	c.request = req
	return ai.Response{Content: c.reply}, nil
}

type refusingModel struct{}

func (refusingModel) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, errors.New("this model must not be reached")
}
