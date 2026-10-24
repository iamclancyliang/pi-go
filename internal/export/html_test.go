package export_test

import (
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/export"
)

func render(t *testing.T, messages ...ai.Message) string {
	t.Helper()
	out, err := export.HTML(export.Document{
		SessionID: "s-1", Name: "parser work", Provider: "deepseek", Model: "deepseek-chat",
		Exported:     time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
		SystemPrompt: "You are pi-go.",
		Tools:        []export.Tool{{Name: "read", Description: "Read a file"}},
		Messages:     messages,
	})
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	return out
}

// TestTheConversationReadsAsAPage: Markdown in the reply is rendered, as
// Pi's export renders it, and the session says what it was.
func TestTheConversationReadsAsAPage(t *testing.T) {
	out := render(t,
		ai.Message{Role: ai.RoleUser, Content: "fix the **parser**"},
		ai.Message{Role: ai.RoleAssistant, Content: "Done:\n\n```go\nfunc parse() {}\n```"},
	)
	for _, want := range []string{
		"<!doctype html>", "parser work", "deepseek/deepseek-chat",
		"<strong>parser</strong>", `<code class="language-go">func parse() {}`,
		"You are pi-go.", "Read a file",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
}

// TestNothingInAConversationRunsInTheReader. A conversation carries text from
// files, commands and models; opened in a browser, none of it may become
// markup that runs.
func TestNothingInAConversationRunsInTheReader(t *testing.T) {
	out := render(t,
		ai.Message{Role: ai.RoleUser, Content: "<script>alert(1)</script> [x](javascript:alert(2))"},
		ai.Message{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{{ID: "c1", Name: "read", Args: `{"path":"<img src=x onerror=alert(3)>"}`}}},
		ai.Message{Role: ai.RoleTool, ToolCallID: "c1", Content: "</pre><script>alert(4)</script>"},
	)
	for _, bad := range []string{"<script>alert", "javascript:alert", "<img src=x"} {
		if strings.Contains(out, bad) {
			t.Errorf("the page carries %q unescaped", bad)
		}
	}
}

// TestAToolResultSitsWithTheCallItAnswers, named by the tool, and reasoning
// is shown apart from the answer.
func TestAToolResultSitsWithTheCallItAnswers(t *testing.T) {
	out := render(t,
		ai.Message{Role: ai.RoleUser, Content: "read it"},
		ai.Message{Role: ai.RoleAssistant, Reasoning: "I should read the file first",
			ToolCalls: []ai.ToolCall{{ID: "c1", Name: "read", Args: `{"path":"a.go"}`}}},
		ai.Message{Role: ai.RoleTool, ToolCallID: "c1", Content: "package main"},
		ai.Message{Role: ai.RoleAssistant, Content: "It is a main package."},
	)
	call := strings.Index(out, "a.go")
	result := strings.Index(out, "package main")
	answer := strings.Index(out, "It is a main package.")
	if call < 0 || result < call || answer < result {
		t.Fatalf("call at %d, result at %d, answer at %d: not in conversation order", call, result, answer)
	}
	if !strings.Contains(out, "result of read") {
		t.Error("the result does not say which tool it came from")
	}
	if !strings.Contains(out, "Thinking") || !strings.Contains(out, "I should read the file first") {
		t.Error("the reasoning is missing or not marked as reasoning")
	}
}

// TestAShellCommandShowsWhatRanAndHowItEnded.
func TestAShellCommandShowsWhatRanAndHowItEnded(t *testing.T) {
	code := 2
	out := render(t, ai.Message{Role: ai.RoleUser, BashExecution: &ai.BashExecution{
		Command: "go test ./...", Output: "FAIL", ExitCode: &code}})
	for _, want := range []string{"go test ./...", "FAIL", "exit 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("the shell record lacks %q", want)
		}
	}
}
