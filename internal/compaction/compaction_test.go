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
// is a tool result with nothing to answer. A cut may land on an assistant
// reply, inside a turn; never on a result.
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
		if got := messages[cut].Role; got == ai.RoleTool {
			t.Fatalf("budget %d cut at a tool result, separating it from the call it answers", budget)
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

// recordingModel answers every request with the next reply and keeps them all.
type recordingModel struct {
	replies  []string
	requests []ai.Request
}

func (r *recordingModel) Generate(_ context.Context, req ai.Request) (ai.Response, error) {
	r.requests = append(r.requests, req)
	reply := "summary"
	if len(r.requests) <= len(r.replies) {
		reply = r.replies[len(r.requests)-1]
	}
	return ai.Response{Content: reply}, nil
}

// oneLongTurn is a single question followed by rounds of tool work, each far
// larger than a small budget: the shape that used to leave nothing to
// summarise, because the only turn start is the first message.
func oneLongTurn(rounds int) []ai.Message {
	long := strings.Repeat("x", 4000) // 1000 tokens each
	messages := []ai.Message{user("refactor the parser")}
	for i := 0; i < rounds; i++ {
		id := "call-" + string(rune('a'+i))
		messages = append(messages, calling(id, "read", `{"path":"p.go"}`), toolResult(id, long))
	}
	return append(messages, assistant("done"))
}

// TestALongSingleTurnIsSplitRatherThanLeftWhole. With a turn start the only
// place to cut, one long turn could not be compacted at all, and an overflow
// inside it could not be recovered. Pi cuts inside the turn and summarises
// the part it dropped with its own prompt.
func TestALongSingleTurnIsSplitRatherThanLeftWhole(t *testing.T) {
	messages := oneLongTurn(10)
	cut := compaction.FindCut(messages, 2500)
	if !cut.Split || cut.TurnStart != 0 || cut.Keep <= 0 {
		t.Fatalf("one long turn was not split: %+v", cut)
	}
	if messages[cut.Keep].Role != ai.RoleAssistant {
		t.Fatalf("the split kept from a %s message", messages[cut.Keep].Role)
	}

	model := &recordingModel{replies: []string{"PREFIX-SUMMARY"}}
	c := &compaction.Compactor{Model: model, ModelName: "m", KeepRecentTokens: 2500}
	summary, kept, err := c.Summarize(context.Background(), messages)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	// Nothing came before the turn, so only the prefix is summarised: one call.
	if len(model.requests) != 1 {
		t.Fatalf("%d summarisation calls, want 1", len(model.requests))
	}
	prompt := model.requests[0].Messages[1].Content
	if !strings.Contains(prompt, "This is the PREFIX of a turn that was too large to keep") ||
		!strings.Contains(prompt, "refactor the parser") {
		t.Fatalf("the prefix was not summarised with the turn-prefix prompt:\n%s", prompt)
	}
	want := "No prior history.\n\n---\n\n**Turn Context (split turn):**\n\nPREFIX-SUMMARY"
	if summary != want {
		t.Fatalf("summary = %q, want %q", summary, want)
	}
	if len(kept) != len(messages)-cut.Keep || kept[0].Role != ai.RoleAssistant {
		t.Fatalf("kept %d messages starting with %v", len(kept), kept[0].Role)
	}
}

// TestASplitSummarisesTheHistoryAndThePrefixSeparately, then joins them the
// way Pi does, so one checkpoint carries both.
func TestASplitSummarisesTheHistoryAndThePrefixSeparately(t *testing.T) {
	long := strings.Repeat("x", 4000)
	messages := append([]ai.Message{user("earlier question " + long), assistant("earlier answer " + long)},
		oneLongTurn(10)...)

	model := &recordingModel{replies: []string{"HISTORY-SUMMARY", "PREFIX-SUMMARY"}}
	c := &compaction.Compactor{Model: model, ModelName: "m", KeepRecentTokens: 2500, Instructions: "focus on the parser"}
	summary, _, err := c.Summarize(context.Background(), messages)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(model.requests) != 2 {
		t.Fatalf("%d summarisation calls, want one for the history and one for the prefix", len(model.requests))
	}
	history, prefix := model.requests[0].Messages[1].Content, model.requests[1].Messages[1].Content
	if !strings.Contains(history, "earlier question") || strings.Contains(history, "refactor the parser") {
		t.Fatalf("the history summary saw the wrong messages:\n%s", history)
	}
	if !strings.Contains(history, "focus on the parser") {
		t.Fatal("the user's focus did not reach the history summary")
	}
	if !strings.Contains(prefix, "refactor the parser") || strings.Contains(prefix, "earlier question") {
		t.Fatalf("the prefix summary saw the wrong messages:\n%s", prefix)
	}
	if summary != "HISTORY-SUMMARY\n\n---\n\n**Turn Context (split turn):**\n\nPREFIX-SUMMARY" {
		t.Fatalf("summary = %q", summary)
	}
}

// TestASplitRecompactionKeepsThePreviousSummary when nothing new came before
// the split turn. Pi writes "No prior history." there and loses every goal
// the previous summary held; keeping it is what recompaction is for.
func TestASplitRecompactionKeepsThePreviousSummary(t *testing.T) {
	model := &recordingModel{replies: []string{"PREFIX-SUMMARY"}}
	c := &compaction.Compactor{Model: model, ModelName: "m", KeepRecentTokens: 2500}
	summary, _, err := c.Recompact(context.Background(), "THE EARLIER SUMMARY", oneLongTurn(10))
	if err != nil {
		t.Fatalf("Recompact: %v", err)
	}
	if !strings.HasPrefix(summary, "THE EARLIER SUMMARY\n\n---\n\n**Turn Context (split turn):**") {
		t.Fatalf("the previous summary was lost: %q", summary)
	}
}

// TestASplitTurnNeverKeepsAToolResultFirst, at every budget: the property the
// split has to keep that a turn-start cut kept for free.
func TestASplitTurnNeverKeepsAToolResultFirst(t *testing.T) {
	messages := oneLongTurn(12)
	for budget := 100; budget <= 20000; budget += 300 {
		cut := compaction.FindCut(messages, budget)
		if messages[cut.Keep].Role == ai.RoleTool {
			t.Fatalf("budget %d kept from a tool result", budget)
		}
		if cut.Split && (cut.TurnStart < 0 || cut.TurnStart >= cut.Keep || messages[cut.TurnStart].Role != ai.RoleUser) {
			t.Fatalf("budget %d split without the turn's start before it: %+v", budget, cut)
		}
	}
}
