package cli_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
// the numbers are 1..N with no gap. The prompt's ack is numbered AFTER the
// events it produced, because the work finished before the ack was made.
func TestAPromptsResponseAndItsEventsShareOneOrder(t *testing.T) {
	lines := runRPC(t, scripted("the answer"),
		`{"id":"1","command":"prompt","message":"a question"}`)

	sawRun, sawResponse := false, false
	for i, l := range lines {
		seq, ok := l["seq"].(float64)
		if !ok || int(seq) != i+1 {
			t.Fatalf("line %d has seq %v: the one order has a gap", i+1, l["seq"])
		}
		switch l["family"] {
		case "run", "reply":
			sawRun = true
		case "response":
			sawResponse = true
			if !sawRun {
				t.Fatal("the prompt ack came before any of its events")
			}
			if l["ok"] != true || l["id"] != "1" {
				t.Fatalf("the prompt ack is wrong: %v", l)
			}
		}
	}
	if !sawRun || !sawResponse {
		t.Fatalf("the stream is missing a family: run=%v response=%v", sawRun, sawResponse)
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
