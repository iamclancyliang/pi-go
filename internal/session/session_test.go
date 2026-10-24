package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// TestUnmatchedToolCalls covers the state a cancellation leaves behind: a tool
// call emitted with no result recorded.
//
// Getting this wrong is not cosmetic. If an unmatched call is reported as
// settled, recovery replays a tool that may have already run; if a settled call
// is reported as unmatched, recovery re-runs work that completed.
func TestUnmatchedToolCalls(t *testing.T) {
	for _, tc := range []struct {
		name string
		msgs []ai.Message
		want []string
	}{
		{
			name: "no tool calls at all",
			msgs: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
			want: nil,
		},
		{
			name: "call settled by its result",
			msgs: []ai.Message{
				{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{{ID: "a"}}},
				{Role: ai.RoleTool, ToolCallID: "a", Content: "done"},
			},
			want: nil,
		},
		{
			name: "call with no result",
			msgs: []ai.Message{
				{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{{ID: "a"}}},
			},
			want: []string{"a"},
		},
		{
			name: "one of two settled",
			msgs: []ai.Message{
				{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{{ID: "a"}, {ID: "b"}}},
				{Role: ai.RoleTool, ToolCallID: "b", Content: "done"},
			},
			want: []string{"a"},
		},
		{
			// A result whose ID matches nothing must not silently
			// settle an unrelated call.
			name: "orphan result does not settle another call",
			msgs: []ai.Message{
				{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{{ID: "a"}}},
				{Role: ai.RoleTool, ToolCallID: "zzz", Content: "who?"},
			},
			want: []string{"a"},
		},
		{
			// Order is emission order, not map order — assertions
			// depend on it being stable.
			name: "unmatched reported in emission order",
			msgs: []ai.Message{
				{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{{ID: "z"}, {ID: "y"}, {ID: "x"}}},
			},
			want: []string{"z", "y", "x"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New("sys")
			s.AppendAll(tc.msgs...)
			got := s.UnmatchedToolCalls()
			if len(got) != len(tc.want) {
				t.Fatalf("unmatched = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("unmatched = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestTruthIsNotAliased proves Truth returns a copy.
//
// Without this, a caller could edit history in place — the exact failure the
// truth/projection split exists to prevent, and one that would be invisible
// until something downstream disagreed about what happened.
func TestTruthIsNotAliased(t *testing.T) {
	s := New("sys")
	s.Append(ai.Message{Role: ai.RoleUser, Content: "original"})

	got := s.Truth()
	got[0].Content = "tampered"
	got[0].ToolCalls = append(got[0].ToolCalls, ai.ToolCall{ID: "injected"})

	after := s.Truth()
	if after[0].Content != "original" {
		t.Errorf("session content mutated through the returned slice: %q", after[0].Content)
	}
	if len(after[0].ToolCalls) != 0 {
		t.Errorf("tool calls injected through the returned slice: %v", after[0].ToolCalls)
	}
}

// TestProjectionCarriesSystemAndCompleteness pins the v0 projection contract:
// the system instruction survives, and a lossless projection says so.
func TestProjectionCarriesSystemAndCompleteness(t *testing.T) {
	s := New("you are pi-go")
	s.Append(ai.Message{Role: ai.RoleUser, Content: "hi"})

	p := s.Project()
	if !p.Complete {
		t.Error("v0 projection loses nothing, so Complete must be true")
	}
	if len(p.Messages) != 2 {
		t.Fatalf("projection has %d messages, want 2 (system + user)", len(p.Messages))
	}
	if p.Messages[0].Role != ai.RoleSystem || p.Messages[0].Content != "you are pi-go" {
		t.Errorf("projection[0] = %+v, want the system instruction first", p.Messages[0])
	}

	// The system instruction is not conversational history.
	if got := s.Len(); got != 1 {
		t.Errorf("Len() = %d, want 1 — the system message must not count as history", got)
	}
}

func TestBashExecutionIsDurableAndExcludedOnlyFromProjection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName("bash", time.Unix(0, 0)))
	store, err := OpenFileStore(path, dir, "bash")
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer store.Close()
	s := WithStore("You are pi-go.", store)
	code := 3
	if err := s.AppendBashExecution(ai.BashExecution{
		Command: "printf secret", Output: "secret\n", ExitCode: &code,
	}); err != nil {
		t.Fatalf("recording bash execution: %v", err)
	}
	if err := s.AppendBashExecution(ai.BashExecution{
		Command: "printf hidden", Output: "hidden\n", ExcludeFromContext: true,
	}); err != nil {
		t.Fatalf("recording excluded bash execution: %v", err)
	}
	truth := s.Truth()
	if len(truth) != 2 || truth[0].BashExecution == nil || truth[1].BashExecution == nil {
		t.Fatalf("bash records missing from truth: %+v", truth)
	}
	if !strings.Contains(truth[0].Content, "secret") || truth[1].Content == "" {
		t.Fatalf("bash records did not carry serializable content: %+v", truth)
	}
	projection := s.Project().Messages
	if len(projection) != 2 || !strings.Contains(projection[1].Content, "secret") || strings.Contains(projection[1].Content, "hidden") {
		t.Fatalf("projection did not preserve/exclude bash records: %+v", projection)
	}
	restored, err := Restore(context.Background(), "You are pi-go.", store)
	if err != nil {
		t.Fatalf("restoring bash records: %v", err)
	}
	if len(restored.Truth()) != 2 || restored.Truth()[0].BashExecution == nil || !restored.Truth()[1].BashExecution.ExcludeFromContext {
		t.Fatalf("restored bash records were %+v", restored.Truth())
	}
	raw, err := os.ReadFile(store.Path())
	if err != nil || !strings.Contains(string(raw), "bash_execution") {
		t.Fatalf("durable store does not contain bash records: %v\n%s", err, raw)
	}
}

func TestBashExecutionWaitsUntilAgentRunEndsToPreserveToolOrdering(t *testing.T) {
	s := New("You are pi-go.")
	s.BeginAgentRun()
	if err := s.AppendBashExecution(ai.BashExecution{Command: "echo shell", Output: "shell\n"}); err != nil {
		t.Fatalf("queueing bash execution: %v", err)
	}
	if err := s.Append(ai.Message{Role: ai.RoleAssistant, Content: "model"}); err != nil {
		t.Fatalf("appending model message: %v", err)
	}
	if got := len(s.Truth()); got != 1 {
		t.Fatalf("queued bash became visible during run: %d messages", got)
	}
	if err := s.EndAgentRun(); err != nil {
		t.Fatalf("flushing bash execution: %v", err)
	}
	truth := s.Truth()
	if len(truth) != 2 || truth[0].Content != "model" || truth[1].BashExecution == nil {
		t.Fatalf("bash execution was not appended after model messages: %+v", truth)
	}
}

// TestReadingHistoryCannotRewriteIt pins that a reader gets copies.
//
// Truth and Project are how anything outside this package reads the record. If
// what they hand back shares storage with the session, a reader can change what
// happened — and an append-only record whose contents can be edited in place is
// not one. The tool calls on a message are where this hides: copying the struct
// copies the slice header, so the copy looks defensive and the elements are still
// shared.
func TestReadingHistoryCannotRewriteIt(t *testing.T) {
	s := New("You are pi-go.")
	if err := s.Append(ai.Message{
		Role:      ai.RoleAssistant,
		Content:   "CALLING",
		ToolCalls: []ai.ToolCall{{ID: "call-1", Name: "read_files", Args: `{"path":"a"}`}},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	for _, read := range []struct {
		name string
		get  func() []ai.Message
	}{
		{"Truth", s.Truth},
		{"Project", func() []ai.Message { return s.Project().Messages }},
	} {
		t.Run(read.name, func(t *testing.T) {
			got := read.get()
			for i := range got {
				got[i].Content = "REWRITTEN"
				for j := range got[i].ToolCalls {
					got[i].ToolCalls[j].Args = "REWRITTEN"
				}
			}

			for _, m := range s.Truth() {
				if m.Content == "REWRITTEN" {
					t.Error("history was rewritten through what a reader was handed")
				}
				for _, call := range m.ToolCalls {
					if call.Args == "REWRITTEN" {
						t.Errorf("a recorded call's arguments were rewritten from "+
							"outside: %q", call.Args)
					}
				}
			}
		})
	}
}

// TestASummaryReachesTheModelAsAUserMessageThatSaysWhatItIs, worded as Pi
// words it. As a system message it would leave a kept part that begins with
// an assistant reply — a compaction that cut inside a turn — with no user
// message before it, which several providers refuse.
func TestASummaryReachesTheModelAsAUserMessageThatSaysWhatItIs(t *testing.T) {
	s := New("you are pi-go")
	if err := s.Append(ai.Message{Role: ai.RoleUser, Content: "old"}); err != nil {
		t.Fatal(err)
	}
	kept := []ai.Message{{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{{ID: "c1", Name: "read", Args: "{}"}}}}
	if err := s.Compact("the summary", kept); err != nil {
		t.Fatal(err)
	}

	got := s.Project().Messages
	if len(got) < 3 {
		t.Fatalf("projection %v", got)
	}
	summary := got[1]
	if summary.Role != ai.RoleUser {
		t.Fatalf("the summary is projected as %q", summary.Role)
	}
	if !strings.HasPrefix(summary.Content, "The conversation history before this point was compacted into the following summary:\n\n<summary>\nthe summary") ||
		!strings.HasSuffix(summary.Content, "\n</summary>") {
		t.Fatalf("the summary does not say what it is: %q", summary.Content)
	}
	if got[2].Role != ai.RoleAssistant {
		t.Fatalf("the kept reply did not follow the summary: %v", got[2])
	}
}
