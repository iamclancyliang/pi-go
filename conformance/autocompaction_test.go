package conformance

import (
	"context"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// usingModel answers with a reply that reports the context it was given.
type usingModel struct{ usage ai.Usage }

func (m usingModel) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{Content: "answered", Model: "fake-1", Usage: m.usage}, nil
}

func autoCompactingAgent(t *testing.T, usage ai.Usage, policy runtime.AutoCompaction) (*runtime.Agent, *runtime.Recorder, *session.Session, *countingSummarizer) {
	t.Helper()
	rec := runtime.NewRecorder()
	sess := session.New("You are pi-go.")
	summarizer := &countingSummarizer{}
	agent, err := runtime.New(runtime.Config{
		Model: usingModel{usage: usage}, ModelName: "fake-1", Tools: tools.NewRegistry(), Session: sess,
		Observers: []events.Observer{rec}, Now: fixedClock(),
		Summarize: summarizer.summarize, AutoCompaction: policy,
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	return agent, rec, sess, summarizer
}

func compactionEvents(rec *runtime.Recorder) []events.Event {
	var out []events.Event
	for _, e := range rec.Events() {
		if e.Kind == events.KindCompactionStart || e.Kind == events.KindCompactionEnd {
			out = append(out, e)
		}
	}
	return out
}

var reported = func(in, out int) ai.Usage { return ai.Usage{InputTokens: in, OutputTokens: out, Reported: true} }

// TestAConversationNearItsWindowIsCompactedAfterTheRun, before the next
// request can overflow — Pi's threshold: the context the last reply reports
// is over the window less the reserve. The answer already arrived, so nothing
// is asked again; the next prompt starts from the summary.
func TestAConversationNearItsWindowIsCompactedAfterTheRun(t *testing.T) {
	agent, rec, sess, summarizer := autoCompactingAgent(t, reported(900, 50),
		runtime.AutoCompaction{Enabled: true, ContextWindow: 1000, ReserveTokens: 100})

	if err := agent.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summarizer.calls() != 1 {
		t.Fatalf("summarised %d times, want once", summarizer.calls())
	}
	if sess.Project().Complete {
		t.Fatal("the conversation was not compacted")
	}
	got := compactionEvents(rec)
	if len(got) != 2 || got[0].Detail.Reason != "threshold" || got[1].Kind != events.KindCompactionEnd || got[1].Detail.Err != "" {
		t.Fatalf("compaction events %+v", got)
	}
	// After the run ended, not during it: the run's own ending says it
	// finished before compaction began.
	ends := 0
	for _, e := range rec.Events() {
		if e.Kind == events.KindAgentEnd {
			ends++
		}
		if e.Kind == events.KindCompactionStart && ends == 0 {
			t.Fatal("compaction started before the run ended")
		}
	}
}

// TestNothingIsCompactedBelowTheThresholdOffOrWithoutAWindow. Without a
// recorded window there is no "close to it"; switched off, only overflow
// recovery remains.
func TestNothingIsCompactedBelowTheThresholdOffOrWithoutAWindow(t *testing.T) {
	cases := map[string]struct {
		usage  ai.Usage
		policy runtime.AutoCompaction
	}{
		"below":          {reported(800, 50), runtime.AutoCompaction{Enabled: true, ContextWindow: 1000, ReserveTokens: 100}},
		"off":            {reported(900, 50), runtime.AutoCompaction{ContextWindow: 1000, ReserveTokens: 100}},
		"no window":      {reported(900, 50), runtime.AutoCompaction{Enabled: true, ReserveTokens: 100}},
		"usage unstated": {ai.Usage{}, runtime.AutoCompaction{Enabled: true, ContextWindow: 1000, ReserveTokens: 100}},
	}
	for name, c := range cases {
		agent, rec, _, summarizer := autoCompactingAgent(t, c.usage, c.policy)
		if err := agent.Run(context.Background(), "hello"); err != nil {
			t.Fatalf("%s: Run: %v", name, err)
		}
		if summarizer.calls() != 0 || len(compactionEvents(rec)) != 0 {
			t.Fatalf("%s: compacted anyway", name)
		}
	}
}

// TestSwitchingAutoCompactionOffReachesTheNextRun.
func TestSwitchingAutoCompactionOffReachesTheNextRun(t *testing.T) {
	agent, _, _, summarizer := autoCompactingAgent(t, reported(900, 50),
		runtime.AutoCompaction{Enabled: true, ContextWindow: 1000, ReserveTokens: 100})
	agent.SetAutoCompaction(false)
	if agent.AutoCompaction() {
		t.Fatal("the switch did not take")
	}
	if err := agent.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summarizer.calls() != 0 {
		t.Fatal("compacted with auto-compaction off")
	}
}
