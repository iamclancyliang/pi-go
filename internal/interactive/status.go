package interactive

import (
	"strconv"
	"sync"
	"time"

	"github.com/iamclancyliang/pi-go/internal/tui"
)

// statusIndicator is the spinner line above the editor while something is
// under way — Pi's StatusIndicator: working, waiting to retry, compacting.
type statusIndicator struct {
	kind   string
	loader *tui.Loader

	mu   sync.Mutex
	stop chan struct{}
}

func (s *statusIndicator) Invalidate()               { s.loader.Invalidate() }
func (s *statusIndicator) Render(width int) []string { return s.loader.Render(width) }

func (s *statusIndicator) dispose() {
	s.loader.Stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
}

type renderFunc func()

func (f renderFunc) RequestRender() { f() }

func newStatus(env *Env, kind, spinnerColor, message string) *statusIndicator {
	return &statusIndicator{
		kind: kind,
		loader: tui.NewLoader(renderFunc(env.requestRender), env.Theme.FgStyle(spinnerColor), env.Theme.FgStyle("muted"),
			message, nil, 0),
	}
}

// workingStatus is shown while the agent works.
func workingStatus(env *Env, message string) *statusIndicator {
	return newStatus(env, "working", "accent", message)
}

// compactionStatus is shown while the conversation is compacted.
func compactionStatus(env *Env, reason string) *statusIndicator {
	hint := "(" + env.KeyText("app.interrupt") + " to cancel)"
	label := "Auto-compacting... " + hint
	switch reason {
	case "manual":
		label = "Compacting context... " + hint
	case "overflow":
		label = "Context overflow detected, Auto-compacting... " + hint
	}
	return newStatus(env, "compaction", "accent", label)
}

// retryStatus counts down to the next attempt of a failed model call.
func retryStatus(env *Env, attempt, maxAttempts int, delay time.Duration) *statusIndicator {
	message := func(seconds int) string {
		return "Retrying (" + strconv.Itoa(attempt) + "/" + strconv.Itoa(maxAttempts) + ") in " +
			strconv.Itoa(seconds) + "s... (" + env.KeyText("app.interrupt") + " to cancel)"
	}
	seconds := int((delay + time.Second - 1) / time.Second)
	s := newStatus(env, "retry", "warning", message(seconds))
	stop := make(chan struct{})
	s.stop = stop
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for remaining := seconds - 1; remaining >= 0; remaining-- {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s.loader.SetMessage(message(remaining))
			}
		}
	}()
	return s
}
