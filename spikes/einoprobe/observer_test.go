package einoprobe

import (
	"context"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// TestObservingModelSelfCheck verifies the instrument itself before it is used
// to observe eino. If the fake model is broken, everything measured with it is
// worthless — so this checks the recorder, not eino.
func TestObservingModelSelfCheck(t *testing.T) {
	reply := schema.AssistantMessage("ok", nil)
	m := newObservingModel(reply)

	got, err := m.Generate(context.Background(), []*schema.Message{
		schema.SystemMessage("sys"),
		schema.UserMessage("hello"),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.Content != "ok" {
		t.Fatalf("reply content = %q, want %q", got.Content, "ok")
	}

	sr, err := m.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer sr.Close()
	if _, err := sr.Recv(); err != nil {
		t.Fatalf("Recv: %v", err)
	}

	calls := m.observed()
	if len(calls) != 2 {
		t.Fatalf("recorded %d calls, want 2", len(calls))
	}
	if calls[0].Method != "Generate" || calls[0].NumInput != 2 {
		t.Errorf("call 0 = %v, want Generate with 2 inputs", calls[0])
	}
	if calls[0].Roles[0] != "system" || calls[0].Roles[1] != "user" {
		t.Errorf("call 0 roles = %v, want [system user]", calls[0].Roles)
	}
	if calls[1].Method != "Stream" || calls[1].NumInput != 1 {
		t.Errorf("call 1 = %v, want Stream with 1 input", calls[1])
	}
	t.Logf("observed: %v", calls)
}

// TestObservingModelConcurrentSelfCheck drives the instrument from many
// goroutines. TurnLoop's Push/preempt path can invoke the model across
// goroutines, so the recorder must be safe there — and the instrument must not
// itself be the source of a race. Run with -race for this to mean anything.
func TestObservingModelConcurrentSelfCheck(t *testing.T) {
	m := newObservingModel(schema.AssistantMessage("ok", nil))

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			in := []*schema.Message{schema.UserMessage("m")}
			if i%2 == 0 {
				got, err := m.Generate(context.Background(), in)
				if err != nil {
					t.Errorf("Generate: %v", err)
					return
				}
				// Mutate the returned message: if replies were shared, this
				// would corrupt other callers and trip the race detector.
				got.Content = "mutated"
				return
			}
			sr, err := m.Stream(context.Background(), in)
			if err != nil {
				t.Errorf("Stream: %v", err)
				return
			}
			defer sr.Close()
			if _, err := sr.Recv(); err != nil {
				t.Errorf("Recv: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if got := len(m.observed()); got != n {
		t.Fatalf("recorded %d calls, want %d", got, n)
	}
}
