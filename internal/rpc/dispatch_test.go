package rpc

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/session"
)

// fakeRun is a prompt in flight that ends when its context does, or when
// released. It records what was steered and followed into it.
type fakeRun struct {
	ctx      context.Context
	release  chan struct{}
	mu       sync.Mutex
	steered  []string
	followed []string
}

func (r *fakeRun) Steer(text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steered = append(r.steered, text)
	return nil
}

func (r *fakeRun) Follow(text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.followed = append(r.followed, text)
	return nil
}

func (r *fakeRun) Wait() error {
	select {
	case <-r.ctx.Done():
		return r.ctx.Err()
	case <-r.release:
		return nil
	}
}

// fakeRunner hands out fakeRuns and remembers the last one.
type fakeRunner struct {
	mu       sync.Mutex
	last     *fakeRun
	prompts  []string
	startErr error
}

func (f *fakeRunner) Start(ctx context.Context, prompt string) (Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return nil, f.startErr
	}
	f.prompts = append(f.prompts, prompt)
	f.last = &fakeRun{ctx: ctx, release: make(chan struct{})}
	return f.last, nil
}

func newChannel(t *testing.T, runner Runner) (*Channel, *session.Session) {
	t.Helper()
	sess := session.New("You are pi-go.")
	return NewChannel(runner, NewState(sess, "scripted", "scripted-1")), sess
}

func dispatch(c *Channel, cmd Command) Response {
	return c.Dispatch(context.Background(), cmd)
}

// TestAnUnknownVerbAndAnUnbuiltOneAreDifferentAnswers. A client must be able to
// tell "no such command" from "a real command not built yet": the first is its
// own bug, the second is a gap this repository tracks.
func TestAnUnknownVerbAndAnUnbuiltOneAreDifferentAnswers(t *testing.T) {
	c, _ := newChannel(t, nil)

	unknown := dispatch(c, Command{ID: "1", Command: "flibbertigibbet"})
	if unknown.OK || unknown.Error.Kind != FailUnknownCommand {
		t.Fatalf("an invented command was not unknown: %+v", unknown.Error)
	}
	unbuilt := dispatch(c, Command{ID: "2", Command: "cycle_model"})
	if unbuilt.OK || unbuilt.Error.Kind != FailUnimplemented {
		t.Fatalf("a real Pi command was not unimplemented: %+v", unbuilt.Error)
	}
}

// TestEveryResponseEchoesTheIdItAnswers, because a response attributable only
// by arrival order is the ambiguity this protocol exists to remove.
func TestEveryResponseEchoesTheIdItAnswers(t *testing.T) {
	c, _ := newChannel(t, nil)
	resp := dispatch(c, Command{ID: "abc-123", Command: "get_state"})
	if resp.ID != "abc-123" || resp.Command != "get_state" || !resp.OK {
		t.Fatalf("the response did not echo its command: %+v", resp)
	}
}

// TestAPromptIsAcknowledgedBeforeItFinishes is the ack-then-events contract:
// the response is a receipt, returned while the run is still in flight, and
// get_state says so.
func TestAPromptIsAcknowledgedBeforeItFinishes(t *testing.T) {
	runner := &fakeRunner{}
	c, _ := newChannel(t, runner)

	resp := dispatch(c, Command{ID: "1", Command: "prompt", Message: "hello"})
	if !resp.OK || len(resp.Data) != 0 {
		t.Fatalf("the ack is wrong: %+v", resp)
	}
	var state stateData
	_ = json.Unmarshal(dispatch(c, Command{ID: "2", Command: "get_state"}).Data, &state)
	if !state.Running {
		t.Fatal("the run was acknowledged but get_state says nothing is running")
	}
	close(runner.last.release)
	c.Settle()
	_ = json.Unmarshal(dispatch(c, Command{ID: "3", Command: "get_state"}).Data, &state)
	if state.Running {
		t.Fatal("the run finished but get_state still says it is running")
	}
}

// TestASecondPromptWhileOneRunsIsBusy, not queued: a queue the client cannot
// see into is a prompt it believes is running.
func TestASecondPromptWhileOneRunsIsBusy(t *testing.T) {
	runner := &fakeRunner{}
	c, _ := newChannel(t, runner)
	dispatch(c, Command{ID: "1", Command: "prompt", Message: "first"})

	second := dispatch(c, Command{ID: "2", Command: "prompt", Message: "second"})
	if second.OK || second.Error.Kind != FailBusy {
		t.Fatalf("a second prompt was not refused as busy: %+v", second)
	}
	if len(runner.prompts) != 1 {
		t.Fatalf("the second prompt reached the runner: %v", runner.prompts)
	}
	close(runner.last.release)
	c.Settle()

	// Once the first has finished, the channel is free again.
	third := dispatch(c, Command{ID: "3", Command: "prompt", Message: "third"})
	if !third.OK {
		t.Fatalf("a prompt after the first finished was refused: %+v", third)
	}
	close(runner.last.release)
	c.Settle()
}

// TestAbortCancelsTheRunAndSaysWhetherThereWasOne.
func TestAbortCancelsTheRunAndSaysWhetherThereWasOne(t *testing.T) {
	runner := &fakeRunner{}
	c, _ := newChannel(t, runner)

	idle := dispatch(c, Command{ID: "0", Command: "abort"})
	if !idle.OK || !strings.Contains(string(idle.Data), `"aborted":false`) {
		t.Fatalf("aborting nothing did not say so: %+v", idle)
	}

	dispatch(c, Command{ID: "1", Command: "prompt", Message: "work"})
	aborted := dispatch(c, Command{ID: "2", Command: "abort"})
	if !aborted.OK || !strings.Contains(string(aborted.Data), `"aborted":true`) {
		t.Fatalf("abort did not report the run it cancelled: %+v", aborted)
	}
	done := make(chan struct{})
	go func() { c.Settle(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the aborted run never settled: abort did not cancel its context")
	}
	if runner.last.ctx.Err() == nil {
		t.Fatal("the run's context was not cancelled")
	}
}

// TestSteerAndFollowUpReachTheRunInFlightAndNothingElse. During a run they are
// forwarded to it; with nothing running they are a typed not_running rather
// than a silent drop or a queued prompt.
func TestSteerAndFollowUpReachTheRunInFlightAndNothingElse(t *testing.T) {
	runner := &fakeRunner{}
	c, _ := newChannel(t, runner)

	idle := dispatch(c, Command{ID: "0", Command: "steer", Message: "now"})
	if idle.OK || idle.Error.Kind != FailNotRunning {
		t.Fatalf("steering nothing was not not_running: %+v", idle)
	}

	dispatch(c, Command{ID: "1", Command: "prompt", Message: "work"})
	if r := dispatch(c, Command{ID: "2", Command: "steer", Message: "turn left"}); !r.OK {
		t.Fatalf("steer during a run failed: %+v", r)
	}
	if r := dispatch(c, Command{ID: "3", Command: "follow_up", Message: "then stop"}); !r.OK {
		t.Fatalf("follow_up during a run failed: %+v", r)
	}
	empty := dispatch(c, Command{ID: "4", Command: "steer", Message: " "})
	if empty.OK || empty.Error.Kind != FailBadArgument {
		t.Fatalf("an empty steer was not a bad argument: %+v", empty)
	}
	close(runner.last.release)
	c.Settle()

	if strings.Join(runner.last.steered, ",") != "turn left" || strings.Join(runner.last.followed, ",") != "then stop" {
		t.Fatalf("the messages did not reach the run as what they were: steered=%v followed=%v",
			runner.last.steered, runner.last.followed)
	}
}

// TestAnEmptyPromptIsABadArgument, not an internal error and not a run.
func TestAnEmptyPromptIsABadArgument(t *testing.T) {
	runner := &fakeRunner{}
	c, _ := newChannel(t, runner)
	resp := dispatch(c, Command{ID: "1", Command: "prompt", Message: "   "})
	if resp.OK || resp.Error.Kind != FailBadArgument {
		t.Fatalf("an empty prompt was not a bad argument: %+v", resp.Error)
	}
	if len(runner.prompts) != 0 {
		t.Fatal("an empty prompt reached the runner")
	}
}

// TestAProviderFailureAtStartKeepsItsClassification is the reason the taxonomy
// is on the wire at all: a client learns whether to wait, pay or fix without
// prose. (A failure after the run starts is on the stream, in agent_end.)
func TestAProviderFailureAtStartKeepsItsClassification(t *testing.T) {
	runner := &fakeRunner{startErr: &ai.ProviderError{Provider: "scripted", Failure: ai.FailureQuota, Detail: "gone"}}
	c, _ := newChannel(t, runner)
	resp := dispatch(c, Command{ID: "1", Command: "prompt", Message: "x"})
	if resp.OK || resp.Error.Kind != FailProvider {
		t.Fatalf("a provider failure was misclassified: %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Detail, string(ai.FailureQuota)) {
		t.Fatalf("the classification was flattened away: %q", resp.Error.Detail)
	}
}

// TestGetStateReportsWhatTheSessionIs.
func TestGetStateReportsWhatTheSessionIs(t *testing.T) {
	c, sess := newChannel(t, nil)
	_ = sess.SetName("my work")
	var data stateData
	if err := json.Unmarshal(dispatch(c, Command{ID: "1", Command: "get_state"}).Data, &data); err != nil {
		t.Fatalf("state is not JSON: %v", err)
	}
	if data.Provider != "scripted" || data.Model != "scripted-1" || data.SessionName != "my work" {
		t.Fatalf("state does not describe the session: %+v", data)
	}
}

// TestSessionStatsHasNoCurrency: pi-go ledgers tokens and computes no cost, so
// the payload must not grow a money field a client would trust.
func TestSessionStatsHasNoCurrency(t *testing.T) {
	c, _ := newChannel(t, nil)
	resp := dispatch(c, Command{ID: "1", Command: "get_session_stats"})
	if strings.Contains(string(resp.Data), "cost") || strings.Contains(string(resp.Data), "currency") {
		t.Fatalf("stats claimed a cost pi-go does not compute: %s", resp.Data)
	}
}
