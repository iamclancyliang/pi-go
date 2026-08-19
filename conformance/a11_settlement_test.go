package conformance

import (
	"context"
	"testing"

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
			settled, ok := reopened.Settlement("call-1")
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
	if settled, ok := reopened.Settlement("call-1"); !ok || settled.Result != "REAL-RESULT" {
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
