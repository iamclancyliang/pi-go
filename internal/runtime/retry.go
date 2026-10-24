package runtime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
)

// RetryPolicy is how a model call that failed transiently is tried again.
//
// Each retry is a NEW call, counted and billed like any other: the ports keep
// one call to one billed request, and this layer adds requests only on
// purpose, announcing each one. What is retried is decided by the failure's
// classification (ai.Retryable) — a throttle or a server fault, or whatever
// the provider itself said to retry — never by reading its text. An exhausted
// balance is never retried, and an overflow is not either: the same request
// would be refused the same way, and shortening it is compaction's job.
//
// The zero value retries nothing, so a runtime nobody configured behaves as
// it always did.
type RetryPolicy struct {
	Enabled bool

	// MaxRetries bounds the retries after the first attempt.
	MaxRetries int

	// BaseDelay is the wait before the first retry; each one after doubles it.
	BaseDelay time.Duration

	// Sleep waits, returning early with the context's error if it ends. Nil
	// uses a timer; tests supply one that does not wait.
	Sleep func(context.Context, time.Duration) error
}

// DefaultRetryPolicy is Pi's: on, three retries, two seconds doubling
// (settings-manager.ts getRetrySettings at the pin).
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{Enabled: true, MaxRetries: 3, BaseDelay: 2 * time.Second}
}

// errRetryCancelled is a backoff ended by AbortRetry rather than by the run.
var errRetryCancelled = errors.New("retry cancelled")

// retrier holds the policy a run retries under and the wait in progress, so
// a client can switch retrying off or cut a wait short while a run is live.
type retrier struct {
	mu     sync.Mutex
	policy RetryPolicy
	cancel context.CancelFunc
}

func (r *retrier) snapshot() RetryPolicy {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.policy
}

func (r *retrier) setEnabled(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.policy.Enabled = enabled
}

// abort ends the wait in progress, reporting whether there was one.
func (r *retrier) abort() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel == nil {
		return false
	}
	r.cancel()
	return true
}

// eligible reports whether a failure after `done` retries earns another.
func (r *retrier) eligible(ctx context.Context, err error, done int) bool {
	if err == nil || ctx.Err() != nil || errors.Is(err, ai.ErrContextOverflow) {
		return false
	}
	policy := r.snapshot()
	return policy.Enabled && done < policy.MaxRetries && ai.Retryable(err)
}

// wait announces retry number *attempt+1 and waits for its turn.
//
// A wait that ends early resets the count, as Pi's does, and says so: the run
// ending returns its own error so it reads as stopped, and AbortRetry returns
// errRetryCancelled so the caller reports the failure being retried.
func (r *retrier) wait(ctx context.Context, attempt *int, cause error, em *emitter) error {
	policy := r.snapshot()
	*attempt++
	delay := policy.BaseDelay << (*attempt - 1)
	em.emit(events.KindRetryStart, func(e *events.Event) {
		e.Detail.Attempt = *attempt
		e.Detail.MaxAttempts = policy.MaxRetries
		e.Detail.DelayMs = delay.Milliseconds()
		e.Detail.Err = cause.Error()
	})

	waitCtx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()
	sleep := policy.Sleep
	if sleep == nil {
		sleep = sleepFor
	}
	err := sleep(waitCtx, delay)
	r.mu.Lock()
	r.cancel = nil
	r.mu.Unlock()
	cancel()
	if err == nil {
		return nil
	}

	ended := *attempt
	*attempt = 0
	no := false
	em.emit(events.KindRetryEnd, func(e *events.Event) {
		e.Detail.Attempt = ended
		e.Detail.Succeeded = &no
		e.Detail.Err = "Retry cancelled"
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errRetryCancelled
}

// settle closes a retry sequence once its last attempt has an outcome.
func (r *retrier) settle(attempt int, final error, em *emitter) {
	if attempt == 0 {
		return
	}
	succeeded := final == nil
	em.emit(events.KindRetryEnd, func(e *events.Event) {
		e.Detail.Attempt = attempt
		e.Detail.Succeeded = &succeeded
		if final != nil {
			e.Detail.Err = final.Error()
		}
	})
}

func sleepFor(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
