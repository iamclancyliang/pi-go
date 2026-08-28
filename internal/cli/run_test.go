package cli_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/cli"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

func scripted(final string, replies ...ai.Response) *ai.Scripted {
	return &ai.Scripted{
		Name:                 "scripted-1",
		Replies:              replies,
		StopWhenToolsSettled: true,
		Final:                ai.AssistantText(final),
	}
}

func runtimeFor(t *testing.T, model ai.Port) cli.Runtime {
	t.Helper()
	registry, err := tools.NewBuiltInRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("building tools: %v", err)
	}
	conversation, err := cli.OpenConversation(
		cli.Args{NoSession: true}, t.TempDir(), cli.DefaultSystemPrompt)
	if err != nil {
		t.Fatalf("opening a conversation: %v", err)
	}
	t.Cleanup(func() { conversation.Close() })

	return cli.Runtime{
		Model:        model,
		ModelName:    "scripted-1",
		Tools:        registry,
		System:       cli.DefaultSystemPrompt,
		Provider:     "scripted",
		Conversation: conversation,
	}
}

// TestPrintWritesTheAnswerToStdoutAndNothingElse. A caller piping stdout into
// another program must receive the answer alone: a banner, a prompt or a
// progress line there corrupts whatever reads it.
func TestPrintWritesTheAnswerToStdoutAndNothingElse(t *testing.T) {
	var out, errOut bytes.Buffer
	code := cli.RunPrint(context.Background(),
		runtimeFor(t, scripted("the answer")),
		cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errOut},
		[]string{"a question"})

	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut.String())
	}
	if out.String() != "the answer\n" {
		t.Fatalf("stdout is %q, and it must hold the answer alone", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("a successful run wrote to stderr: %q", errOut.String())
	}
}

// TestPrintReportsAFailureOnStderrAndInTheExitCode. A pipeline reading stdout
// must not mistake an apology for an answer, and a script must be able to tell
// without parsing text.
func TestPrintReportsAFailureOnStderrAndInTheExitCode(t *testing.T) {
	var out, errOut bytes.Buffer
	code := cli.RunPrint(context.Background(),
		runtimeFor(t, &alwaysFails{because: "the provider refused"}),
		cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errOut},
		[]string{"a question"})

	if code == 0 {
		t.Fatal("a failed run exited zero")
	}
	if out.Len() != 0 {
		t.Fatalf("a failed run wrote %q to stdout", out.String())
	}
	if !strings.Contains(errOut.String(), "refused") {
		t.Fatalf("the reason did not reach stderr: %q", errOut.String())
	}
}

// TestPrintSendsEveryPromptInOrder, because more than one may be given and each
// builds on the last.
func TestPrintSendsEveryPromptInOrder(t *testing.T) {
	model := scripted("done")
	var out, errOut bytes.Buffer
	code := cli.RunPrint(context.Background(), runtimeFor(t, model),
		cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errOut},
		[]string{"first", "second"})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}

	// The second request must carry the first exchange: two prompts are one
	// conversation, not two unrelated questions.
	sent := model.Requests()
	if len(sent) < 2 {
		t.Fatalf("two prompts produced %d requests", len(sent))
	}
	last := sent[len(sent)-1]
	var joined strings.Builder
	for _, m := range last.Messages {
		joined.WriteString(m.Content)
		joined.WriteString("\n")
	}
	if !strings.Contains(joined.String(), "first") || !strings.Contains(joined.String(), "second") {
		t.Fatalf("the last request did not carry both prompts:\n%s", joined.String())
	}
}

// TestPrintWithNoPromptFails rather than starting a session it was not asked
// for. Reaching a provider on an empty command line is a billed request nobody
// requested.
func TestPrintWithNoPromptFails(t *testing.T) {
	var out, errOut bytes.Buffer
	code := cli.RunPrint(context.Background(), runtimeFor(t, scripted("x")),
		cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errOut}, nil)
	if code == 0 {
		t.Fatal("a run with no prompt succeeded")
	}
	if out.Len() != 0 {
		t.Fatalf("it wrote %q to stdout", out.String())
	}
}

// TestInteractiveAnswersEachLineAndEndsAtEOF.
func TestInteractiveAnswersEachLineAndEndsAtEOF(t *testing.T) {
	var out, errOut bytes.Buffer
	code := cli.RunInteractive(context.Background(),
		runtimeFor(t, scripted("an answer")),
		cli.Streams{In: strings.NewReader("first\nsecond\n"), Out: &out, Err: &errOut})

	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut.String())
	}
	if n := strings.Count(out.String(), "an answer"); n != 2 {
		t.Fatalf("two prompts produced %d answers:\n%s", n, out.String())
	}
	if !strings.Contains(out.String(), "scripted/scripted-1") {
		t.Fatalf("the banner does not say what is being spent:\n%s", out.String())
	}
}

// TestInteractiveIgnoresBlankLines. Sending an empty prompt is a billed request
// for nothing, and pressing return is how a person thinks.
func TestInteractiveIgnoresBlankLines(t *testing.T) {
	model := scripted("an answer")
	var out, errOut bytes.Buffer
	cli.RunInteractive(context.Background(), runtimeFor(t, model),
		cli.Streams{In: strings.NewReader("\n   \n\nreal question\n"), Out: &out, Err: &errOut})

	if n := len(model.Requests()); n != 1 {
		t.Fatalf("blank lines produced %d requests, want 1", n)
	}
}

// TestInteractiveStopsOnExit without waiting for the stream to close.
func TestInteractiveStopsOnExit(t *testing.T) {
	model := scripted("an answer")
	var out, errOut bytes.Buffer
	cli.RunInteractive(context.Background(), runtimeFor(t, model),
		cli.Streams{In: strings.NewReader("/exit\nnever asked\n"), Out: &out, Err: &errOut})

	if n := len(model.Requests()); n != 0 {
		t.Fatalf("input after /exit was still sent: %d requests", n)
	}
}

// TestInteractiveKeepsGoingAfterAFailedTurn. One provider error must not end a
// session: the next question may well succeed, and losing the conversation is a
// worse outcome than one unanswered turn.
func TestInteractiveKeepsGoingAfterAFailedTurn(t *testing.T) {
	var out, errOut bytes.Buffer
	code := cli.RunInteractive(context.Background(),
		runtimeFor(t, &alwaysFails{because: "transient trouble"}),
		cli.Streams{In: strings.NewReader("one\ntwo\n"), Out: &out, Err: &errOut})

	if code != 0 {
		t.Fatalf("a failed turn ended the session with %d", code)
	}
	if n := strings.Count(errOut.String(), "transient trouble"); n != 2 {
		t.Fatalf("both turns should have reported: %q", errOut.String())
	}
}

// TestInteractiveStopsWhenTheRunIsCancelled, and says so rather than looping on
// a context that will fail every time.
func TestInteractiveStopsWhenTheRunIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out, errOut bytes.Buffer
	code := cli.RunInteractive(ctx, runtimeFor(t, scripted("never")),
		cli.Streams{In: strings.NewReader("a question\nanother\n"), Out: &out, Err: &errOut})

	if code != 130 {
		t.Fatalf("a cancelled session exited %d, want 130", code)
	}
	if !strings.Contains(errOut.String(), "stopped") {
		t.Fatalf("it did not say it was stopped: %q", errOut.String())
	}
}

// alwaysFails is a model port that refuses every request, so the modes can be
// driven through their failure paths without a network.
type alwaysFails struct{ because string }

func (a *alwaysFails) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, errors.New(a.because)
}
