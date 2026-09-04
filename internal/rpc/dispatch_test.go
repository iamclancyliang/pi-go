package rpc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/session"
)

type runFunc func(ctx context.Context, prompt string) error

func (f runFunc) Run(ctx context.Context, prompt string) error { return f(ctx, prompt) }

func newState(t *testing.T) (*session.Session, State) {
	t.Helper()
	sess := session.New("You are pi-go.")
	return sess, NewState(sess, "scripted", "scripted-1")
}

// TestAnUnknownVerbAndAnUnbuiltOneAreDifferentAnswers. A client must be able to
// tell "no such command" from "a real command not built yet": the first is its
// own bug, the second is a gap this repository tracks.
func TestAnUnknownVerbAndAnUnbuiltOneAreDifferentAnswers(t *testing.T) {
	_, state := newState(t)

	unknown := Dispatch(context.Background(), Command{ID: "1", Command: "flibbertigibbet"}, nil, state)
	if unknown.OK || unknown.Error.Kind != FailUnknownCommand {
		t.Fatalf("an invented command was not unknown: %+v", unknown.Error)
	}

	unbuilt := Dispatch(context.Background(), Command{ID: "2", Command: "cycle_model"}, nil, state)
	if unbuilt.OK || unbuilt.Error.Kind != FailUnimplemented {
		t.Fatalf("a real Pi command was not unimplemented: %+v", unbuilt.Error)
	}
}

// TestEveryResponseEchoesTheIdItAnswers, because a response attributable only
// by arrival order is the ambiguity this protocol exists to remove.
func TestEveryResponseEchoesTheIdItAnswers(t *testing.T) {
	_, state := newState(t)
	resp := Dispatch(context.Background(), Command{ID: "abc-123", Command: "get_state"}, nil, state)
	if resp.ID != "abc-123" || resp.Command != "get_state" {
		t.Fatalf("the response did not echo its command: %+v", resp)
	}
	if !resp.OK {
		t.Fatalf("get_state failed: %+v", resp.Error)
	}
}

// TestGetStateReportsWhatTheSessionIs.
func TestGetStateReportsWhatTheSessionIs(t *testing.T) {
	sess, state := newState(t)
	_ = sess.SetName("my work")

	resp := Dispatch(context.Background(), Command{ID: "1", Command: "get_state"}, nil, state)
	var data stateData
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("state is not JSON: %v", err)
	}
	if data.Provider != "scripted" || data.Model != "scripted-1" || data.SessionName != "my work" {
		t.Fatalf("state does not describe the session: %+v", data)
	}
}

// TestAPromptRunsAndAcknowledges. The response is receipt; the answer is in the
// events, which a prompt-less test cannot see, so this checks the run happened
// and the ack came back.
func TestAPromptRunsAndAcknowledges(t *testing.T) {
	_, state := newState(t)
	ran := ""
	run := runFunc(func(_ context.Context, p string) error { ran = p; return nil })

	resp := Dispatch(context.Background(), Command{ID: "1", Command: "prompt", Message: "hello"}, run, state)
	if !resp.OK {
		t.Fatalf("prompt failed: %+v", resp.Error)
	}
	if ran != "hello" {
		t.Fatalf("the prompt did not reach the runner: %q", ran)
	}
	if len(resp.Data) != 0 {
		t.Fatalf("the ack carried data; the outcome belongs in the events: %s", resp.Data)
	}
}

// TestAnEmptyPromptIsABadArgument, not an internal error and not a run.
func TestAnEmptyPromptIsABadArgument(t *testing.T) {
	_, state := newState(t)
	called := false
	run := runFunc(func(context.Context, string) error { called = true; return nil })

	resp := Dispatch(context.Background(), Command{ID: "1", Command: "prompt", Message: "   "}, run, state)
	if resp.OK || resp.Error.Kind != FailBadArgument {
		t.Fatalf("an empty prompt was not a bad argument: %+v", resp.Error)
	}
	if called {
		t.Fatal("an empty prompt reached the runner")
	}
}

// TestAProviderFailureKeepsItsClassification is the reason the taxonomy is on
// the wire at all: a client learns whether to wait, pay or fix without prose.
func TestAProviderFailureKeepsItsClassification(t *testing.T) {
	_, state := newState(t)
	pe := &ai.ProviderError{Provider: "scripted", Failure: ai.FailureQuota, Detail: "balance gone"}
	run := runFunc(func(context.Context, string) error { return pe })

	resp := Dispatch(context.Background(), Command{ID: "1", Command: "prompt", Message: "x"}, run, state)
	if resp.OK || resp.Error.Kind != FailProvider {
		t.Fatalf("a provider failure was misclassified: %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Detail, string(ai.FailureQuota)) {
		t.Fatalf("the classification was flattened away: %q", resp.Error.Detail)
	}
}

// TestSessionStatsHasNoCurrency: pi-go ledgers tokens and computes no cost, so
// the payload must not grow a money field a client would trust.
func TestSessionStatsHasNoCurrency(t *testing.T) {
	_, state := newState(t)
	resp := Dispatch(context.Background(), Command{ID: "1", Command: "get_session_stats"}, nil, state)
	if strings.Contains(string(resp.Data), "cost") || strings.Contains(string(resp.Data), "currency") {
		t.Fatalf("stats claimed a cost pi-go does not compute: %s", resp.Data)
	}
}
