package runtime

import (
	"context"
	"fmt"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
)

// AutoCompaction compacts a conversation once it has grown close to the
// model's context window, before the next request can overflow it.
//
// Pi's threshold (shouldCompact, compaction.ts at the pin): the context the
// last reply reports is more than the window less a reserve kept for the
// next reply. It is checked after a run that ended normally, and the answer
// already arrived, so nothing is asked again — the next prompt starts from
// the summary.
//
// This switch governs only that. Recovering from an overflow, by compacting
// and asking once more, is always on: switched off, a conversation that
// outgrew its model would simply fail, and nothing a user gains from that is
// worth the turn it loses. Pi's one switch governs both.
type AutoCompaction struct {
	Enabled bool

	// ContextWindow is the model's window in tokens. Zero turns the threshold
	// off: without a recorded window there is no "close to it", and a guess
	// would compact conversations that were nowhere near the limit.
	ContextWindow int

	// ReserveTokens is the room kept for the next reply. Zero uses Pi's 16384.
	ReserveTokens int
}

// DefaultReserveTokens is Pi's reserve (DEFAULT_COMPACTION_SETTINGS).
const DefaultReserveTokens = 16384

// AutoCompaction reports whether the threshold compaction is on.
func (a *Agent) AutoCompaction() bool {
	a.compactMu.Lock()
	defer a.compactMu.Unlock()
	return a.compact.Enabled
}

// SetAutoCompaction switches the threshold compaction from the next run's
// end on. Overflow recovery is unaffected.
func (a *Agent) SetAutoCompaction(enabled bool) {
	a.compactMu.Lock()
	defer a.compactMu.Unlock()
	a.compact.Enabled = enabled
}

// noteContext records the context a reply reports, which is what the
// threshold is measured against. A reply that reported no usage leaves the
// last measurement standing rather than reading as an empty context.
func (a *Agent) noteContext(used ai.Usage) {
	if !used.Reported {
		return
	}
	a.compactMu.Lock()
	defer a.compactMu.Unlock()
	a.lastContext = used.InputTokens + used.OutputTokens
}

// compactIfNearTheWindow runs the threshold compaction after a run that
// ended normally. A failure is announced, not returned: the run it follows
// already succeeded, and the conversation is no worse than before.
func (a *Agent) compactIfNearTheWindow(ctx context.Context) {
	a.compactMu.Lock()
	policy, used := a.compact, a.lastContext
	a.compactMu.Unlock()

	reserve := policy.ReserveTokens
	if reserve <= 0 {
		reserve = DefaultReserveTokens
	}
	if !policy.Enabled || policy.ContextWindow <= 0 || used <= policy.ContextWindow-reserve || a.cfg.Summarize == nil {
		return
	}

	a.emitter.emit(events.KindCompactionStart, func(e *events.Event) { e.Detail.Reason = "threshold" })
	err := a.compactNow(ctx)
	a.emitter.emit(events.KindCompactionEnd, func(e *events.Event) {
		e.Detail.Reason = "threshold"
		if err != nil {
			e.Detail.Err = err.Error()
		}
	})
}

func (a *Agent) compactNow(ctx context.Context) error {
	summary, retained, err := a.cfg.Summarize(ctx, a.cfg.Session.Truth())
	if err != nil {
		return fmt.Errorf("runtime: compacting near the window: %w", err)
	}
	if err := a.cfg.Session.Compact(summary, retained); err != nil {
		return err
	}
	// The measurement described the context before it was shortened. Kept,
	// it would compact again after the next run whatever that run's reply
	// reported, which is the stale-usage loop Pi guards against too.
	a.compactMu.Lock()
	a.lastContext = 0
	a.compactMu.Unlock()
	return nil
}
