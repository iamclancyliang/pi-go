package conformance

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// failingThen fails its first `failures` calls with `failure`, each reporting
// what it consumed, and answers after that.
type failingThen struct {
	mu       sync.Mutex
	calls    int
	failures int
	failure  ai.Failure
}

func (f *failingThen) Generate(context.Context, ai.Request) (ai.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.failures {
		refused := &ai.ProviderError{Provider: "fake", Failure: f.failure, Status: 503, Detail: "try later"}
		refused.Record(ai.Usage{InputTokens: 10})
		return ai.Response{}, refused
	}
	return ai.Response{Content: "answered", Model: "fake-1", Usage: ai.Usage{InputTokens: 10, OutputTokens: 2}}, nil
}

func (f *failingThen) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// noWait records the delays a retry asked for, without waiting them.
type noWait struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (n *noWait) sleep(_ context.Context, d time.Duration) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.delays = append(n.delays, d)
	return nil
}

func retryingAgent(t *testing.T, model ai.Port, policy runtime.RetryPolicy) (*runtime.Agent, *runtime.Recorder, *session.Session) {
	t.Helper()
	rec := runtime.NewRecorder()
	sess := session.New("You are pi-go.")
	agent, err := runtime.New(runtime.Config{
		Model: model, ModelName: "fake-1", Tools: tools.NewRegistry(), Session: sess,
		Observers: []events.Observer{rec}, Now: fixedClock(), Retry: policy,
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	return agent, rec, sess
}

func retryEvents(rec *runtime.Recorder) (starts, ends []events.Event) {
	for _, e := range rec.Events() {
		switch e.Kind {
		case events.KindRetryStart:
			starts = append(starts, e)
		case events.KindRetryEnd:
			ends = append(ends, e)
		}
	}
	return starts, ends
}

// TestATransientFailureIsRetriedWithBackoffAndEveryAttemptLedgered. Pi
// retries a failed turn after a throttle or a server fault, doubling the wait
// each time. Each retry is a new billed call, so the attempts that failed are
// in the ledger, and the retrying is announced so a client can show why
// nothing is arriving.
func TestATransientFailureIsRetriedWithBackoffAndEveryAttemptLedgered(t *testing.T) {
	model := &failingThen{failures: 2, failure: ai.FailureTransient}
	waits := &noWait{}
	agent, rec, sess := retryingAgent(t, model, runtime.RetryPolicy{
		Enabled: true, MaxRetries: 3, BaseDelay: 100 * time.Millisecond, Sleep: waits.sleep})

	if err := agent.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("a failure another attempt survived still failed the run: %v", err)
	}
	if got := model.count(); got != 3 {
		t.Fatalf("model calls = %d, want two failures and one answer", got)
	}
	if len(waits.delays) != 2 || waits.delays[0] != 100*time.Millisecond || waits.delays[1] != 200*time.Millisecond {
		t.Fatalf("waits were %v, want 100ms then 200ms", waits.delays)
	}
	starts, ends := retryEvents(rec)
	if len(starts) != 2 || starts[1].Detail.Attempt != 2 || starts[1].Detail.MaxAttempts != 3 || starts[1].Detail.DelayMs != 200 {
		t.Fatalf("retry starts %+v", starts)
	}
	if len(ends) != 1 || ends[0].Detail.Succeeded == nil || !*ends[0].Detail.Succeeded || ends[0].Detail.Attempt != 2 {
		t.Fatalf("retry ends %+v", ends)
	}
	if used := sess.Usage(); used.InputTokens != 30 {
		t.Fatalf("ledgered input = %d, want all three attempts' 30", used.InputTokens)
	}
}

// TestAnExhaustedBalanceIsNeverRetried: every attempt spends more of what is
// already gone.
func TestAnExhaustedBalanceIsNeverRetried(t *testing.T) {
	model := &failingThen{failures: 5, failure: ai.FailureQuota}
	agent, rec, _ := retryingAgent(t, model, runtime.RetryPolicy{
		Enabled: true, MaxRetries: 3, BaseDelay: time.Millisecond, Sleep: (&noWait{}).sleep})

	if err := agent.Run(context.Background(), "hello"); err == nil {
		t.Fatal("an exhausted balance answered")
	}
	if got := model.count(); got != 1 {
		t.Fatalf("an exhausted balance was asked %d times", got)
	}
	if starts, _ := retryEvents(rec); len(starts) != 0 {
		t.Fatalf("a retry was announced for a failure that cannot succeed: %+v", starts)
	}
}

// TestRetryingStopsAtItsBudgetAndSaysHowItEnded.
func TestRetryingStopsAtItsBudgetAndSaysHowItEnded(t *testing.T) {
	model := &failingThen{failures: 100, failure: ai.FailureThrottled}
	agent, rec, _ := retryingAgent(t, model, runtime.RetryPolicy{
		Enabled: true, MaxRetries: 2, BaseDelay: time.Millisecond, Sleep: (&noWait{}).sleep})

	err := agent.Run(context.Background(), "hello")
	if f, ok := ai.FailureOf(err); !ok || f != ai.FailureThrottled {
		t.Fatalf("the run ended with %v, want the throttle it kept hitting", err)
	}
	if got := model.count(); got != 3 {
		t.Fatalf("model calls = %d, want the first and two retries", got)
	}
	_, ends := retryEvents(rec)
	if len(ends) != 1 || ends[0].Detail.Succeeded == nil || *ends[0].Detail.Succeeded || ends[0].Detail.Err == "" {
		t.Fatalf("retry ends %+v, want one saying it failed and why", ends)
	}
}

// TestSwitchedOffNothingIsRetried, and a runtime nobody configured is off.
func TestSwitchedOffNothingIsRetried(t *testing.T) {
	model := &failingThen{failures: 1, failure: ai.FailureTransient}
	agent, _, _ := retryingAgent(t, model, runtime.RetryPolicy{})
	if err := agent.Run(context.Background(), "hello"); err == nil {
		t.Fatal("a failure was retried with retrying off")
	}
	if got := model.count(); got != 1 {
		t.Fatalf("model calls = %d", got)
	}
}

// TestAbortingARetryLetsTheFailureStand. The wait is what is cancelled, not
// the run: the run ends the way the failure it was waiting to retry ends it,
// rather than reading as stopped by the caller.
func TestAbortingARetryLetsTheFailureStand(t *testing.T) {
	model := &failingThen{failures: 5, failure: ai.FailureTransient}
	waiting := make(chan struct{})
	agent, rec, _ := retryingAgent(t, model, runtime.RetryPolicy{
		Enabled: true, MaxRetries: 3, BaseDelay: time.Hour,
		Sleep: func(ctx context.Context, _ time.Duration) error {
			close(waiting)
			<-ctx.Done()
			return ctx.Err()
		}})

	done := make(chan error, 1)
	go func() { done <- agent.Run(context.Background(), "hello") }()
	select {
	case <-waiting:
	case err := <-done:
		t.Fatalf("the run ended without waiting to retry: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no retry wait began")
	}
	if !agent.AbortRetry() {
		t.Fatal("AbortRetry found no wait in progress")
	}
	err := <-done
	if f, ok := ai.FailureOf(err); !ok || f != ai.FailureTransient || errors.Is(err, context.Canceled) {
		t.Fatalf("an aborted retry ended the run with %v, want the transient failure", err)
	}
	_, ends := retryEvents(rec)
	if len(ends) != 1 || ends[0].Detail.Err != "Retry cancelled" {
		t.Fatalf("retry ends %+v", ends)
	}
}
