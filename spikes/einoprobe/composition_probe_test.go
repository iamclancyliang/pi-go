package einoprobe

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// labeledModel is a terminal model that reports WHICH instance served the call.
//
// The composition question cannot be answered by option values alone: "no
// temperature observed" looks identical whether the injecting wrapper was
// discarded or simply never ran. Recording the serving instance separates those.
type labeledModel struct {
	tr   *trace
	name string

	mu      sync.Mutex
	callIdx int
}

func (m *labeledModel) observe(input []*schema.Message, opts []model.Option) *schema.Message {
	m.mu.Lock()
	m.callIdx++
	idx := m.callIdx
	m.mu.Unlock()

	common := model.GetCommonOptions(&model.Options{}, opts...)
	temp := "nil"
	if common != nil && common.Temperature != nil {
		temp = fmt.Sprintf("%.1f", *common.Temperature)
	}
	m.tr.add(layerModel, "model:Generate",
		fmt.Sprintf("servingModel=%s call#%d inputs=%d observedTemperature=%s", m.name, idx, len(input), temp))
	return schema.AssistantMessage("final", nil)
}

func (m *labeledModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.observe(in, opts), nil
}

func (m *labeledModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg := m.observe(in, opts)
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() { defer sw.Close(); sw.Send(msg, nil) }()
	return sr, nil
}

var _ model.BaseChatModel = (*labeledModel)(nil)

// injectingWrapper records that it was actually TRAVERSED at call time, not
// merely constructed at wrap time. A handler can be invoked and its result then
// thrown away by a later handler — only a call-time record distinguishes those.
type injectingWrapper struct {
	tr    *trace
	inner model.BaseModel[*schema.Message]
	temp  float32
}

func (w *injectingWrapper) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	w.tr.add(layerControl, "chain:injectorTraversed", fmt.Sprintf("appending temperature=%.1f", w.temp))
	return w.inner.Generate(ctx, in, append(opts, model.WithTemperature(w.temp))...)
}

func (w *injectingWrapper) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	w.tr.add(layerControl, "chain:injectorTraversed", fmt.Sprintf("appending temperature=%.1f", w.temp))
	return w.inner.Stream(ctx, in, append(opts, model.WithTemperature(w.temp))...)
}

// injectingHandler PRESERVES the model it is given and adds an option.
type injectingHandler struct {
	adk.BaseChatModelAgentMiddleware
	tr   *trace
	temp float32
}

func (h *injectingHandler) WrapModel(ctx context.Context, m model.BaseModel[*schema.Message], mc *adk.TypedModelContext[*schema.Message]) (model.BaseModel[*schema.Message], error) {
	h.tr.add(layerControl, "WrapModel:injector", fmt.Sprintf("received=%T", m))
	return &injectingWrapper{tr: h.tr, inner: m, temp: h.temp}, nil
}

// substitutingHandler DISCARDS the model it is given and returns a fresh one.
// This is exactly the risky pattern: per-turn model selection replaces the
// model outright.
type substitutingHandler struct {
	adk.BaseChatModelAgentMiddleware
	tr      *trace
	replace *labeledModel
}

func (h *substitutingHandler) WrapModel(ctx context.Context, m model.BaseModel[*schema.Message], mc *adk.TypedModelContext[*schema.Message]) (model.BaseModel[*schema.Message], error) {
	h.tr.add(layerControl, "WrapModel:substituter", fmt.Sprintf("received=%T discarded=true returning=%s", m, h.replace.name))
	return h.replace, nil
}

// runCompositionProbe registers the two handlers in the given order and runs one
// turn. Returns the trace.
func runCompositionProbe(t *testing.T, substituterFirst bool) *trace {
	t.Helper()
	tr := newTrace()

	modelA := &labeledModel{tr: tr, name: "modelA-original"}
	modelB := &labeledModel{tr: tr, name: "modelB-substitute"}

	inj := &injectingHandler{tr: tr, temp: 7.0}
	sub := &substitutingHandler{tr: tr, replace: modelB}

	handlers := []adk.TypedChatModelAgentMiddleware[*schema.Message]{inj, sub}
	if substituterFirst {
		handlers = []adk.TypedChatModelAgentMiddleware[*schema.Message]{sub, inj}
	}

	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "composition", Description: "multi-handler composition probe", Instruction: "probe",
		Model:    modelA,
		Handlers: handlers,
	})
	if err != nil {
		t.Fatalf("NewChatModelAgent: %v", err)
	}

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[*schema.Message, *schema.Message]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[*schema.Message, *schema.Message], items []*schema.Message) (*adk.GenInputResult[*schema.Message, *schema.Message], error) {
			tr.beginGenInput(fmt.Sprintf("buffered=%d", len(items)))
			if len(items) == 0 {
				return nil, nil
			}
			return &adk.GenInputResult[*schema.Message, *schema.Message]{
				Input:    &adk.TypedAgentInput[*schema.Message]{Messages: items},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[*schema.Message, *schema.Message], consumed []*schema.Message) (adk.TypedAgent[*schema.Message], error) {
			tr.beginPrepareAgent(fmt.Sprintf("consumed=%d", len(consumed)))
			return agent, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *adk.TurnContext[*schema.Message, *schema.Message], events *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.Message]]) error {
			for {
				if _, ok := events.Next(); !ok {
					return nil
				}
			}
		},
	})

	loop.Push(schema.UserMessage("hello"))
	loop.Run(context.Background())
	loop.Stop(adk.UntilIdleFor(150 * time.Millisecond))
	loop.Wait()
	return tr
}

// TestWrapModelCompositionOrder answers: when two WrapModel handlers are
// registered and one SUBSTITUTES the model outright, does it silently bypass
// the other?
//
// Pre-registered interpretations (INTERPRETATION.md, M1-M4). The assertions
// below encode M-outer/M-inner as observed, and will fail if eino's composition
// order changes — which is the point: this is the regression gate for the
// ordering risk described above.
func TestWrapModelCompositionOrder(t *testing.T) {
	for _, tc := range []struct {
		name             string
		substituterFirst bool
	}{
		{"injectorRegisteredFirst", false},
		{"substituterRegisteredFirst", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := runCompositionProbe(t, tc.substituterFirst)
			t.Logf("\n=== COMPOSITION RAW TIMELINE (%s) ===\n%s", tc.name, tr.render())

			// The substituter is registered in both arms, so it always runs.
			if n := tr.countEvents("WrapModel:substituter"); n != 1 {
				t.Fatalf("substituter WrapModel fired %d times, want 1", n)
			}

			calls := tr.detailsMatching("model:Generate")
			if len(calls) != 1 {
				t.Fatalf("model calls = %d, want 1: %v", len(calls), calls)
			}

			// Substitution must take effect: the replacement serves the call.
			// (M3 would mean substitution silently did nothing.)
			if !strings.Contains(calls[0], "servingModel=modelB-substitute") {
				t.Fatalf("M3: substitution did not take effect, call served by %q", calls[0])
			}

			traversed := tr.countEvents("chain:injectorTraversed")
			injWrapped := tr.countEvents("WrapModel:injector")
			subWrapSeq := tr.firstSeq("WrapModel:substituter", "")

			if tc.substituterFirst {
				// M1 - bypass, in its STRONGEST form. The pre-registered M1 row
				// anticipated "constructed then discarded". What actually happens
				// is more severe: the inner handler's WrapModel is never invoked
				// at all, because the outer handler never calls through to it.
				if injWrapped != 0 {
					t.Errorf("M1: injector WrapModel fired %d times, want 0 — "+
						"an outer substituter must never reach the inner handler", injWrapped)
				}
				if traversed != 0 {
					t.Errorf("M1: injector traversed %d times, want 0", traversed)
				}
				if !strings.Contains(calls[0], "observedTemperature=nil") {
					t.Errorf("M1 incoherent: injector never ran but its option arrived: %s", calls[0])
				}
				return
			}

			// M2 - the injector is registered first, so it is OUTERMOST and its
			// injection survives the substitution below it.
			if injWrapped != 1 {
				t.Fatalf("M2: injector WrapModel fired %d times, want 1", injWrapped)
			}
			if traversed != 1 {
				t.Fatalf("M4: injector traversed %d times for a single model call — unregistered outcome", traversed)
			}
			if !strings.Contains(calls[0], "observedTemperature=7.0") {
				t.Errorf("M2 incoherent: injector traversed but its option did not arrive: %s", calls[0])
			}

			// Wrapping is LAZY, and this is what makes the bypass possible: the
			// injector's Generate runs BEFORE the substituter is even asked to
			// wrap. Handlers are not all composed up front — each is reached only
			// if the one outside it calls through. Assert the ordering directly,
			// since it is the mechanism the M1 arm depends on.
			travSeq := tr.firstSeq("chain:injectorTraversed", "")
			if travSeq < 0 || subWrapSeq < 0 {
				t.Fatalf("missing sequence markers: traversed=%d substituterWrap=%d", travSeq, subWrapSeq)
			}
			if travSeq > subWrapSeq {
				t.Errorf("expected LAZY composition (outer Generate at seq %d before inner WrapModel at seq %d); "+
					"eager composition would reverse these", travSeq, subWrapSeq)
			}
		})
	}
}
