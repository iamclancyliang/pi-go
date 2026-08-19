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
		recorded    string
		declaredNow string
		known       bool
		wantReplay  bool
	}{
		{"both say safe", "safe", "safe", true, true},
		{"recorded safe, now forbidden", "safe", "never", true, false},
		{"recorded forbidden, now safe", "never", "safe", true, false},
		{"neither says safe", "never", "never", true, false},
		{"tool no longer registered", "safe", "", false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &session.MemoryStore{}
			live := session.WithStore("You are pi-go.", store)
			if err := live.RecordIntent(session.ToolIntent{
				CallID: "call-1", Tool: "delete_files", Args: `{"path":"/tmp/x"}`,
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
				func(string) (string, bool) { return c.declaredNow, c.known })
			if err != nil {
				t.Fatalf("RecoverUnsettled: %v", err)
			}

			if c.wantReplay {
				if len(replayable) != 1 {
					t.Errorf("replayable = %v, want the call to be repeatable", replayable)
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
		CallID: "call-1", Tool: "delete_files", Replay: "never",
	}); err != nil {
		t.Fatalf("RecordIntent: %v", err)
	}
	if err := live.Settle(session.ToolSettlement{
		CallID: "call-1", Result: "REAL-RESULT",
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
		func(string) (string, bool) { return "safe", true })
	if err != nil {
		t.Fatalf("RecoverUnsettled: %v", err)
	}
	if len(replayable) != 0 {
		t.Errorf("recovery offered to repeat a call that already has an answer: %v", replayable)
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
