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

func (m *overflowingModel) Generate(_ context.Context, req ai.Request) (ai.Response, error) {
	m.mu.Lock()
	m.requests = append(m.requests, req.Messages)
	m.mu.Unlock()
	return ai.Response{}, fmt.Errorf("provider refused: %w", ai.ErrContextOverflow)
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
