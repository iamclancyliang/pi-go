package cli_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/cli"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// runRPC feeds commands to RunRPC and returns every stdout line parsed, with
// the version line dropped.
func runRPC(t *testing.T, model ai.Port, commands ...string) []map[string]any {
	t.Helper()
	var out, errOut bytes.Buffer
	cli.RunRPC(context.Background(),
		runtimeFor(t, model),
		cli.Streams{In: strings.NewReader(strings.Join(commands, "\n") + "\n"), Out: &out, Err: &errOut})

	var lines []map[string]any
	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		var m map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			t.Fatalf("stdout carries a non-JSON line: %q", scanner.Text())
		}
		lines = append(lines, m)
	}
	if len(lines) == 0 || lines[0]["protocol"] != "pi-go-stream" {
		t.Fatalf("the stream did not open with its version: %v", lines)
	}
	return lines[1:]
}

func responses(lines []map[string]any) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["family"] == "response" {
			out = append(out, l)
		}
	}
	return out
}

// TestACommandWithoutAnIdIsRefused: a response it could only match by position
// is the ambiguity this protocol removes, so a command that omits the id is
// answered with a malformed failure rather than served.
func TestACommandWithoutAnIdIsRefused(t *testing.T) {
	lines := runRPC(t, scripted("unused"), `{"command":"get_state"}`)
	got := responses(lines)
	if len(got) != 1 || got[0]["ok"] != false {
		t.Fatalf("a command without an id was served: %v", got)
	}
	errObj := got[0]["error"].(map[string]any)
	if errObj["kind"] != "malformed_command" {
		t.Fatalf("the id-less command was misclassified: %v", errObj)
	}
}

// TestAMalformedLineIsAnsweredNotFatal: one line of garbage gets a typed
// refusal and the loop reads the next command.
func TestAMalformedLineIsAnsweredNotFatal(t *testing.T) {
	lines := runRPC(t, scripted("unused"),
		`not json at all`,
		`{"id":"2","command":"get_state"}`)
	got := responses(lines)
	if len(got) != 2 {
		t.Fatalf("the loop stopped at the garbage line: %d responses", len(got))
	}
	if got[0]["ok"] != false || got[1]["ok"] != true {
		t.Fatalf("garbage was not survived: %v", got)
	}
	if got[1]["id"] != "2" {
		t.Fatalf("the command after the garbage lost its id: %v", got[1])
	}
}

// TestAPromptsResponseAndItsEventsShareOneOrder is the whole point of the
// channel joining the stream: every line, response or event, carries a seq, and
// the numbers are 1..N with no gap, even though the ack and the events come
// from different goroutines. The ack is a receipt, so it precedes the run's
// end; the run's end is the last thing written, because the loop lets a
// running prompt finish before it returns on EOF.
func TestAPromptsResponseAndItsEventsShareOneOrder(t *testing.T) {
	lines := runRPC(t, scripted("the answer"),
		`{"id":"1","command":"prompt","message":"a question"}`)

	ackAt, endAt := -1, -1
	for i, l := range lines {
		seq, ok := l["seq"].(float64)
		if !ok || int(seq) != i+1 {
			t.Fatalf("line %d has seq %v: the one order has a gap", i+1, l["seq"])
		}
		if l["family"] == "response" {
			if l["ok"] != true || l["id"] != "1" {
				t.Fatalf("the prompt ack is wrong: %v", l)
			}
			ackAt = i
		}
		if l["family"] == "run" && l["kind"] == "agent_end" {
			endAt = i
		}
	}
	if ackAt < 0 || endAt < 0 {
		t.Fatalf("the stream is missing the ack or the end: ack=%d end=%d", ackAt, endAt)
	}
	if ackAt > endAt {
		t.Fatal("the ack came after the run ended: a receipt that arrives after the work is not one")
	}
	if endAt != len(lines)-1 {
		t.Fatalf("agent_end is not the last line; EOF did not wait for the run: %v", lines[len(lines)-1])
	}
}

// heldModel blocks until its context is cancelled: the shape of a model call
// that only abort can end.
type heldModel struct{}

func (heldModel) Generate(ctx context.Context, _ ai.Request) (ai.Response, error) {
	<-ctx.Done()
	return ai.Response{}, ctx.Err()
}

// TestAbortEndsARunningPromptAndTheStreamSaysAborted. The abort command is read
// while the model is still blocked — which is only possible because a prompt no
// longer holds the loop — and the run's end reports the cancellation rather
// than an error.
func TestAbortEndsARunningPromptAndTheStreamSaysAborted(t *testing.T) {
	lines := runRPC(t, heldModel{},
		`{"id":"1","command":"prompt","message":"never answers"}`,
		`{"id":"2","command":"abort"}`)

	var abortAck map[string]any
	var end map[string]any
	for _, l := range lines {
		if l["family"] == "response" && l["id"] == "2" {
			abortAck = l
		}
		if l["family"] == "run" && l["kind"] == "agent_end" {
			end = l
		}
	}
	if abortAck == nil || abortAck["ok"] != true || !strings.Contains(fmt.Sprint(abortAck["data"]), "true") {
		t.Fatalf("abort did not report cancelling a run: %v", abortAck)
	}
	if end == nil {
		t.Fatal("the aborted run never ended on the stream")
	}
	if detail, _ := end["detail"].(map[string]any); detail["reason"] != "aborted" {
		t.Fatalf("the run's end does not say it was aborted: %v", end)
	}
}

// TestAnUnbuiltCommandFailsWithATypedKind, which is how the channel answers the
// commands ADR-0010 records as incomplete: one at a time, named, rather than
// the mode refusing wholesale.
func TestAnUnbuiltCommandFailsWithATypedKind(t *testing.T) {
	lines := runRPC(t, scripted("unused"), `{"id":"1","command":"cycle_model"}`)
	got := responses(lines)
	errObj := got[0]["error"].(map[string]any)
	if got[0]["ok"] != false || errObj["kind"] != "unimplemented" {
		t.Fatalf("an unbuilt command was not typed unimplemented: %v", got[0])
	}
}

// recordedRuntime opens a conversation that is written to disk, seeded with
// one exchange, so the store-backed commands have a record to work on.
func recordedRuntime(t *testing.T) (cli.Runtime, string) {
	t.Helper()
	dir := t.TempDir()
	work := t.TempDir()
	registry, err := tools.NewBuiltInRegistry(work)
	if err != nil {
		t.Fatalf("building tools: %v", err)
	}
	conversation, err := cli.OpenConversation(cli.Args{SessionDir: dir}, work, cli.DefaultSystemPrompt)
	if err != nil {
		t.Fatalf("opening a conversation: %v", err)
	}
	t.Cleanup(func() { conversation.Close() })
	if err := conversation.Session.AppendAll(
		ai.Message{Role: ai.RoleUser, Content: "first question"},
		ai.Message{Role: ai.RoleAssistant, Content: "first answer"},
	); err != nil {
		t.Fatalf("seeding the record: %v", err)
	}
	return cli.Runtime{
		Model: scripted("unused"), ModelName: "scripted-1", Tools: registry,
		System: cli.DefaultSystemPrompt, Provider: "scripted", Conversation: conversation,
		Args: cli.Args{SessionDir: dir}, WorkingDir: work,
	}, conversation.ID
}

func runRPCWith(t *testing.T, rt cli.Runtime, commands ...string) []map[string]any {
	t.Helper()
	var out, errOut bytes.Buffer
	cli.RunRPC(context.Background(), rt,
		cli.Streams{In: strings.NewReader(strings.Join(commands, "\n") + "\n"), Out: &out, Err: &errOut})
	var lines []map[string]any
	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		var m map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			t.Fatalf("stdout carries a non-JSON line: %q", scanner.Text())
		}
		lines = append(lines, m)
	}
	return responses(lines[1:])
}

// TestTheRecordedConversationCanBeWalkedForkedAndLeft, end to end against a
// real file store: get_tree shows the seeded exchange, fork at its first entry
// opens a NEW conversation holding only that much, and switch_session returns
// to the original with everything. The original file is never changed.
func TestTheRecordedConversationCanBeWalkedForkedAndLeft(t *testing.T) {
	rt, originalID := recordedRuntime(t)

	// First pass: read the tree to learn the entry ids.
	got := runRPCWith(t, rt, `{"id":"1","command":"get_tree"}`)
	tree := got[0]["data"].(map[string]any)
	entries := tree["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("the seeded exchange is not two entries: %v", entries)
	}
	firstID := entries[0].(map[string]any)["id"].(string)

	// Second pass on the same record: fork at the first entry, then come back.
	got = runRPCWith(t, rt,
		`{"id":"2","command":"fork","entry_id":"`+firstID[:20]+`"}`,
		`{"id":"3","command":"get_state"}`,
		`{"id":"4","command":"switch_session","session":"`+originalID+`"}`,
		`{"id":"5","command":"get_state"}`,
		`{"id":"6","command":"new_session"}`,
		`{"id":"7","command":"get_commands"}`,
	)
	forked := got[0]
	if forked["ok"] != true {
		t.Fatalf("fork failed: %v", forked["error"])
	}
	forkData := forked["data"].(map[string]any)
	if forkData["session"] == originalID {
		t.Fatal("fork did not open a new conversation")
	}
	if forkData["message_count"] != float64(1) {
		t.Fatalf("the fork holds %v messages, not the one up to the fork point", forkData["message_count"])
	}
	afterFork := got[1]["data"].(map[string]any)
	if afterFork["message_count"] != float64(1) {
		t.Fatalf("get_state after fork does not describe the fork: %v", afterFork)
	}
	switched := got[2]
	if switched["ok"] != true {
		t.Fatalf("switch_session back to the original failed: %v", switched["error"])
	}
	if switched["data"].(map[string]any)["message_count"] != float64(2) {
		t.Fatalf("the original lost messages: %v", switched["data"])
	}
	if got[3]["data"].(map[string]any)["message_count"] != float64(2) {
		t.Fatalf("get_state after switching back is wrong: %v", got[3]["data"])
	}
	fresh := got[4]["data"].(map[string]any)
	if fresh["message_count"] != float64(0) || fresh["session"] == originalID {
		t.Fatalf("new_session did not start empty and new: %v", fresh)
	}
	cmds := got[5]["data"].(map[string]any)
	if len(cmds["commands"].([]any)) == 0 || len(cmds["not_here"].([]any)) == 0 {
		t.Fatalf("get_commands did not report both halves: %v", cmds)
	}
}

// TestAnUnrecordedRunAnswersUnavailableNotInternalError: NoSession keeps the
// conversation in memory, and the store-backed commands must say that is why.
func TestAnUnrecordedRunAnswersUnavailableNotInternalError(t *testing.T) {
	got := runRPC(t, scripted("unused"),
		`{"id":"1","command":"get_tree"}`, `{"id":"2","command":"clone"}`)
	for _, r := range responses(got) {
		errObj, _ := r["error"].(map[string]any)
		if r["ok"] != false || errObj["kind"] != "unavailable" {
			t.Fatalf("%s on an unrecorded run was not unavailable: %v", r["command"], r)
		}
	}
}
