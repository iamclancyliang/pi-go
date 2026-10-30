package compaction

import (
	"context"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

func TestABranchSummaryComesWithThePreambleAndItsFiles(t *testing.T) {
	model := &ai.Scripted{Name: "m", Final: ai.AssistantText("## Goal\nfix it")}
	c := &Compactor{Model: model, ModelName: "m"}
	got, err := c.SummarizeBranch(context.Background(), []BranchEntry{
		{Message: ai.Message{Role: ai.RoleUser, Content: "fix the parser"}},
		{Message: ai.Message{Role: ai.RoleAssistant, Content: "on it", ToolCalls: []ai.ToolCall{
			{ID: "1", Name: "read", Args: `{"path":"a.go"}`},
			{ID: "2", Name: "read", Args: `{"path":"b.go"}`},
			{ID: "3", Name: "edit", Args: `{"path":"b.go"}`},
		}}},
		{Message: ai.Message{Role: ai.RoleTool, Content: "SECRET TOOL OUTPUT", ToolCallID: "1"}},
	}, "the parser", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := branchSummaryPreamble + "## Goal\nfix it\n\n<read-files>\na.go\n</read-files>\n\n<modified-files>\nb.go\n</modified-files>"
	if got.Summary != want {
		t.Fatalf("summary is\n%q\nwant\n%q", got.Summary, want)
	}
	if strings.Join(got.ReadFiles, ",") != "a.go" || strings.Join(got.ModifiedFiles, ",") != "b.go" {
		t.Fatalf("files %v %v", got.ReadFiles, got.ModifiedFiles)
	}
	request := model.Requests()[0]
	prompt := request.Messages[1].Content
	if !strings.Contains(prompt, "fix the parser") || strings.Contains(prompt, "SECRET TOOL OUTPUT") {
		t.Fatalf("the prompt should hold the conversation without tool results:\n%s", prompt)
	}
	if !strings.HasSuffix(prompt, "Additional focus: the parser") {
		t.Fatalf("the focus is missing from:\n%s", prompt)
	}
}

// TestNothingToSummariseCostsNothing: a branch of only tool results has no text to
// send, and a model call for nothing is a bill for nothing.
func TestNothingToSummariseCostsNothing(t *testing.T) {
	model := &ai.Scripted{Name: "m", Final: ai.AssistantText("x")}
	got, err := (&Compactor{Model: model}).SummarizeBranch(context.Background(), []BranchEntry{
		{Message: ai.Message{Role: ai.RoleTool, Content: "output", ToolCallID: "1"}},
	}, "", 0, 0)
	if err != nil || got.Summary != "No content to summarize" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if len(model.Requests()) != 0 {
		t.Fatal("a model was asked to summarise nothing")
	}
}

// TestALongBranchKeepsItsNewestPart and the files from everything, fitting in a
// summary that would otherwise have been cut.
func TestALongBranchKeepsItsNewestPart(t *testing.T) {
	big := strings.Repeat("x", 400) // 100 tokens
	entries := []BranchEntry{
		{Message: ai.Message{Role: ai.RoleUser, Content: "OLDEST " + big}, ReadFiles: []string{"from-earlier-branch.go"}},
		{Message: ai.Message{Role: ai.RoleUser, Content: "THE SUMMARY"}, IsSummary: true},
		{Message: ai.Message{Role: ai.RoleUser, Content: "NEWEST " + big}},
	}
	messages, read, _ := prepareBranch(entries, 105)
	var kept []string
	for _, m := range messages {
		kept = append(kept, m.Content[:6])
	}
	if strings.Join(kept, " ") != "THE SU NEWEST" {
		t.Fatalf("kept %v", kept)
	}
	if !read["from-earlier-branch.go"] {
		t.Fatal("a file recorded on an entry that did not fit was lost")
	}
}
