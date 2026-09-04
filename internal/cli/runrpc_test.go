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
