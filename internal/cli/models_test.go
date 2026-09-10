package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

type modelListingPort struct {
	models []ai.ModelRef
	err    error
}

type cancellationListingPort struct{}

// rpcModelPort is used behind RunRPC so the test exercises the real rpcHost
// model switch and the runtime agent that receives the following prompt.
type rpcModelPort struct {
	*ai.Scripted
	models []ai.ModelRef
	calls  int
}

func (p *rpcModelPort) ListModels(context.Context) ([]ai.ModelRef, error) {
	p.calls++
	return append([]ai.ModelRef(nil), p.models...), nil
}

func (cancellationListingPort) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, nil
}

func (cancellationListingPort) ListModels(ctx context.Context) ([]ai.ModelRef, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *modelListingPort) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, nil
}

func (p *modelListingPort) ListModels(context.Context) ([]ai.ModelRef, error) {
	return append([]ai.ModelRef(nil), p.models...), p.err
}

func TestModelListingIsSortedDeduplicatedAndProviderScoped(t *testing.T) {
	port := &modelListingPort{models: []ai.ModelRef{
		{ID: "zeta"},
		{Provider: "ollama", ID: "alpha"},
		{Provider: "ollama", ID: "alpha"},
		{Provider: "", ID: ""},
	}}
	got, err := listAvailableModels(context.Background(), port, "ollama")
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	want := []ai.ModelRef{{Provider: "ollama", ID: "alpha"}, {Provider: "ollama", ID: "zeta"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("listing was %+v, want %+v", got, want)
	}
}

func TestCycleModelUsesForwardWrapAndAbsentCurrentRules(t *testing.T) {
	port := &modelListingPort{models: []ai.ModelRef{
		{Provider: "ollama", ID: "alpha"},
		{Provider: "ollama", ID: "beta"},
		{Provider: "ollama", ID: "gamma"},
	}}
	var switched []ai.ModelRef
	switchModel := func(provider, model string) error {
		switched = append(switched, ai.ModelRef{Provider: provider, ID: model})
		return nil
	}
	result, err := cycleModels(context.Background(), port, "ollama", "alpha", nil, "", switchModel)
	if err != nil || result == nil || result.Model.ID != "beta" {
		t.Fatalf("alpha did not cycle to beta: result=%+v err=%v", result, err)
	}
	result, err = cycleModels(context.Background(), port, "ollama", "gamma", nil, "", switchModel)
	if err != nil || result == nil || result.Model.ID != "alpha" {
		t.Fatalf("gamma did not wrap to alpha: result=%+v err=%v", result, err)
	}
	result, err = cycleModels(context.Background(), port, "ollama", "missing", nil, "", switchModel)
	if err != nil || result == nil || result.Model.ID != "beta" {
		t.Fatalf("an absent current model did not start at index zero then advance: result=%+v err=%v", result, err)
	}
	if len(switched) != 3 {
		t.Fatalf("switch called %d times, want 3: %v", len(switched), switched)
	}
}

func TestCycleModelIntersectsScopedOrderWithAvailableListing(t *testing.T) {
	port := &modelListingPort{models: []ai.ModelRef{
		{Provider: "ollama", ID: "alpha"},
		{Provider: "ollama", ID: "beta"},
		{Provider: "ollama", ID: "gamma"},
	}}
	var switched ai.ModelRef
	result, err := cycleModels(context.Background(), port, "ollama", "alpha", []ai.ModelRef{
		{Provider: "ollama", ID: "gamma"},
		{Provider: "ollama", ID: "alpha"},
		{Provider: "ollama", ID: "does-not-exist"},
	}, "high", func(provider, model string) error {
		switched = ai.ModelRef{Provider: provider, ID: model}
		return nil
	})
	if err != nil || result == nil || result.Model != (ai.ModelRef{Provider: "ollama", ID: "gamma"}) {
		t.Fatalf("scoped cycle chose %+v with err %v", result, err)
	}
	if !result.IsScoped || result.ThinkingLevel != ai.ThinkingHigh || switched.ID != "gamma" {
		t.Fatalf("scoped result lost scope/thinking or switch: %+v, switched=%+v", result, switched)
	}
}

func TestCycleModelResolvesParsedPatternsAgainstTheFreshListing(t *testing.T) {
	port := &modelListingPort{models: []ai.ModelRef{
		{Provider: "ollama", ID: "alpha"},
		{Provider: "ollama", ID: "beta:7b"},
		{Provider: "ollama", ID: "gamma"},
	}}
	result, err := cycleModelsWithPatterns(context.Background(), port, "ollama", "alpha", nil,
		[]string{"OLLAMA/*BETA*", "ollama/alpha", "ollama/alpha"}, "", func(provider, model string) error {
			if provider != "ollama" || model != "beta:7b" {
				t.Fatalf("pattern cycle switched to %s/%s", provider, model)
			}
			return nil
		})
	if err != nil || result == nil || result.Model.ID != "beta:7b" || !result.IsScoped {
		t.Fatalf("parsed patterns were not resolved in order: result=%+v err=%v", result, err)
	}
}

func TestAnUnmatchedParsedScopeReturnsADiagnosticWithoutSwitching(t *testing.T) {
	port := &modelListingPort{models: []ai.ModelRef{
		{Provider: "ollama", ID: "alpha"},
		{Provider: "ollama", ID: "beta"},
	}}
	switched := false
	result, err := cycleModelsWithPatterns(context.Background(), port, "ollama", "alpha", nil,
		[]string{"does-not-match"}, "", func(string, string) error {
			switched = true
			return nil
		})
	var patternErr *ModelPatternError
	if result != nil || !errors.As(err, &patternErr) || len(patternErr.Diagnostics) != 1 ||
		!strings.Contains(patternErr.Diagnostics[0], "does-not-match") || switched {
		t.Fatalf("unmatched pattern was not rejected transactionally: result=%+v err=%v switched=%v", result, err, switched)
	}
}

func TestUnsupportedPatternReturnsADiagnosticWithoutSelectingAnUnrelatedModel(t *testing.T) {
	port := &modelListingPort{models: []ai.ModelRef{
		{Provider: "ollama", ID: "alpha"},
		{Provider: "ollama", ID: "beta"},
	}}
	switched := false
	result, err := cycleModelsWithPatterns(context.Background(), port, "ollama", "alpha", nil,
		[]string{"alpha:high"}, "", func(string, string) error {
			switched = true
			return nil
		})
	var patternErr *ModelPatternError
	if result != nil || !errors.As(err, &patternErr) || len(patternErr.Diagnostics) != 1 ||
		!strings.Contains(patternErr.Diagnostics[0], "per-pattern thinking levels are not supported") || switched {
		t.Fatalf("unsupported suffix was not rejected transactionally: result=%+v err=%v switched=%v", result, err, switched)
	}
}

func TestInvalidGlobReturnsADiagnostic(t *testing.T) {
	port := &modelListingPort{models: []ai.ModelRef{{Provider: "ollama", ID: "alpha"}}}
	result, err := cycleModelsWithPatterns(context.Background(), port, "ollama", "alpha", nil,
		[]string{"ollama/["}, "", func(string, string) error { return nil })
	var patternErr *ModelPatternError
	if result != nil || !errors.As(err, &patternErr) || len(patternErr.Diagnostics) != 1 ||
		!strings.Contains(patternErr.Diagnostics[0], "unsupported glob syntax") {
		t.Fatalf("invalid glob was not diagnosed: result=%+v err=%v", result, err)
	}
}

func runModelRPC(t *testing.T, rt Runtime, commands ...string) (string, string) {
	t.Helper()
	var out, errOut strings.Builder
	if code := RunRPC(context.Background(), rt, Streams{
		In: strings.NewReader(strings.Join(commands, "\n") + "\n"), Out: &out, Err: &errOut,
	}); code != 0 {
		t.Fatalf("RunRPC returned %d: stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
	return out.String(), errOut.String()
}

func newRPCModelRuntime(t *testing.T, initial ai.Port, opener func(Args) (ai.Port, string, string, error)) Runtime {
	t.Helper()
	conversation, err := OpenConversation(Args{NoSession: true}, t.TempDir(), DefaultSystemPrompt)
	if err != nil {
		t.Fatalf("opening RPC conversation: %v", err)
	}
	t.Cleanup(func() { _ = conversation.Close() })
	registry, err := tools.NewBuiltInRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("building RPC tools: %v", err)
	}
	args := Args{Provider: "test", Model: "alpha"}
	return Runtime{
		Model: initial, ModelName: "alpha", Provider: "test", Tools: registry,
		System: DefaultSystemPrompt, Conversation: conversation, Args: args,
		ModelPatterns: []string{"test/alpha", "test/beta"},
		OpenModel: func(args Args, _ http.RoundTripper) (ai.Port, string, string, error) {
			return opener(args)
		},
	}
}

func TestRPCModelCycleUsesTheSelectedModelForTheNextPrompt(t *testing.T) {
	models := []ai.ModelRef{{Provider: "test", ID: "alpha"}, {Provider: "test", ID: "beta"}}
	alpha := &rpcModelPort{Scripted: &ai.Scripted{Name: "alpha", Final: ai.AssistantText("alpha answer")}, models: models}
	beta := &rpcModelPort{Scripted: &ai.Scripted{Name: "beta", Final: ai.AssistantText("beta answer")}, models: models}
	rt := newRPCModelRuntime(t, alpha, func(args Args) (ai.Port, string, string, error) {
		if args.Model == "beta" {
			return beta, "test", "beta", nil
		}
		return alpha, "test", "alpha", nil
	})
	out, errOut := runModelRPC(t, rt,
		`{"id":"1","command":"cycle_model"}`,
		`{"id":"2","command":"prompt","message":"question"}`)
	if errOut != "" || !strings.Contains(out, `"id":"beta"`) || !strings.Contains(out, "beta answer") {
		t.Fatalf("RPC cycle did not select beta: stdout=%s stderr=%s", out, errOut)
	}
	requests := beta.Requests()
	if len(requests) != 1 || requests[0].Model != "beta" {
		t.Fatalf("the next prompt did not use the selected beta port: %+v", requests)
	}
}

func TestRPCFailedModelCycleKeepsTheOldModelForTheNextPrompt(t *testing.T) {
	models := []ai.ModelRef{{Provider: "test", ID: "alpha"}, {Provider: "test", ID: "beta"}}
	alpha := &rpcModelPort{Scripted: &ai.Scripted{Name: "alpha", Final: ai.AssistantText("alpha answer")}, models: models}
	beta := &rpcModelPort{Scripted: &ai.Scripted{Name: "beta", Final: ai.AssistantText("beta answer")}, models: models}
	rt := newRPCModelRuntime(t, alpha, func(args Args) (ai.Port, string, string, error) {
		if args.Model == "beta" {
			return nil, "", "", errors.New("beta unavailable")
		}
		return alpha, "test", "alpha", nil
	})
	out, errOut := runModelRPC(t, rt,
		`{"id":"1","command":"cycle_model"}`,
		`{"id":"2","command":"prompt","message":"question"}`)
	if errOut != "" || !strings.Contains(out, "beta unavailable") || !strings.Contains(out, "alpha answer") {
		t.Fatalf("failed RPC cycle did not preserve alpha: stdout=%s stderr=%s", out, errOut)
	}
	if got := beta.Requests(); len(got) != 0 {
		t.Fatalf("the failed model received a prompt: %+v", got)
	}
	requests := alpha.Requests()
	if len(requests) != 1 || requests[0].Model != "alpha" {
		t.Fatalf("the next prompt did not retain alpha: %+v", requests)
	}
}

func TestCycleModelDeduplicatesTheScopedSnapshotBeforeCycling(t *testing.T) {
	port := &modelListingPort{models: []ai.ModelRef{
		{Provider: "ollama", ID: "alpha"},
		{Provider: "ollama", ID: "beta"},
	}}
	var switched ai.ModelRef
	result, err := cycleModels(context.Background(), port, "ollama", "alpha", []ai.ModelRef{
		{Provider: "ollama", ID: "beta"},
		{Provider: "ollama", ID: "beta"},
		{Provider: "ollama", ID: "alpha"},
	}, "", func(provider, model string) error {
		switched = ai.ModelRef{Provider: provider, ID: model}
		return nil
	})
	if err != nil || result == nil || result.Model.ID != "beta" || switched.ID != "beta" {
		t.Fatalf("duplicate scoped entries changed the cycle: result=%+v switched=%+v err=%v", result, switched, err)
	}
}

func TestCycleModelReturnsNilForOneCandidateWithoutSwitching(t *testing.T) {
	port := &modelListingPort{models: []ai.ModelRef{{Provider: "ollama", ID: "only"}}}
	switched := false
	result, err := cycleModels(context.Background(), port, "ollama", "only", nil, "", func(string, string) error {
		switched = true
		return nil
	})
	if err != nil || result != nil || switched {
		t.Fatalf("one candidate cycled as result=%+v err=%v switched=%v", result, err, switched)
	}
}

func TestModelListingUnsupportedAndNetworkFailureStayDistinct(t *testing.T) {
	if _, err := listAvailableModels(context.Background(), &ai.Scripted{}, "scripted"); !errors.Is(err, ai.ErrModelListingUnsupported) {
		t.Fatalf("unsupported listing was %v", err)
	}
	wrappedUnsupported := &modelListingPort{err: fmt.Errorf("listing endpoint: %w", ai.ErrModelListingUnsupported)}
	if _, err := listAvailableModels(context.Background(), wrappedUnsupported, "ollama"); !errors.Is(err, ai.ErrModelListingUnsupported) {
		t.Fatalf("wrapped unsupported listing was %v", err)
	}
	_, err := listAvailableModels(context.Background(), &modelListingPort{err: errors.New("server unavailable")}, "ollama")
	var providerErr *ai.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Provider != "ollama" || providerErr.Failure != ai.FailureTransient {
		t.Fatalf("network listing was not a transient provider error: %v", err)
	}
}

func TestModelListingPropagatesCancellationAndHasABound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := listAvailableModels(ctx, cancellationListingPort{}, "ollama")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a stalled listing returned %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("listing cancellation took %v", elapsed)
	}
}
