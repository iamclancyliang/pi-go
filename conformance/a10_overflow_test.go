package conformance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// TestA10OverflowGetsOneRecoveryThenFails pins the recovery budget.
//
// One shortening and one more attempt per user input. A second overflow ends the
// operation as failed: reporting it as an empty answer would read to the caller
// as the model having nothing to say, and the run would look successful while
// having answered nothing.
func TestA10OverflowGetsOneRecoveryThenFails(t *testing.T) {
	model := &overflowingModel{}
	summarizer := &countingSummarizer{}
	store := &session.MemoryStore{}
	sess := session.WithStore("You are pi-go.", store)

	rec := runtime.NewRecorder()
	agent, err := runtime.New(runtime.Config{
		Model:     model,
		ModelName: "fake-1",
		Tools:     tools.NewRegistry(),
		Session:   sess,
		Policy:    runtime.DenyWrites,
		Observers: []events.Observer{rec},
		Now:       fixedClock(),
		Summarize: summarizer.summarize,
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	runErr := agent.Run(ctx, "a very long question")

	if runErr == nil {
		t.Fatal("a run that never answered reported success")
	}
	if !errors.Is(runErr, ai.ErrContextOverflow) {
		t.Errorf("error = %v, want it to name the overflow so a caller can tell "+
			"this from an ordinary failure", runErr)
	}
	if got := model.calls(); got != 2 {
		t.Errorf("model calls = %d, want 2: one original and one after shortening", got)
	}
	if got := summarizer.calls(); got != 1 {
		t.Errorf("summarizer calls = %d, want 1: the second overflow must not "+
			"shorten again", got)
	}

	// The refused attempts are durable and auditable, and none of them is in the
	// conversation: a provider error is not something the conversation said.
	if got := sess.OverflowAttempts(); got != 2 {
		t.Errorf("recorded attempts = %d, want 2", got)
	}
	for _, m := range sess.Truth() {
		if strings.Contains(m.Content, "exceeded the model's context") {
			t.Error("an overflow refusal was written into the conversation")
		}
	}

	// The retry asked with a DIFFERENT context: the summary stands in for what
	// was refused, and the question being answered is still there. Asking again
	// with the same payload would resend exactly what the provider rejected.
	first, second := model.payloads()
	if sameContents(first, second) {
		t.Errorf("retry sent the same context that was just refused: %v",
			contentsOf(second))
	}
	if !carries(second, "SUMMARY") {
		t.Error("the retry does not carry the summary that replaced the history")
	}
	if !carries(second, "a very long question") {
		t.Error("the retry dropped the question it is meant to answer")
	}
	if carries(second, "exceeded the model's context") {
		t.Error("the retry resent the refusal it had just received")
	}
}

// overflowingModel always refuses for size, and records what it was asked.
type overflowingModel struct {
	mu       sync.Mutex
	requests [][]ai.Message
}

// refusedCost is what one refused request still costs. A provider that rejects a
// request for being too large has already read it, and bills for it.
var refusedCost = ai.Usage{InputTokens: 1200, OutputTokens: 0}

func (m *overflowingModel) Generate(_ context.Context, req ai.Request) (ai.Response, error) {
	m.mu.Lock()
	m.requests = append(m.requests, req.Messages)
	m.mu.Unlock()
	// The cost rides on the response even though the call failed: dropping it
	// here would under-report what the user actually paid.
	return ai.Response{Usage: refusedCost},
		fmt.Errorf("provider refused: %w", ai.ErrContextOverflow)
}

func (m *overflowingModel) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *overflowingModel) payloads() (first, second []ai.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) < 2 {
		return nil, nil
	}
	return m.requests[0], m.requests[1]
}

type countingSummarizer struct {
	mu sync.Mutex
	n  int
}

func (c *countingSummarizer) summarize(_ context.Context, truth []ai.Message) (string, []ai.Message, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	// Keep the last message: a compaction that dropped the question would
	// leave the retry asking nothing at all.
	var retained []ai.Message
	if len(truth) > 0 {
		retained = []ai.Message{truth[len(truth)-1]}
	}
	return "SUMMARY", retained, nil
}

func (c *countingSummarizer) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func sameContents(a, b []ai.Message) bool {
	return equal(contentsOf(a), contentsOf(b))
}

func carries(msgs []ai.Message, want string) bool {
	for _, m := range msgs {
		if strings.Contains(m.Content, want) {
			return true
		}
	}
	return false
}

// TestA10TerminalOverflowIsDurable pins that the failure outlives the process.
//
// A terminal state that lives only in a returned error is forgotten on restart:
// the process reopens, sees an unanswered question, and spends the same money
// reaching the same conclusion, handing the caller a fresh failure rather than
// the one already on record.
//
// Reopening the SAME operation returns that recorded result and spends nothing:
// no model call, no shortening. Asking a NEW question deliberately clears it —
// the budget and the terminal state both belong to the input that earned them.
func TestA10TerminalOverflowIsDurable(t *testing.T) {
	model := &overflowingModel{}
	summarizer := &countingSummarizer{}
	store := &session.MemoryStore{}
	sess := session.WithStore("You are pi-go.", store)

	rec := runtime.NewRecorder()
	agent, err := runtime.New(runtime.Config{
		Model:     model,
		ModelName: "fake-1",
		Tools:     tools.NewRegistry(),
		Session:   sess,
		Policy:    runtime.DenyWrites,
		Observers: []events.Observer{rec},
		Now:       fixedClock(),
		Summarize: summarizer.summarize,
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	runErr := agent.Run(ctx, "a very long question")
	if runErr == nil {
		t.Fatal("the run reported success")
	}

	reopened, err := session.Restore(context.Background(), "You are pi-go.", store)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	failure := reopened.Failure()
	if failure == nil {
		t.Fatal("the terminal state did not survive the reopen")
	}
	if failure.Code != runtime.CodeContextOverflow {
		t.Errorf("failure code = %q, want %q so a caller can branch on it rather "+
			"than parsing a message", failure.Code, runtime.CodeContextOverflow)
	}

	// Reopening returns the recorded result rather than re-deriving it. A
	// terminal state that can only be learnt by running the work again is not
	// worth having stored: the caller pays twice to be told the same thing.
	reopenedModel := &overflowingModel{}
	reopenedSummarizer := &countingSummarizer{}
	reopenedAgent, err := runtime.New(runtime.Config{
		Model:     reopenedModel,
		ModelName: "fake-1",
		Tools:     tools.NewRegistry(),
		Session:   reopened,
		Policy:    runtime.DenyWrites,
		Observers: []events.Observer{runtime.NewRecorder()},
		Now:       fixedClock(),
		Summarize: reopenedSummarizer.summarize,
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	outcome := reopenedAgent.Reopen()
	if !outcome.Failed() {
		t.Fatalf("reopened outcome = %q, want %q", outcome.Status, runtime.OutcomeFailed)
	}
	if outcome.Failure == nil || outcome.Failure.Code != runtime.CodeContextOverflow {
		t.Errorf("reopened outcome carries %+v, want the recorded %q",
			outcome.Failure, runtime.CodeContextOverflow)
	}
	if !errors.Is(outcome.Err(), ai.ErrContextOverflow) {
		t.Errorf("reopened error = %v, want the same error the original call "+
			"raised, so the two ways of learning this cannot disagree", outcome.Err())
	}
	// Both errors must render the recorded code and detail identically. They
	// differ only in their tail — the mid-run one keeps the provider's own
	// message, which the durable record does not store — and the shared head is
	// what a second rendering site would be free to drift away from.
	recorded := "runtime: " + failure.Code + ": " + failure.Detail
	if !strings.Contains(runErr.Error(), recorded) {
		t.Errorf("mid-run error %q does not render the record as %q", runErr, recorded)
	}
	// Nothing about the framework reaches a caller. Replacing it is meant to be
	// invisible outside the runtime, and both an error message and an event are
	// outside it — the event stream is rendered by clients.
	frameworkText := []string{"NodeRunError", "GraphRunError", "node path"}
	for _, leak := range frameworkText {
		if strings.Contains(runErr.Error(), leak) {
			t.Errorf("returned error names %q, which is the framework showing "+
				"through: %q", leak, runErr)
		}
	}
	for _, e := range rec.Events() {
		for _, leak := range frameworkText {
			if strings.Contains(e.Detail.Err, leak) {
				t.Errorf("event %s carries %q, which is the framework showing "+
					"through: %q", e.Kind, leak, e.Detail.Err)
			}
		}
	}
	if !strings.Contains(outcome.Err().Error(), recorded) {
		t.Errorf("reopened error %q does not render the record as %q",
			outcome.Err(), recorded)
	}
	if got := reopenedModel.calls(); got != 0 {
		t.Errorf("model calls while reopening = %d, want 0: the answer is already "+
			"on record and asking again costs money to reach it a second time", got)
	}
	if got := reopenedSummarizer.calls(); got != 0 {
		t.Errorf("shortenings while reopening = %d, want 0", got)
	}

	// A new question clears it: the previous attempts were answering something
	// else, and charging them here would leave this one unable to recover at all.
	if err := reopened.Append(ai.Message{Role: ai.RoleUser, Content: "a short question"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if reopened.Failure() != nil {
		t.Error("a new question inherited the previous question's terminal state")
	}
	if got := reopenedAgent.Reopen().Status; got != runtime.OutcomeOpen {
		t.Errorf("outcome after a new question = %q, want %q", got, runtime.OutcomeOpen)
	}
	if got := reopened.OverflowAttempts(); got != 0 {
		t.Errorf("attempts after a new question = %d, want 0", got)
	}
}

// TestA10WithNoWayToShortenTheFirstOverflowIsTerminal pins that the recovery
// budget is not the same thing as the ability to recover.
//
// One attempt is granted per input, but spending it requires something that can
// shorten the context. With nothing configured to do that, asking again would
// resend precisely what was just refused — so the FIRST refusal is already the
// end, and the reason recorded says the context could not be shortened rather
// than that the allowance was used up. A caller reading "recovery already spent"
// here would go looking for the attempt that spent it.
func TestA10WithNoWayToShortenTheFirstOverflowIsTerminal(t *testing.T) {
	model := &overflowingModel{}
	store := &session.MemoryStore{}
	sess := session.WithStore("You are pi-go.", store)

	agent, err := runtime.New(runtime.Config{
		Model:     model,
		ModelName: "fake-1",
		Tools:     tools.NewRegistry(),
		Session:   sess,
		Policy:    runtime.DenyWrites,
		Observers: []events.Observer{runtime.NewRecorder()},
		Now:       fixedClock(),
		// No Summarize: nothing here can make the request smaller.
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	runErr := agent.Run(ctx, "a very long question")
	if runErr == nil {
		t.Fatal("a run that never answered reported success")
	}
	if !errors.Is(runErr, ai.ErrContextOverflow) {
		t.Errorf("error = %v, want it to name the overflow", runErr)
	}

	if got := model.calls(); got != 1 {
		t.Errorf("model calls = %d, want 1: with no way to shorten the context, "+
			"asking again would resend exactly what was refused", got)
	}
	failure := sess.Failure()
	if failure == nil {
		t.Fatal("the operation did not end terminally, so a reopen would try the " +
			"same losing attempt again")
	}
	if failure.Code != runtime.CodeContextOverflow {
		t.Errorf("failure code = %q, want %q", failure.Code, runtime.CodeContextOverflow)
	}
	if !strings.Contains(failure.Detail, "shorten") {
		t.Errorf("failure detail = %q, want it to say the context could not be "+
			"shortened: %q sends a reader looking for an attempt that never happened",
			failure.Detail, "recovery already spent")
	}
	// The refused request was billed, and the ledger has to carry exactly what the
	// provider reported. A locally guessed number would be a different claim about
	// the user's money.
	if got, want := sess.OverflowUsage().Total(), refusedCost.Total(); got != want {
		t.Errorf("recorded cost = %d, want %d: what the provider reported is what "+
			"the ledger owes", got, want)
	}
}
