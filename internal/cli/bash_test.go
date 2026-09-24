package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/cli"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

func TestInteractiveBangCommandsStreamAndProjectBashResults(t *testing.T) {
	root := t.TempDir()
	conversation, err := cli.OpenConversation(cli.Args{NoSession: true}, root, cli.DefaultSystemPrompt)
	if err != nil {
		t.Fatalf("opening conversation: %v", err)
	}
	defer conversation.Close()
	registry, err := tools.NewBuiltInRegistry(root)
	if err != nil {
		t.Fatalf("building tools: %v", err)
	}
	model := &ai.Scripted{Name: "scripted", Final: ai.AssistantText("answer")}
	var out, errOut bytes.Buffer
	cli.RunInteractive(context.Background(), cli.Runtime{
		Model: model, ModelName: "scripted", Provider: "scripted", Tools: registry,
		Conversation: conversation, System: cli.DefaultSystemPrompt,
	}, cli.Streams{In: strings.NewReader("!printf visible\\n\n!!printf hidden\\n\nquestion\n"), Out: &out, Err: &errOut})
	if !strings.Contains(out.String(), "visible") || !strings.Contains(out.String(), "hidden") {
		t.Fatalf("bang output was not streamed: stdout=%s stderr=%s", out.String(), errOut.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("successful bang commands produced an error: %s", errOut.String())
	}
	requests := model.Requests()
	if len(requests) != 1 {
		t.Fatalf("question made %d model requests, want 1", len(requests))
	}
	var sawVisible, sawHidden bool
	for _, message := range requests[0].Messages {
		sawVisible = sawVisible || strings.Contains(message.Content, "visible")
		sawHidden = sawHidden || strings.Contains(message.Content, "hidden")
	}
	if !sawVisible || sawHidden {
		t.Fatalf("bash projection included wrong messages: %+v", requests[0].Messages)
	}
	truth := conversation.Session.Truth()
	if len(truth) != 4 || truth[0].BashExecution == nil || truth[1].BashExecution == nil || !truth[1].BashExecution.ExcludeFromContext {
		t.Fatalf("bang results were not durably represented in truth: %+v", truth)
	}
}

func TestInteractiveBangReportsEmptyAndNonzeroResults(t *testing.T) {
	root := t.TempDir()
	conversation, err := cli.OpenConversation(cli.Args{NoSession: true}, root, cli.DefaultSystemPrompt)
	if err != nil {
		t.Fatalf("opening conversation: %v", err)
	}
	defer conversation.Close()
	registry, err := tools.NewBuiltInRegistry(root)
	if err != nil {
		t.Fatalf("building tools: %v", err)
	}
	var out, errOut bytes.Buffer
	cli.RunInteractive(context.Background(), cli.Runtime{
		Model: &ai.Scripted{Name: "scripted", Final: ai.AssistantText("unused")}, ModelName: "scripted",
		Provider: "scripted", Tools: registry, Conversation: conversation,
	}, cli.Streams{In: strings.NewReader("!true\n!printf x; exit 4\n"), Out: &out, Err: &errOut})
	if !strings.Contains(out.String(), "(no output)") || !strings.Contains(errOut.String(), "command exited with code 4") {
		t.Fatalf("empty/nonzero bang results were not reported: stdout=%s stderr=%s", out.String(), errOut.String())
	}
}

func runBashRPC(t *testing.T, model ai.Port, commands ...string) (string, string) {
	t.Helper()
	root := t.TempDir()
	conversation, err := cli.OpenConversation(cli.Args{NoSession: true}, root, cli.DefaultSystemPrompt)
	if err != nil {
		t.Fatalf("opening conversation: %v", err)
	}
	defer conversation.Close()
	registry, err := tools.NewBuiltInRegistry(root)
	if err != nil {
		t.Fatalf("building tools: %v", err)
	}
	var out, errOut bytes.Buffer
	code := cli.RunRPC(context.Background(), cli.Runtime{
		Model: model, ModelName: "scripted", Provider: "scripted", Tools: registry,
		Conversation: conversation, WorkingDir: root,
	}, cli.Streams{In: strings.NewReader(strings.Join(commands, "\n") + "\n"), Out: &out, Err: &errOut})
	if code != 0 {
		t.Fatalf("RPC run failed with code %d: stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
	return out.String(), errOut.String()
}

func TestRPCBashStreamsFinalResultAndSupportsMalformedArgs(t *testing.T) {
	out, errOut := runBashRPC(t, &ai.Scripted{Name: "scripted", Final: ai.AssistantText("unused")},
		`{"id":"b1","command":"bash","bash_command":"printf hello\\n"}`,
		`{"id":"b2","command":"bash"}`,
		`{"id":"b3","command":"bash","bash_command":"true","timeout":"bad"}`)
	if errOut != "" || !strings.Contains(out, `"type":"bash_execution_update"`) || !strings.Contains(out, "hello") {
		t.Fatalf("RPC bash did not stream or report output: stdout=%s stderr=%s", out, errOut)
	}
	if !strings.Contains(out, `"command":"bash","ok":true`) || !strings.Contains(out, "needs a command") || !strings.Contains(out, "not JSON") {
		t.Fatalf("RPC malformed bash arguments were not diagnosed: %s", out)
	}
}

func TestRPCBashRejectsInvalidTimeoutAsBadArgument(t *testing.T) {
	out, errOut := runBashRPC(t, &ai.Scripted{Name: "scripted", Final: ai.AssistantText("unused")},
		`{"id":"negative","command":"bash","bash_command":"true","timeout":-1}`,
		`{"id":"oversized","command":"bash","bash_command":"true","timeout":999999999}`)
	if errOut != "" || strings.Count(out, `"kind":"bad_argument"`) != 2 {
		t.Fatalf("invalid bash timeouts were not bad arguments: stdout=%s stderr=%s", out, errOut)
	}
}

func TestRPCBashAbortCancelsMultipleCommands(t *testing.T) {
	out, errOut := runBashRPC(t, &ai.Scripted{Name: "scripted", Final: ai.AssistantText("unused")},
		`{"id":"b1","command":"bash","bash_command":"printf before; sleep 30"}`,
		`{"id":"b2","command":"bash","bash_command":"sleep 30"}`,
		`{"id":"a","command":"abort_bash"}`)
	if errOut != "" || strings.Count(out, `"cancelled":true`) != 2 {
		t.Fatalf("RPC abort did not cancel both commands: stdout=%s stderr=%s", out, errOut)
	}
}

func TestRPCBashCanRunWhilePromptIsActive(t *testing.T) {
	out, errOut := runBashRPC(t, heldModel{},
		`{"id":"p","command":"prompt","message":"hold"}`,
		`{"id":"b","command":"bash","bash_command":"printf independent"}`,
		`{"id":"a","command":"abort"}`)
	if errOut != "" || !strings.Contains(out, "independent") || !strings.Contains(out, `"command":"bash","ok":true`) || !strings.Contains(out, `"reason":"aborted"`) {
		t.Fatalf("RPC bash/prompt interaction was wrong: stdout=%s stderr=%s", out, errOut)
	}
}
