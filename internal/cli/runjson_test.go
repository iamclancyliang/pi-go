package cli_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/cli"
)

// TestJSONModeWritesOnlyTheStreamToStdout. The mode's whole contract with a
// pipeline is that stdout parses as JSON lines or is empty — an answer, a
// banner or an apology there corrupts whatever reads it.
func TestJSONModeWritesOnlyTheStreamToStdout(t *testing.T) {
	var out, errOut bytes.Buffer
	code := cli.RunJSON(context.Background(),
		runtimeFor(t, scripted("the answer")),
		cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errOut},
		[]string{"a question"})

	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("a successful run wrote to stderr: %q", errOut.String())
	}

	raw := out.String()
	scanner := bufio.NewScanner(&out)
	var kinds []string
	first := true
	for scanner.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("stdout carries a non-JSON line: %q", scanner.Text())
		}
		if first {
			if line["protocol"] != "pi-go-stream" {
				t.Fatalf("the stream did not open with its version: %v", line)
			}
			first = false
			continue
		}
		if kind, ok := line["kind"].(string); ok {
			kinds = append(kinds, kind)
		}
	}
	joined := strings.Join(kinds, " ")
	if !strings.Contains(joined, "agent_start") || !strings.HasSuffix(joined, "agent_end") {
		t.Fatalf("the stream is not framed by the run: %v", kinds)
	}
	// The answer reaches the consumer inside the stream, not beside it.
	if !strings.Contains(raw, "the answer") {
		t.Fatal("the answer is nowhere in the stream")
	}
}

// TestJSONModeReportsAFailureOnStderrAndInTheExitCode, like print mode: a
// script must be able to tell without parsing text, and the events that did
// happen must still have been streamed.
func TestJSONModeReportsAFailureOnStderrAndInTheExitCode(t *testing.T) {
	var out, errOut bytes.Buffer
	code := cli.RunJSON(context.Background(),
		runtimeFor(t, &alwaysFails{because: "the provider refused"}),
		cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errOut},
		[]string{"a question"})

	if code == 0 {
		t.Fatal("a failed run exited zero")
	}
	if !strings.Contains(errOut.String(), "refused") {
		t.Fatalf("the reason did not reach stderr: %q", errOut.String())
	}
	// stdout still holds a stream — version line and the events up to the
	// failure — and every line of it parses.
	scanner := bufio.NewScanner(&out)
	lines := 0
	for scanner.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("a failed run corrupted the stream: %q", scanner.Text())
		}
		lines++
	}
	if lines < 2 {
		t.Fatalf("the failure erased the stream: %d lines", lines)
	}
}

// TestJSONModeRefusesToRunWithNothingToSay: no prompt is a usage error, said
// on stderr, with nothing on stdout for a parser to choke on.
func TestJSONModeRefusesToRunWithNothingToSay(t *testing.T) {
	var out, errOut bytes.Buffer
	code := cli.RunJSON(context.Background(), runtimeFor(t, scripted("unused")),
		cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errOut}, nil)

	if code == 0 {
		t.Fatal("a promptless run exited zero")
	}
	if out.Len() != 0 {
		t.Fatalf("a refused run wrote to stdout: %q", out.String())
	}
}
