package cli_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/cli"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

type cliModelListingPort struct {
	*ai.Scripted
	models      []ai.ModelRef
	listingErr  error
	listingCall int
}

func (p *cliModelListingPort) ListModels(context.Context) ([]ai.ModelRef, error) {
	p.listingCall++
	return append([]ai.ModelRef(nil), p.models...), p.listingErr
}

func newNoSessionConversation(t *testing.T) *cli.Conversation {
	t.Helper()
	conversation, err := cli.OpenConversation(cli.Args{NoSession: true}, t.TempDir(), cli.DefaultSystemPrompt)
	if err != nil {
		t.Fatalf("opening conversation: %v", err)
	}
	t.Cleanup(func() { conversation.Close() })
	return conversation
}

func newModelTestTools(t *testing.T) *tools.Registry {
	t.Helper()
	registry, err := tools.NewBuiltInRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("building tools: %v", err)
	}
	return registry
}

func runInteractiveRuntime(t *testing.T, rt cli.Runtime, lines ...string) (string, string) {
	return runInteractiveRuntimeContext(t, context.Background(), rt, lines...)
}

func runInteractiveRuntimeContext(t *testing.T, ctx context.Context, rt cli.Runtime, lines ...string) (string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	cli.RunInteractive(ctx, rt, cli.Streams{
		In: strings.NewReader(strings.Join(lines, "\n") + "\n"), Out: &out, Err: &errOut,
	})
	return out.String(), errOut.String()
}

type cancellationCLIListingPort struct {
	*ai.Scripted
}

func (p *cancellationCLIListingPort) ListModels(ctx context.Context) ([]ai.ModelRef, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func runModelListingSession(t *testing.T, port ai.Port, lines ...string) (string, string) {
	t.Helper()
	registry, err := tools.NewBuiltInRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("building tools: %v", err)
	}
	conversation, err := cli.OpenConversation(cli.Args{NoSession: true}, t.TempDir(), cli.DefaultSystemPrompt)
	if err != nil {
		t.Fatalf("opening conversation: %v", err)
	}
	defer conversation.Close()
	var out, errOut bytes.Buffer
	cli.RunInteractive(context.Background(), cli.Runtime{
		Model:        port,
		ModelName:    "alpha",
		Tools:        registry,
		System:       cli.DefaultSystemPrompt,
		Provider:     "ollama",
		Conversation: conversation,
	}, cli.Streams{
		In: strings.NewReader(strings.Join(lines, "\n") + "\n"), Out: &out, Err: &errOut,
	})
	return out.String(), errOut.String()
}

func TestInteractiveModelCommandsUseProviderListingOnDemand(t *testing.T) {
	port := &cliModelListingPort{
		Scripted: &ai.Scripted{Name: "alpha", Final: ai.AssistantText("unused")},
		models: []ai.ModelRef{
			{Provider: "ollama", ID: "beta"},
			{Provider: "ollama", ID: "alpha"},
		},
	}
	out, errOut := runModelListingSession(t, port, "/models", "/model")
	if !strings.Contains(out, "ollama/alpha") || !strings.Contains(out, "ollama/beta") {
		t.Fatalf("provider listing did not reach the shared CLI commands:\n%s", out)
	}
	if !strings.Contains(out, "ollama/alpha") || !strings.Contains(out, "available models") {
		t.Fatalf("/model did not show the on-demand listing:\n%s", out)
	}
	if strings.Contains(errOut, "could not list models") {
		t.Fatalf("a successful listing was reported as failed: %q", errOut)
	}
	if port.listingCall != 2 {
		t.Fatalf("listing was cached across two explicit requests: %d calls", port.listingCall)
	}
}

func TestModelListingIsNotReachedAtStartupOrByUnrelatedCommands(t *testing.T) {
	port := &cliModelListingPort{
		Scripted: &ai.Scripted{Name: "alpha", Final: ai.AssistantText("answer")},
		models:   []ai.ModelRef{{Provider: "ollama", ID: "alpha"}},
	}
	runModelListingSession(t, port, "/help", "question")
	if port.listingCall != 0 {
		t.Fatalf("model listing was reached by startup or unrelated work: %d calls", port.listingCall)
	}
}

func TestParsedModelScopeCyclesAndServesTheNextPrompt(t *testing.T) {
	alpha := &cliModelListingPort{
		Scripted: &ai.Scripted{Name: "alpha", Final: ai.AssistantText("alpha answer")},
		models: []ai.ModelRef{
			{Provider: "test", ID: "alpha"}, {Provider: "test", ID: "beta"}, {Provider: "test", ID: "gamma"},
		},
	}
	beta := &cliModelListingPort{
		Scripted: &ai.Scripted{Name: "beta", Final: ai.AssistantText("beta answer")},
		models:   alpha.models,
	}
	parsed := cli.ParseArgs([]string{"--provider", "test", "--model", "alpha", "--models", "test/alpha,test/beta"})
	var opened []string
	openModel := func(args cli.Args, _ http.RoundTripper) (ai.Port, string, string, error) {
		opened = append(opened, args.Model)
		if args.Model == "beta" {
			return beta, "test", "beta", nil
		}
		return alpha, "test", "alpha", nil
	}
	out, errOut := runInteractiveRuntime(t, cli.Runtime{
		Model: alpha, ModelName: "alpha", Provider: "test", Conversation: newNoSessionConversation(t),
		Tools: newModelTestTools(t), Args: parsed, ModelPatterns: parsed.Models, OpenModel: openModel,
	}, "/cycle-model", "question")
	if !strings.Contains(out, "model test/beta") || !strings.Contains(out, "beta answer") {
		t.Fatalf("the parsed scope did not select the next model: out=%s err=%s", out, errOut)
	}
	if len(opened) != 1 || opened[0] != "beta" {
		t.Fatalf("model opener saw %v, want [beta]", opened)
	}
	requests := beta.Requests()
	if len(requests) != 1 || requests[0].Model != "beta" {
		t.Fatalf("the next prompt did not use beta: %+v", requests)
	}
}

func TestFailedModelSwitchKeepsTheOldModelForTheNextPrompt(t *testing.T) {
	alpha := &cliModelListingPort{
		Scripted: &ai.Scripted{Name: "alpha", Final: ai.AssistantText("alpha answer")},
		models: []ai.ModelRef{
			{Provider: "test", ID: "alpha"}, {Provider: "test", ID: "beta"},
		},
	}
	parsed := cli.ParseArgs([]string{"--provider", "test", "--model", "alpha", "--models", "test/alpha,test/beta"})
	openModel := func(args cli.Args, _ http.RoundTripper) (ai.Port, string, string, error) {
		if args.Model == "beta" {
			return nil, "", "", errors.New("beta unavailable")
		}
		return alpha, "test", "alpha", nil
	}
	out, errOut := runInteractiveRuntime(t, cli.Runtime{
		Model: alpha, ModelName: "alpha", Provider: "test", Conversation: newNoSessionConversation(t),
		Tools: newModelTestTools(t), Args: parsed, ModelPatterns: parsed.Models, OpenModel: openModel,
	}, "/cycle-model", "question")
	if !strings.Contains(errOut, "could not cycle model: beta unavailable") || !strings.Contains(out, "alpha answer") {
		t.Fatalf("failed switch did not preserve old model: out=%s err=%s", out, errOut)
	}
	requests := alpha.Requests()
	if len(requests) != 1 || requests[0].Model != "alpha" {
		t.Fatalf("the next prompt used a failed model switch: %+v", requests)
	}
}

func TestZeroCandidateCycleHasAnAccurateMessage(t *testing.T) {
	port := &cliModelListingPort{Scripted: &ai.Scripted{Name: "alpha", Final: ai.AssistantText("unused")}}
	out, errOut := runInteractiveRuntime(t, cli.Runtime{
		Model: port, ModelName: "alpha", Provider: "test", Conversation: newNoSessionConversation(t),
		Tools: newModelTestTools(t),
	}, "/cycle-model")
	if !strings.Contains(out, "no alternate model is available") || strings.Contains(out, "only one model") {
		t.Fatalf("zero candidates got the wrong message: out=%s err=%s", out, errOut)
	}
}

func TestInteractiveListingUsesTheRunContextWhenItIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	port := &cancellationCLIListingPort{Scripted: &ai.Scripted{Name: "alpha", Final: ai.AssistantText("unused")}}
	started := time.Now()
	_, errOut := runInteractiveRuntimeContext(t, ctx, cli.Runtime{
		Model: port, ModelName: "alpha", Provider: "test", Conversation: newNoSessionConversation(t),
		Tools: newModelTestTools(t),
	}, "/models")
	if !strings.Contains(errOut, "could not list models") {
		t.Fatalf("cancelled listing was not reported: %q", errOut)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancelled interactive listing took %v", elapsed)
	}
}
