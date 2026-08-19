// Package conformance holds the acceptance-scenario tests from
// docs/specs/traceability-matrix.md.
//
// It lives INSIDE the module deliberately. Implementation sits under
// `internal/`, which no code outside this repository can import — so a
// conformance suite in a separate module could not reach the runtime at all.
package conformance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// TestA1TracerBullet covers the headline scenario: a fake model requests two
// tools, then answers, and the run produces a complete event trace plus a
// final answer.
//
// It exercises the full path:
//
//	prompt -> model boundary -> agent loop -> read-only tools -> model answer
//	       -> event trace -> session snapshot
func TestA1TracerBullet(t *testing.T) {
	rec, sess, model, fileRead, listFiles := runA1(t)

	kinds := rec.Kinds()
	t.Logf("trace: %v", kinds)

	// --- the model was actually reached, twice ---
	//
	// Two calls, not one: the model must be asked again AFTER the tools
	// settle, or "then answers" is unproven.
	requests := model.Requests()
	if len(requests) != 2 {
		t.Fatalf("model calls = %d, want 2 (one requesting tools, one answering): %d requests", len(requests), len(requests))
	}

	// --- both tools ran, exactly once each ---
	if got := fileRead.Calls(); len(got) != 1 || got[0] != "README.md" {
		t.Errorf("file_read calls = %v, want exactly [README.md]", got)
	}
	if got := listFiles.Calls(); got != 1 {
		t.Errorf("list_files calls = %d, want 1", got)
	}

	// --- the trace is complete and correctly ordered ---
	//
	// This round runs SEQUENTIALLY: it contains a tool that declared it cannot
	// overlap, and one such tool makes the whole round sequential. The shape that
	// proves it is each call finishing -- start, end, result -- before the next
	// one starts. A round of parallel-safe tools emits a different shape, which
	// TestParallelRoundOrdering covers.
	assertOrder(t, kinds,
		events.KindAgentStart,
		events.KindTurnStart,
		events.KindModelRequest,
		events.KindModelResponse,
		events.KindToolStart,
		events.KindToolEnd,
		events.KindToolResult,
		events.KindToolStart,
		events.KindToolEnd,
		events.KindToolResult,
		events.KindModelRequest,
		events.KindModelResponse,
		events.KindTurnEnd,
		events.KindAgentEnd,
	)

	// --- tool events pair by ID, not by position ---
	//
	// Asserting "a tool_start is followed by a tool_end" would pass even if
	// the runtime paired the wrong call with the wrong result. The IDs are
	// what actually pair.
	starts := map[string]string{}
	ends := map[string]string{}
	for _, e := range rec.Events() {
		switch e.Kind {
		case events.KindToolStart:
			if e.ToolCallID == "" {
				t.Errorf("tool_start seq %d has no ToolCallID — pairing is unprovable", e.Seq)
			}
			starts[e.ToolCallID] = e.ToolName
		case events.KindToolEnd:
			if e.ToolCallID == "" {
				t.Errorf("tool_end seq %d has no ToolCallID", e.Seq)
			}
			ends[e.ToolCallID] = e.ToolName
		}
	}
	if len(starts) != 2 {
		t.Errorf("distinct tool_start IDs = %d, want 2: %v", len(starts), starts)
	}
	for id, name := range starts {
		endName, ok := ends[id]
		if !ok {
			t.Errorf("tool call %s (%s) started but never ended", id, name)
			continue
		}
		if endName != name {
			t.Errorf("tool call %s started as %q but ended as %q", id, name, endName)
		}
	}

	// --- the second model call actually saw the tool results ---
	//
	// This is the contract that matters and the one easiest to fake. Assert
	// on the tool-result CONTENT reaching the model, not on message roles:
	// a role-shape check passes even when the results are empty.
	second := requests[1]
	var toolContents []string
	for _, m := range second.Messages {
		if m.Role == ai.RoleTool {
			toolContents = append(toolContents, m.Content)
		}
	}
	if len(toolContents) != 2 {
		t.Fatalf("second model call saw %d tool results, want 2: %#v", len(toolContents), toolContents)
	}
	joined := strings.Join(toolContents, "\n")
	if !strings.Contains(joined, "pi-go tracer bullet fixture") {
		t.Errorf("file_read result never reached the model: %q", joined)
	}
	if !strings.Contains(joined, "config.yml") {
		t.Errorf("list_files result never reached the model: %q", joined)
	}

	// --- context grew rather than being rebuilt ---
	first := requests[0]
	if len(second.Messages) <= len(first.Messages) {
		t.Errorf("second call context (%d msgs) did not grow beyond the first (%d) — "+
			"history was rebuilt, not continued", len(second.Messages), len(first.Messages))
	}

	// --- the final answer is present ---
	last := rec.Events()[len(rec.Events())-1]
	if last.Kind != events.KindAgentEnd {
		t.Fatalf("last event = %s, want agent_end", last.Kind)
	}
	if last.Detail.Reason != "stop" {
		t.Errorf("agent_end reason = %q, want %q", last.Detail.Reason, "stop")
	}
	var answered bool
	for _, e := range rec.Events() {
		if e.Kind == events.KindModelResponse && strings.Contains(e.Detail.Text, "two files") {
			answered = true
		}
	}
	if !answered {
		t.Error("no model_response carried the final answer")
	}

	// --- session snapshot is complete truth ---
	snap := sess.Snapshot()
	// user + assistant(tool calls) + 2 tool results + assistant(answer)
	if len(snap.Messages) != 5 {
		t.Errorf("session recorded %d messages, want 5: %#v", len(snap.Messages), snap.Messages)
	}
	if len(snap.Unmatched) != 0 {
		t.Errorf("session has unmatched tool calls after a clean run: %v", snap.Unmatched)
	}
	if snap.System == "" {
		t.Error("session snapshot lost the system instruction")
	}
}

// TestA1NoToolsControl is the negative control for the tool half of that
// scenario.
//
// Without it, "the tools ran because the model asked for them" rests only on
// the positive case — and tools firing for some other reason would look
// identical. Here the model asks for nothing, so nothing may run.
func TestA1NoToolsControl(t *testing.T) {
	registry, fileRead, listFiles := tools.NewFixtureRegistry()
	sess := session.New("You are pi-go.")
	model := &ai.Scripted{
		Name:  "fake-1",
		Final: ai.AssistantText("No tools needed."),
	}
	rec := runtime.NewRecorder()

	agent, err := runtime.New(runtime.Config{
		Model:     model,
		ModelName: "fake-1",
		Tools:     registry,
		Session:   sess,
		Observers: []events.Observer{rec},
		Now:       fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := agent.Run(ctx, "Say hello."); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, k := range rec.Kinds() {
		if k == events.KindToolStart || k == events.KindToolEnd {
			t.Errorf("tool event %s emitted when the model requested no tools", k)
		}
	}
	if got := fileRead.Calls(); len(got) != 0 {
		t.Errorf("file_read ran %v times with no tool request", got)
	}
	if got := listFiles.Calls(); got != 0 {
		t.Errorf("list_files ran %d times with no tool request", got)
	}
}

// runA1 executes the scenario and returns everything needed to assert on it.
func runA1(t *testing.T) (*runtime.Recorder, *session.Session, *ai.Scripted, *tools.FileRead, *tools.ListFiles) {
	t.Helper()

	registry, fileRead, listFiles := tools.NewFixtureRegistry()
	sess := session.New("You are pi-go.")

	model := &ai.Scripted{
		Name: "fake-1",
		Replies: []ai.Response{
			ai.AssistantToolCalls(
				ai.ToolCall{ID: "call-1", Name: "file_read", Args: `{"path":"README.md"}`},
				ai.ToolCall{ID: "call-2", Name: "list_files", Args: `{}`},
			),
		},
		// Guards against the harness bug that once produced a false
		// safety failure: a script indexed only by call count re-issues
		// its canned tool call, which looks like the product replaying
		// a tool. Answering once the calls are settled removes that
		// failure mode entirely.
		StopWhenToolsSettled: true,
		Final:                ai.AssistantText("I read two files."),
	}

	rec := runtime.NewRecorder()
	agent, err := runtime.New(runtime.Config{
		Model:     model,
		ModelName: "fake-1",
		Tools:     registry,
		Session:   sess,
		Policy:    runtime.DenyWrites,
		Observers: []events.Observer{rec},
		Now:       fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := agent.Run(ctx, "Read README.md and list the files."); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rec, sess, model, fileRead, listFiles
}

// assertOrder checks that got matches want exactly.
//
// Exact rather than subsequence: A1's expected result is a COMPLETE event
// trace, so an extra or missing event is a failure. A subsequence check would
// silently tolerate both.
func assertOrder(t *testing.T, got []events.Kind, want ...events.Kind) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("trace length = %d, want %d\n got: %v\nwant: %v", len(got), len(want), got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("trace[%d] = %s, want %s\n got: %v\nwant: %v", i, got[i], want[i], got, want)
			return
		}
	}
}

// fixedClock returns a deterministic clock so the golden trace does not change
// between runs.
func fixedClock() func() time.Time {
	base := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	var n int
	return func() time.Time {
		n++
		return base.Add(time.Duration(n) * time.Millisecond)
	}
}

// TestSystemPromptEntersContextOnce guards the prompt the model actually sees.
//
// The session projection carries the system message, and the framework can be
// given its own instruction; supplying both puts it in twice. Nothing else in the
// suite notices, because every assertion about behaviour still holds when the
// prompt is duplicated — the model is simply asked something different from what
// the session says.
func TestSystemPromptEntersContextOnce(t *testing.T) {
	_, sess, model, _, _ := runA1(t)

	requests := model.Requests()
	if len(requests) == 0 {
		t.Fatal("the model received no requests")
	}
	for index, req := range requests {
		var system []string
		for _, m := range req.Messages {
			if m.Role == ai.RoleSystem {
				system = append(system, m.Content)
			}
		}
		if len(system) != 1 {
			t.Errorf("request %d carried %d system messages, want 1: %q",
				index, len(system), system)
			continue
		}
		if system[0] != sess.System() {
			t.Errorf("request %d system message = %q, want the session's %q",
				index, system[0], sess.System())
		}
	}
}
