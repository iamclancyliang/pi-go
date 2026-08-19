package conformance

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// TestA11UnsettledCallsAreNotAssumedNotToHaveHappened pins the conservative
// reading of a lost outcome.
//
// A process can die between a tool acting and the record of what it did. The
// transcript then cannot distinguish a call that never ran from one that ran and
// lost its answer — and the second is the dangerous reading. So an unsettled
// intent means unknown, never absent.
//
// Repeating is allowed only when the policy recorded before the crash and the one
// the tool declares now BOTH say it is safe. Either side disagreeing refuses:
// code that changed while the process was down has not agreed to be repeated.
func TestA11UnsettledCallsAreNotAssumedNotToHaveHappened(t *testing.T) {
	cases := []struct {
		name        string
		recorded    tools.ReplayPolicy
		declaredNow tools.ReplayPolicy
		version     string
		known       bool
		wantReplay  bool
	}{
		{"both say safe", tools.ReplaySafe, tools.ReplaySafe, "v1", true, true},
		{"recorded safe, now forbidden", tools.ReplaySafe, tools.ReplayNever, "v1", true, false},
		{"recorded forbidden, now safe", tools.ReplayNever, tools.ReplaySafe, "v1", true, false},
		{"neither says safe", tools.ReplayNever, tools.ReplayNever, "v1", true, false},
		{"tool no longer registered", tools.ReplaySafe, tools.ReplaySafe, "v1", false, false},
		{"tool changed while down", tools.ReplaySafe, tools.ReplaySafe, "v2", true, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &session.MemoryStore{}
			live := session.WithStore("You are pi-go.", store)
			if err := live.RecordIntent(session.ToolIntent{
				OperationID: "op-1", CallID: "call-1", ResultID: "result-1",
				Tool: "delete_files", ToolVersion: "v1", Args: `{"path":"/tmp/x"}`,
				Replay: c.recorded,
			}); err != nil {
				t.Fatalf("RecordIntent: %v", err)
			}
			// The process dies here: the tool may or may not have acted.

			reopened, err := session.Restore(context.Background(), "You are pi-go.", store)
			if err != nil {
				t.Fatalf("Restore: %v", err)
			}
			if got := len(reopened.UnsettledIntents()); got != 1 {
				t.Fatalf("unsettled intents after restart = %d, want 1", got)
			}

			replayable, err := session.RecoverUnsettled(context.Background(), reopened,
				func(string) (tools.ReplayPolicy, string, bool) {
					return c.declaredNow, c.version, c.known
				})
			if err != nil {
				t.Fatalf("RecoverUnsettled: %v", err)
			}

			if c.wantReplay {
				if len(replayable) != 1 {
					t.Fatalf("replayable = %v, want the call to be repeatable", replayable)
				}
				// The caller gets everything needed to repeat the exact work,
				// rather than an id it must look up again — the lookup is where
				// this decision can quietly be bypassed.
				if got := replayable[0]; got.Args == "" || got.ResultID == "" {
					t.Errorf("replayable intent = %+v, want the exact args and the "+
						"reserved result id", got)
				}
				return
			}
			if len(replayable) != 0 {
				t.Errorf("replayable = %v, want nothing repeated", replayable)
			}
			// Refusing to repeat is not the same as leaving it dangling: the
			// call is settled as interrupted so the model is told the outcome
			// is unknown rather than waiting forever.
			if got := len(reopened.UnsettledIntents()); got != 0 {
				t.Errorf("%d intents left unsettled after recovery, want 0", got)
			}
			// The outcome is recorded AND told to the model. A model that hears
			// nothing about the call waits for an answer that is not coming.
			settled, ok := reopened.Settlement("result-1")
			if !ok || !settled.Interrupted {
				t.Errorf("settlement = %+v (found=%v), want a synthetic interrupted "+
					"outcome", settled, ok)
			}
			if settled.ResultID != "result-1" {
				t.Errorf("settlement wrote to %q, want the id reserved before the "+
					"call", settled.ResultID)
			}
			var told bool
			for _, m := range reopened.Project().Messages {
				if m.ToolCallID == "call-1" {
					told = true
				}
			}
			if !told {
				t.Error("the model was never told the call's outcome is unknown")
			}
		})
	}
}

// TestA11AnAlreadySettledCallIsAuthoritative pins that recovery does not touch a
// call whose outcome is known.
//
// Re-executing or re-settling it would replace a real answer with a synthetic
// one, which is worse than the crash it is recovering from.
func TestA11AnAlreadySettledCallIsAuthoritative(t *testing.T) {
	store := &session.MemoryStore{}
	live := session.WithStore("You are pi-go.", store)

	if err := live.RecordIntent(session.ToolIntent{
		OperationID: "op-1", CallID: "call-1", ResultID: "result-1",
		Tool: "delete_files", ToolVersion: "v1", Replay: tools.ReplayNever,
	}); err != nil {
		t.Fatalf("RecordIntent: %v", err)
	}
	if err := live.Settle(session.ToolSettlement{
		CallID: "call-1", ResultID: "result-1", Result: "REAL-RESULT",
	}); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	reopened, err := session.Restore(context.Background(), "You are pi-go.", store)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := reopened.UnsettledIntents(); len(got) != 0 {
		t.Fatalf("a settled call was reported unsettled: %v", got)
	}
	replayable, err := session.RecoverUnsettled(context.Background(), reopened,
		func(string) (tools.ReplayPolicy, string, bool) {
			return tools.ReplaySafe, "v1", true
		})
	if err != nil {
		t.Fatalf("RecoverUnsettled: %v", err)
	}
	if len(replayable) != 0 {
		t.Errorf("recovery offered to repeat a call that already has an answer: %v", replayable)
	}
	// And the real answer is still the answer.
	if settled, ok := reopened.Settlement("result-1"); !ok || settled.Result != "REAL-RESULT" {
		t.Errorf("settlement = %+v, want the real result kept", settled)
	}
}

// TestA11DefaultPolicyForbidsRepeating pins the default a tool gets by saying
// nothing.
func TestA11DefaultPolicyForbidsRepeating(t *testing.T) {
	var declaredNothing tools.Execution
	if declaredNothing.Replay != tools.ReplayNever {
		t.Errorf("a tool that declares nothing got %q, want never: an unstated "+
			"policy must not permit repeating a destructive call",
			declaredNothing.Replay)
	}
}

// recordReadingTool reads the durable record from inside its own call.
//
// Reading it afterwards cannot tell the two orderings apart: the record exists
// either way by then. Only a tool that looks while it is running can say the
// attempt was durable BEFORE the effect, which is the whole ordering the contract
// is about.
type recordReadingTool struct {
	name      string
	version   string
	replay    tools.ReplayPolicy
	terminate bool
	store     session.Store

	seen []session.ToolIntent
}

func (t *recordReadingTool) Name() string        { return t.name }
func (t *recordReadingTool) Description() string { return "test tool" }
func (t *recordReadingTool) Version() string     { return t.version }
func (t *recordReadingTool) Execution() tools.Execution {
	return tools.Execution{ReadOnly: true, Sequential: true, Replay: t.replay}
}

func (t *recordReadingTool) Call(ctx context.Context, _ string) (tools.Result, error) {
	reopened, err := session.Restore(ctx, "You are pi-go.", t.store)
	if err != nil {
		return tools.Result{}, err
	}
	t.seen = reopened.UnsettledIntents()
	return tools.Result{Content: t.name + " finished", Terminate: t.terminate}, nil
}

// TestA11TheAttemptIsRecordedBeforeTheEffect pins the ordering a real call gets.
//
// A record written after the tool returns is missing in exactly the case it
// exists for. If the process dies during the call, a restart reads a conversation
// where nothing was attempted, so it is free to run the call again — against a
// world the first attempt may already have changed.
//
// The settlement is written WITH the result, because an outcome and the record
// that the outcome is known are the same fact. Splitting them leaves a call whose
// result is in history but which recovery would still offer to repeat.
func TestA11TheAttemptIsRecordedBeforeTheEffect(t *testing.T) {
	store := &session.MemoryStore{}
	tool := &recordReadingTool{
		name: "read_files", version: "v7",
		replay: tools.ReplaySafe, terminate: true, store: store,
	}
	registry := tools.NewRegistry()
	registry.MustRegister(tool)

	sess := session.WithStore("You are pi-go.", store)
	model := &ai.Scripted{
		Name: "fake-1",
		Replies: []ai.Response{ai.AssistantToolCalls(
			ai.ToolCall{ID: "call-1", Name: "read_files", Args: `{"path":"/tmp/x"}`},
		)},
		StopWhenToolsSettled: true,
		Final:                ai.AssistantText("done"),
	}
	agent, err := runtime.New(runtime.Config{
		Model:     model,
		ModelName: "fake-1",
		Tools:     registry,
		Session:   sess,
		Policy:    runtime.DenyWrites,
		Observers: []events.Observer{runtime.NewRecorder()},
		Now:       fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := agent.Run(ctx, "read the file"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(tool.seen) != 1 {
		t.Fatalf("the running call saw %d recorded attempts, want its own: a record "+
			"written after the effect cannot survive a crash during it", len(tool.seen))
	}
	intent := tool.seen[0]
	want := session.ToolIntent{
		OperationID: "op-1",
		CallID:      "call-1",
		ResultID:    "op-1.call-1",
		Tool:        "read_files",
		ToolVersion: "v7",
		Args:        `{"path":"/tmp/x"}`,
		Replay:      tools.ReplaySafe,
	}
	if intent != want {
		t.Errorf("recorded attempt = %+v, want %+v", intent, want)
	}

	// The terms are the ones the tool offered when it ran. Reading them back at
	// recovery time instead would judge the decision against whatever the tool
	// says later, which is the code that has not agreed to be repeated.
	settlement, settled := sess.Settlement(want.ResultID)
	if !settled {
		t.Fatal("the call ran and reported, and is still unsettled: recovery would " +
			"offer to repeat work already in history")
	}
	if settlement.Result != "read_files finished" {
		t.Errorf("settled result = %q, want what the call produced", settlement.Result)
	}
	if settlement.ResultID != want.ResultID {
		t.Errorf("settled slot = %q, want the %q the attempt reserved",
			settlement.ResultID, want.ResultID)
	}
	if !settlement.Terminate {
		t.Error("the settlement forgot that this call asked the conversation to " +
			"stop, so nothing on record says why the run ended")
	}
	if settlement.Interrupted {
		t.Error("a call that reported its own outcome was settled as interrupted")
	}
	if got := sess.UnsettledIntents(); len(got) != 0 {
		t.Errorf("unsettled after the round = %+v, want none", got)
	}
}

// writingTool declares a mutation, so the policy refuses it before it runs.
type writingTool struct{ ran bool }

func (w *writingTool) Name() string               { return "delete_files" }
func (w *writingTool) Description() string        { return "test tool" }
func (w *writingTool) Execution() tools.Execution { return tools.Execution{} }
func (w *writingTool) Call(context.Context, string) (tools.Result, error) {
	w.ran = true
	return tools.Result{Content: "deleted"}, nil
}

// TestA11ARefusalIsNotAnAttempt pins that a call which never ran leaves no
// attempt on record.
//
// Recording one anyway would make recovery treat a refusal as an outcome that
// might have happened, and for a tool declaring itself unsafe to repeat that is
// the reading that leaves a user unable to be told anything definite about their
// files.
func TestA11ARefusalIsNotAnAttempt(t *testing.T) {
	store := &session.MemoryStore{}
	tool := &writingTool{}
	registry := tools.NewRegistry()
	registry.MustRegister(tool)

	sess := session.WithStore("You are pi-go.", store)
	model := &ai.Scripted{
		Name: "fake-1",
		Replies: []ai.Response{ai.AssistantToolCalls(
			ai.ToolCall{ID: "call-1", Name: "delete_files", Args: `{}`},
		)},
		StopWhenToolsSettled: true,
		Final:                ai.AssistantText("done"),
	}
	agent, err := runtime.New(runtime.Config{
		Model:     model,
		ModelName: "fake-1",
		Tools:     registry,
		Session:   sess,
		Policy:    runtime.DenyWrites,
		Observers: []events.Observer{runtime.NewRecorder()},
		Now:       fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := agent.Run(ctx, "delete the file"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if tool.ran {
		t.Fatal("a refused call ran")
	}
	reopened, err := session.Restore(context.Background(), "You are pi-go.", store)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := reopened.UnsettledIntents(); len(got) != 0 {
		t.Errorf("recorded attempts = %+v, want none: nothing was attempted", got)
	}
	if _, settled := reopened.Settlement("op-1.call-1"); settled {
		t.Error("a call that never ran was settled, which reads as an outcome")
	}
}

// silentTool declares no version, so one has to be derived from what it does
// declare.
type silentTool struct {
	name        string
	description string
	replay      tools.ReplayPolicy
}

func (s *silentTool) Name() string               { return s.name }
func (s *silentTool) Description() string        { return s.description }
func (s *silentTool) Execution() tools.Execution { return tools.Execution{Replay: s.replay} }
func (s *silentTool) Call(context.Context, string) (tools.Result, error) {
	return tools.Result{}, nil
}

// TestA11ReplayTermsComeFromTheToolItself pins where the replay decision reads
// its inputs.
//
// The version is what makes "the same tool" answerable at all. Without one, a
// binary rebuilt with different behaviour behind the same tool name inherits an
// earlier agreement to be repeated — which is the case a repeat must not be
// allowed to slip through.
func TestA11ReplayTermsComeFromTheToolItself(t *testing.T) {
	t.Run("a tool that declares a version reports it", func(t *testing.T) {
		registry := tools.NewRegistry()
		registry.MustRegister(&recordReadingTool{
			name: "read_files", version: "v7", replay: tools.ReplaySafe,
		})
		policy, version, known := registry.Declaration("read_files")
		if !known || policy != tools.ReplaySafe || version != "v7" {
			t.Errorf("declaration = (%v, %q, %v), want (safe, \"v7\", true)",
				policy, version, known)
		}
	})

	t.Run("changed declarations derive a different version", func(t *testing.T) {
		before := tools.NewRegistry()
		before.MustRegister(&silentTool{name: "grep", description: "search files"})
		after := tools.NewRegistry()
		after.MustRegister(&silentTool{name: "grep", description: "search files and directories"})

		_, first, _ := before.Declaration("grep")
		_, second, _ := after.Declaration("grep")
		if first == "" {
			t.Fatal("a tool with no declared version got no version at all, so a " +
				"record written before a change still matches the changed tool")
		}
		if first == second {
			t.Error("a tool whose declarations changed kept the same version, so an " +
				"earlier agreement to repeat carries over to code that never made it")
		}
	})

	t.Run("an unregistered tool has declared nothing", func(t *testing.T) {
		policy, version, known := tools.NewRegistry().Declaration("gone")
		if known {
			t.Error("a tool that is not registered was reported as known")
		}
		if policy != tools.ReplayNever || version != "" {
			t.Errorf("declaration = (%v, %q), want (never, \"\"): nothing about a "+
				"tool that is not there may be assumed", policy, version)
		}
	})
}

// refusesToolResults fails any write carrying a tool result, and accepts the
// rest.
//
// Selective, because a store that fails everything cannot tell the two shapes
// apart: the conversation never gets far enough to settle anything. Failing only
// the write that carries the result is what distinguishes one all-or-none write
// from a settlement and a message recorded separately.
type refusesToolResults struct {
	inner session.MemoryStore
}

func (r *refusesToolResults) Append(ctx context.Context, entries ...session.Entry) error {
	for _, e := range entries {
		if e.Message != nil && e.Message.Role == ai.RoleTool {
			return errors.New("store unavailable")
		}
	}
	return r.inner.Append(ctx, entries...)
}

func (r *refusesToolResults) Load(ctx context.Context) ([]session.Entry, error) {
	return r.inner.Load(ctx)
}

// TestA11AnOutcomeAndItsResultAreOneWrite pins that settling a call and telling
// the model about it cannot come apart.
//
// Settling first and recording the result second leaves the worst reachable
// state when the second write fails: the call is settled, so recovery passes over
// it, while the conversation the model reads has no result for it. Nothing
// afterwards can tell that anything is missing — which is worse than either
// write failing outright.
func TestA11AnOutcomeAndItsResultAreOneWrite(t *testing.T) {
	t.Run("recovery", func(t *testing.T) {
		store := &refusesToolResults{}
		sess := session.WithStore("You are pi-go.", store)
		if err := sess.RecordIntent(session.ToolIntent{
			OperationID: "op-1", CallID: "call-1", ResultID: "op-1.call-1",
			Tool: "delete_files", ToolVersion: "v1", Replay: tools.ReplayNever,
		}); err != nil {
			t.Fatalf("RecordIntent: %v", err)
		}

		declared := func(string) (tools.ReplayPolicy, string, bool) {
			return tools.ReplayNever, "v1", true
		}
		if _, err := session.RecoverUnsettled(context.Background(), sess, declared); err == nil {
			t.Fatal("a settlement that could not be recorded was reported as done")
		}
		if _, settled := sess.Settlement("op-1.call-1"); settled {
			t.Error("the call was settled while the result the model reads was not " +
				"recorded, so nothing will revisit it and nothing says it is missing")
		}
		if got := sess.UnsettledIntents(); len(got) != 1 {
			t.Errorf("unsettled = %d, want 1: the outcome is still unknown", len(got))
		}
	})

	t.Run("a call that ran", func(t *testing.T) {
		store := &refusesToolResults{}
		tool := &recordReadingTool{name: "read_files", version: "v7",
			replay: tools.ReplaySafe, store: store}
		registry := tools.NewRegistry()
		registry.MustRegister(tool)

		sess := session.WithStore("You are pi-go.", store)
		agent, err := runtime.New(runtime.Config{
			Model: &ai.Scripted{
				Name: "fake-1",
				Replies: []ai.Response{ai.AssistantToolCalls(
					ai.ToolCall{ID: "call-1", Name: "read_files", Args: `{}`},
				)},
				StopWhenToolsSettled: true,
				Final:                ai.AssistantText("done"),
			},
			ModelName: "fake-1",
			Tools:     registry,
			Session:   sess,
			Policy:    runtime.DenyWrites,
			Observers: []events.Observer{runtime.NewRecorder()},
			Now:       fixedClock(),
		})
		if err != nil {
			t.Fatalf("runtime.New: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := agent.Run(ctx, "read the file"); err == nil {
			t.Fatal("a turn whose result could not be recorded reported success")
		}
		if _, settled := sess.Settlement("op-1.call-1"); settled {
			t.Error("the call was settled while its result was not recorded")
		}
	})
}

// TestA11AttemptsAreDistinguishedByTheirReservedSlot pins that a reused call id
// cannot close the wrong attempt.
//
// The model chooses call ids and nothing stops a later operation reusing one. If
// attempts were paired on the call id, the earlier operation's outcome would
// answer for the later one, and recovery would pass over an effect that is
// genuinely unknown — silently, because everything would look settled.
func TestA11AttemptsAreDistinguishedByTheirReservedSlot(t *testing.T) {
	store := &session.MemoryStore{}
	sess := session.WithStore("You are pi-go.", store)

	first := session.ToolIntent{
		OperationID: "op-1", CallID: "call-1", ResultID: "op-1.call-1",
		Tool: "delete_files", ToolVersion: "v1", Replay: tools.ReplayNever,
	}
	if err := sess.RecordIntent(first); err != nil {
		t.Fatalf("RecordIntent: %v", err)
	}
	if err := sess.Settle(session.ToolSettlement{
		CallID: first.CallID, ResultID: first.ResultID, Result: "deleted",
	}); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	// A later operation, and the model reuses the same call id.
	second := first
	second.OperationID, second.ResultID = "op-2", "op-2.call-1"
	if err := sess.RecordIntent(second); err != nil {
		t.Fatalf("RecordIntent: %v", err)
	}

	unsettled := sess.UnsettledIntents()
	if len(unsettled) != 1 || unsettled[0].ResultID != second.ResultID {
		t.Fatalf("unsettled = %+v, want only the second attempt: the first "+
			"operation's outcome does not answer for it", unsettled)
	}
	if _, settled := sess.Settlement(second.ResultID); settled {
		t.Error("the second attempt was reported as settled by the first one's outcome")
	}
	// An attempt that HAS an outcome is not waiting for one. Asked the other way
	// round, a recovery would offer a decision about work already reported.
	if got, waiting := sess.UnsettledIntent(first.ResultID); waiting {
		t.Errorf("attempt with an outcome reported as waiting: %+v", got)
	}
	if got, waiting := sess.UnsettledIntent(second.ResultID); !waiting ||
		got.OperationID != second.OperationID {
		t.Errorf("waiting attempt = %+v (found=%v), want the second one", got, waiting)
	}
	if _, waiting := sess.UnsettledIntent("op-3.call-1"); waiting {
		t.Error("an attempt nobody recorded was reported as waiting")
	}

	reopened, err := session.Restore(context.Background(), "You are pi-go.", store)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := reopened.UnsettledIntents(); len(got) != 1 ||
		got[0].ResultID != second.ResultID {
		t.Errorf("unsettled after a restart = %+v, want only the second attempt", got)
	}

	// An attempt with no reserved slot is refused, because everything filed
	// under the empty name would be the same attempt.
	if err := sess.RecordIntent(session.ToolIntent{CallID: "call-2"}); err == nil {
		t.Error("an attempt with no reserved slot was accepted")
	}
	if err := sess.Settle(session.ToolSettlement{CallID: "call-2"}); err == nil {
		t.Error("a settlement naming no attempt was accepted")
	}
}

// repeatableTool records how many times it was actually run.
type repeatableTool struct {
	name    string
	version string
	replay  tools.ReplayPolicy
	runs    int
}

func (c *repeatableTool) Name() string        { return c.name }
func (c *repeatableTool) Description() string { return "test tool" }
func (c *repeatableTool) Version() string     { return c.version }
func (c *repeatableTool) Execution() tools.Execution {
	return tools.Execution{ReadOnly: true, Replay: c.replay}
}

func (c *repeatableTool) Call(context.Context, string) (tools.Result, error) {
	c.runs++
	return tools.Result{Content: c.name + " ran"}, nil
}

// recoveringAgent builds an agent over a session restored from store.
func recoveringAgent(t *testing.T, store session.Store, registry *tools.Registry) (*runtime.Agent, *session.Session) {
	t.Helper()

	sess, err := session.Restore(context.Background(), "You are pi-go.", store)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	agent, err := runtime.New(runtime.Config{
		Model:     &ai.Scripted{Name: "fake-1", Final: ai.AssistantText("done")},
		ModelName: "fake-1",
		Tools:     registry,
		Session:   sess,
		Policy:    runtime.DenyWrites,
		Observers: []events.Observer{runtime.NewRecorder()},
		Now:       fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	return agent, sess
}

// seedUnfinished records an attempt and nothing else: the process died during the
// call.
func seedUnfinished(t *testing.T, store session.Store, tool string,
	version string, replay tools.ReplayPolicy) session.ToolIntent {
	t.Helper()

	sess := session.WithStore("You are pi-go.", store)
	if err := sess.AppendAll(
		ai.Message{Role: ai.RoleUser, Content: "do the thing"},
		ai.Message{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{
			{ID: "call-1", Name: tool, Args: `{}`},
		}},
	); err != nil {
		t.Fatalf("AppendAll: %v", err)
	}
	intent := session.ToolIntent{
		OperationID: "op-1", CallID: "call-1", ResultID: "op-1.call-1",
		Tool: tool, ToolVersion: version, Args: `{}`, Replay: replay,
	}
	if err := sess.RecordIntent(intent); err != nil {
		t.Fatalf("RecordIntent: %v", err)
	}
	return intent
}

// TestA11RecoveryAsksBeforeRepeating pins the recovery policy: a call that may be
// repeated is presented, not repeated.
//
// Repeating automatically would bet safety on every tool author having marked the
// declaration correctly, and the two mistakes are not symmetric — "did not do it"
// is visible and retryable, while "did it twice" may already have changed the
// user's files and cannot be seen or undone.
func TestA11RecoveryAsksBeforeRepeating(t *testing.T) {
	t.Run("a repeatable call waits for an answer", func(t *testing.T) {
		store := &session.MemoryStore{}
		intent := seedUnfinished(t, store, "read_files", "v7", tools.ReplaySafe)

		tool := &repeatableTool{name: "read_files", version: "v7", replay: tools.ReplaySafe}
		registry := tools.NewRegistry()
		registry.MustRegister(tool)
		agent, sess := recoveringAgent(t, store, registry)

		found, err := agent.Recover(context.Background())
		if err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if len(found.Awaiting) != 1 || found.Awaiting[0].ResultID != intent.ResultID {
			t.Fatalf("awaiting = %+v, want the unfinished call", found.Awaiting)
		}
		if tool.runs != 0 {
			t.Errorf("the tool ran %d times during recovery, want 0: whoever owns "+
				"the effects owns the decision to repeat", tool.runs)
		}
		if _, settled := sess.Settlement(intent.ResultID); settled {
			t.Error("a call that is waiting for an answer was given one")
		}

		// Nothing can be asked while it waits: the conversation holds a tool call
		// with no result, and a model shown that either waits forever or is
		// invited to act as though the call never happened.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := agent.Run(ctx, "and now this"); !errors.Is(err, runtime.ErrAwaitingRecovery) {
			t.Errorf("Run while an answer is owed = %v, want it refused", err)
		}
	})

	t.Run("repeating runs it again and records what it produced", func(t *testing.T) {
		store := &session.MemoryStore{}
		intent := seedUnfinished(t, store, "read_files", "v7", tools.ReplaySafe)

		tool := &repeatableTool{name: "read_files", version: "v7", replay: tools.ReplaySafe}
		registry := tools.NewRegistry()
		registry.MustRegister(tool)
		agent, sess := recoveringAgent(t, store, registry)

		if _, err := agent.Recover(context.Background()); err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if err := agent.Repeat(context.Background(), intent.ResultID); err != nil {
			t.Fatalf("Repeat: %v", err)
		}
		if tool.runs != 1 {
			t.Errorf("the tool ran %d times, want 1", tool.runs)
		}
		settled, ok := sess.Settlement(intent.ResultID)
		if !ok || settled.Result != "read_files ran" {
			t.Errorf("settlement = %+v, want what the repeat produced", settled)
		}
		if settled.Interrupted {
			t.Error("a call that was repeated and reported was settled as interrupted")
		}
		// Answered once. A second answer would run a tool the caller already
		// decided about.
		if err := agent.Repeat(context.Background(), intent.ResultID); !errors.Is(err, runtime.ErrAlreadySettled) {
			t.Errorf("repeating an answered call = %v, want it refused", err)
		}
		if tool.runs != 1 {
			t.Errorf("the tool ran %d times after a refused repeat, want 1", tool.runs)
		}
	})

	t.Run("abandoning tells the model the outcome is unknown", func(t *testing.T) {
		store := &session.MemoryStore{}
		intent := seedUnfinished(t, store, "read_files", "v7", tools.ReplaySafe)

		tool := &repeatableTool{name: "read_files", version: "v7", replay: tools.ReplaySafe}
		registry := tools.NewRegistry()
		registry.MustRegister(tool)
		agent, sess := recoveringAgent(t, store, registry)

		if _, err := agent.Recover(context.Background()); err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if err := agent.Abandon(intent.ResultID); err != nil {
			t.Fatalf("Abandon: %v", err)
		}
		if tool.runs != 0 {
			t.Errorf("the tool ran %d times, want 0", tool.runs)
		}
		settled, ok := sess.Settlement(intent.ResultID)
		if !ok || !settled.Interrupted {
			t.Fatalf("settlement = %+v, want an unknown outcome: declining to "+
				"repeat says nothing about whether the first attempt took effect",
				settled)
		}
		if !carries(sess.Project().Messages, settled.Result) {
			t.Error("the model was not told, so it is still waiting for this call")
		}
		// And now the conversation can go on.
		if got := sess.UnsettledIntents(); len(got) != 0 {
			t.Errorf("unsettled after an answer = %+v, want none", got)
		}
	})

	t.Run("a call that may not be repeated is not a question", func(t *testing.T) {
		store := &session.MemoryStore{}
		intent := seedUnfinished(t, store, "delete_files", "v1", tools.ReplayNever)

		registry := tools.NewRegistry()
		registry.MustRegister(&repeatableTool{name: "delete_files", version: "v1"})
		agent, sess := recoveringAgent(t, store, registry)

		found, err := agent.Recover(context.Background())
		if err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if len(found.Awaiting) != 0 {
			t.Errorf("awaiting = %+v, want nothing: no answer would let this run "+
				"again, so there is nothing to ask", found.Awaiting)
		}
		settled, ok := sess.Settlement(intent.ResultID)
		if !ok || !settled.Interrupted {
			t.Errorf("settlement = %+v, want it settled as unknown without asking",
				settled)
		}
	})

	t.Run("a tool changed since the question cannot answer it", func(t *testing.T) {
		store := &session.MemoryStore{}
		intent := seedUnfinished(t, store, "read_files", "v7", tools.ReplaySafe)

		// Recovery asked while v7 was registered; the answer arrives after a
		// swap. A repeat now is not the act that was agreed to.
		tool := &repeatableTool{name: "read_files", version: "v8", replay: tools.ReplaySafe}
		registry := tools.NewRegistry()
		registry.MustRegister(tool)
		agent, _ := recoveringAgent(t, store, registry)

		err := agent.Repeat(context.Background(), intent.ResultID)
		if !errors.Is(err, runtime.ErrTermsChanged) {
			t.Errorf("repeat after the tool changed = %v, want it to say the terms "+
				"no longer match", err)
		}
		if errors.Is(err, runtime.ErrToolGone) {
			t.Error("a registered tool that changed was reported as missing")
		}
		if tool.runs != 0 {
			t.Errorf("the changed tool ran %d times, want 0", tool.runs)
		}
	})
}

// TestA11OnlyRecordedAttemptsCanBeDecidedAbout pins that a decision names an
// attempt this conversation actually made.
//
// Taking a description of the work from the caller would let a repeat be asked for
// something never attempted here: the tool would run and its result be appended for
// a call the transcript does not contain, with no durable attempt behind either.
func TestA11OnlyRecordedAttemptsCanBeDecidedAbout(t *testing.T) {
	store := &session.MemoryStore{}
	seedUnfinished(t, store, "read_files", "v7", tools.ReplaySafe)

	tool := &repeatableTool{name: "read_files", version: "v7", replay: tools.ReplaySafe}
	registry := tools.NewRegistry()
	registry.MustRegister(tool)
	agent, sess := recoveringAgent(t, store, registry)

	invented := "op-9.call-9"
	if err := agent.Repeat(context.Background(), invented); !errors.Is(err, runtime.ErrNoAttemptWaiting) {
		t.Errorf("repeating an attempt that was never recorded = %v, want it refused", err)
	}
	if tool.runs != 0 {
		t.Errorf("the tool ran %d times for an attempt nobody made, want 0", tool.runs)
	}
	if err := agent.Abandon(invented); !errors.Is(err, runtime.ErrNoAttemptWaiting) {
		t.Errorf("abandoning an attempt that was never recorded = %v, want it refused", err)
	}
	if _, settled := sess.Settlement(invented); settled {
		t.Error("an outcome was recorded for an attempt the conversation never made")
	}
	// And the real one is untouched by either refusal.
	if got := sess.UnsettledIntents(); len(got) != 1 {
		t.Errorf("unsettled = %+v, want the one real attempt still waiting", got)
	}
}

// failingTool reports an error, which is an outcome rather than a crash.
type failingTool struct {
	version string
	runs    int
}

func (f *failingTool) Name() string        { return "read_files" }
func (f *failingTool) Description() string { return "test tool" }
func (f *failingTool) Version() string     { return f.version }
func (f *failingTool) Execution() tools.Execution {
	return tools.Execution{ReadOnly: true, Replay: tools.ReplaySafe}
}

func (f *failingTool) Call(context.Context, string) (tools.Result, error) {
	f.runs++
	return tools.Result{}, errors.New("the file moved")
}

// TestA11ARepeatThatFailsIsStillAnAnswer pins what happens when the repeated call
// reports an error.
//
// A failing tool is an outcome the model can react to, so the failure becomes the
// recorded result: the attempt is answered and the conversation continues. Leaving
// it unsettled instead would keep offering a decision that has already been made,
// and the model would still be waiting for a call it will never hear about.
func TestA11ARepeatThatFailsIsStillAnAnswer(t *testing.T) {
	store := &session.MemoryStore{}
	intent := seedUnfinished(t, store, "read_files", "v7", tools.ReplaySafe)

	tool := &failingTool{version: "v7"}
	registry := tools.NewRegistry()
	registry.MustRegister(tool)

	sess, err := session.Restore(context.Background(), "You are pi-go.", store)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	rec := runtime.NewRecorder()
	agent, err := runtime.New(runtime.Config{
		Model:     &ai.Scripted{Name: "fake-1", Final: ai.AssistantText("done")},
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

	if err := agent.Repeat(context.Background(), intent.ResultID); err != nil {
		t.Fatalf("Repeat: %v", err)
	}
	if tool.runs != 1 {
		t.Fatalf("the tool ran %d times, want 1", tool.runs)
	}
	settled, ok := sess.Settlement(intent.ResultID)
	if !ok {
		t.Fatal("a repeat that failed left the attempt unanswered, so the decision " +
			"would be offered again and the model would still be waiting")
	}
	if !strings.Contains(settled.Result, "the file moved") {
		t.Errorf("settled result = %q, want the failure the tool reported: a model "+
			"told nothing cannot react to it", settled.Result)
	}
	if settled.Interrupted {
		t.Error("a call that reported its own failure was settled as interrupted, " +
			"which says the outcome is unknown when it is known")
	}
	// The failure is on the end event, where it says HOW the call finished.
	var reported string
	for _, e := range rec.Events() {
		if e.Kind == events.KindToolEnd && e.ToolCallID == intent.CallID {
			reported = e.Detail.Err
		}
	}
	if !strings.Contains(reported, "the file moved") {
		t.Errorf("tool_end error = %q, want the failure: the event stream is what a "+
			"client renders, so a failure missing from it is invisible", reported)
	}
}

// TestA11ARepeatCanBeRefusedWithoutAnsweringTheAttempt pins the two ways a repeat
// cannot proceed.
//
// Neither settles the attempt. A refusal says the repeat did not happen, and that
// is not an answer to whether the FIRST attempt took effect — so the decision is
// still owed, and abandoning it stays available.
func TestA11ARepeatCanBeRefusedWithoutAnsweringTheAttempt(t *testing.T) {
	t.Run("the policy denies it", func(t *testing.T) {
		store := &session.MemoryStore{}
		intent := seedUnfinished(t, store, "sync_files", "v1", tools.ReplaySafe)

		// A mutation whose repeat is harmless — an idempotent write is exactly
		// that — so the replay terms permit a repeat and the policy still does
		// not. The two decisions are separate, and this is where they disagree.
		tool := &idempotentWriteTool{version: "v1"}
		registry := tools.NewRegistry()
		registry.MustRegister(tool)
		agent, sess := recoveringAgent(t, store, registry)

		if err := agent.Repeat(context.Background(), intent.ResultID); err == nil {
			t.Error("a repeat the policy denies was allowed")
		}
		if tool.runs != 0 {
			t.Errorf("a denied repeat ran the tool %d times, want 0", tool.runs)
		}
		if _, settled := sess.Settlement(intent.ResultID); settled {
			t.Error("a denied repeat answered the attempt, reporting the refusal as " +
				"the outcome of a call that ran before the refusal existed")
		}
		// Still answerable the other way.
		if err := agent.Abandon(intent.ResultID); err != nil {
			t.Errorf("Abandon after a denied repeat: %v", err)
		}
	})

	t.Run("the tool is gone", func(t *testing.T) {
		store := &session.MemoryStore{}
		intent := seedUnfinished(t, store, "read_files", "v7", tools.ReplaySafe)

		// Registered under a different name: nothing can run this call now.
		registry := tools.NewRegistry()
		registry.MustRegister(&repeatableTool{name: "list_files", version: "v7",
			replay: tools.ReplaySafe})
		agent, sess := recoveringAgent(t, store, registry)

		// The reason has to be distinguishable. An absent tool has declared
		// nothing, so it also fails the terms comparison — told only that the
		// terms changed, a caller would hunt for a declaration that moved when
		// the tool is simply not there.
		err := agent.Repeat(context.Background(), intent.ResultID)
		if !errors.Is(err, runtime.ErrToolGone) {
			t.Errorf("repeat with the tool unregistered = %v, want it to say the "+
				"tool is gone", err)
		}
		if errors.Is(err, runtime.ErrTermsChanged) {
			t.Error("an absent tool was reported as one whose terms changed")
		}
		if _, settled := sess.Settlement(intent.ResultID); settled {
			t.Error("the attempt was answered by the tool's absence")
		}
		if got := sess.UnsettledIntents(); len(got) != 1 {
			t.Errorf("unsettled = %+v, want the attempt still waiting", got)
		}
	})
}

// idempotentWriteTool mutates and declares a repeat harmless.
//
// Not a contradiction: "ensure this file says X" changes the world and can be run
// twice with the same result. It is the case where the replay terms and the policy
// give different answers, which is why they are asked separately.
type idempotentWriteTool struct {
	version string
	runs    int
}

func (i *idempotentWriteTool) Name() string        { return "sync_files" }
func (i *idempotentWriteTool) Description() string { return "test tool" }
func (i *idempotentWriteTool) Version() string     { return i.version }
func (i *idempotentWriteTool) Execution() tools.Execution {
	return tools.Execution{ReadOnly: false, Replay: tools.ReplaySafe}
}

func (i *idempotentWriteTool) Call(context.Context, string) (tools.Result, error) {
	i.runs++
	return tools.Result{Content: "synced"}, nil
}

// refusesIntents fails the write that records an attempt, and accepts the rest.
type refusesIntents struct {
	inner session.MemoryStore
}

func (r *refusesIntents) Append(ctx context.Context, entries ...session.Entry) error {
	for _, e := range entries {
		if e.Intent != nil {
			return errors.New("store unavailable")
		}
	}
	return r.inner.Append(ctx, entries...)
}

func (r *refusesIntents) Load(ctx context.Context) ([]session.Entry, error) {
	return r.inner.Load(ctx)
}

// TestA11ACallWhoseAttemptCannotBeRecordedDoesNotRun pins the rule that makes the
// record worth having.
//
// Running anyway would produce exactly what the record exists to prevent: an
// effect with nothing saying it was attempted. A restart would then read an
// untouched conversation and be free to do it again. The model is told the call
// failed, which is true — it never ran — rather than being left with no answer.
func TestA11ACallWhoseAttemptCannotBeRecordedDoesNotRun(t *testing.T) {
	store := &refusesIntents{}
	tool := &repeatableTool{name: "read_files", version: "v7", replay: tools.ReplaySafe}
	registry := tools.NewRegistry()
	registry.MustRegister(tool)

	sess := session.WithStore("You are pi-go.", store)
	agent, err := runtime.New(runtime.Config{
		Model: &ai.Scripted{
			Name: "fake-1",
			Replies: []ai.Response{ai.AssistantToolCalls(
				ai.ToolCall{ID: "call-1", Name: "read_files", Args: `{}`},
			)},
			StopWhenToolsSettled: true,
			Final:                ai.AssistantText("done"),
		},
		ModelName: "fake-1",
		Tools:     registry,
		Session:   sess,
		Policy:    runtime.DenyWrites,
		Observers: []events.Observer{runtime.NewRecorder()},
		Now:       fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := agent.Run(ctx, "read the file"); err == nil {
		t.Fatal("a turn that could not record what it was about to do reported success")
	}

	if tool.runs != 0 {
		t.Errorf("the tool ran %d times with no durable record of the attempt, want "+
			"0: a restart would read an untouched conversation and do it again",
			tool.runs)
	}
	if !carries(sess.Project().Messages, "not attempted") {
		t.Error("the model was told nothing about the call it asked for")
	}
}

// TestRestoreReportsAStoreItCannotRead is the paired control for reopening.
//
// A conversation that cannot be read back is not an empty conversation. Returning
// one would hand the caller a fresh, blank history and let it carry on writing into
// a record whose earlier contents are unknown.
func TestRestoreReportsAStoreItCannotRead(t *testing.T) {
	_, err := session.Restore(context.Background(), "You are pi-go.", unreadableStore{})
	if err == nil {
		t.Fatal("a store that could not be read produced a session, which reads as " +
			"a conversation that never happened")
	}
}

type unreadableStore struct{}

func (unreadableStore) Append(context.Context, ...session.Entry) error { return nil }
func (unreadableStore) Load(context.Context) ([]session.Entry, error) {
	return nil, errors.New("store unavailable")
}
