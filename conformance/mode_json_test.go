package conformance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/jsonstream"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// streamLines runs one streamed prompt through a jsonstream.Writer and returns
// every line of the wire, parsed.
func streamLines(t *testing.T) []map[string]any {
	t.Helper()

	model := &streamOnlyModel{blocks: []ai.Chunk{
		{Index: 0, Kind: ai.BlockThinking, Delta: "let me think"},
		{Index: 1, Kind: ai.BlockText, Delta: "the "},
		{Index: 1, Kind: ai.BlockText, Delta: "answer"},
	}}
	var buf bytes.Buffer
	writer := jsonstream.NewWriter(&buf)

	agent, err := runtime.New(runtime.Config{
		Model:          model,
		ModelName:      "fake-1",
		Tools:          tools.NewRegistry(),
		Session:        session.New("You are pi-go."),
		Observers:      []events.Observer{writer},
		ReplyObservers: []runtime.ReplyObserver{writer},
	})
	if err != nil {
		t.Fatalf("building the agent: %v", err)
	}
	if err := agent.Run(context.Background(), "a question"); err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	if err := writer.Err(); err != nil {
		t.Fatalf("the stream broke: %v", err)
	}

	var out []map[string]any
	scanner := bufio.NewScanner(&buf)
	for scanner.Scan() {
		var m map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			t.Fatalf("a line is not JSON: %q: %v", scanner.Text(), err)
		}
		out = append(out, m)
	}
	if strings.Contains(buf.String(), `"partial"`) {
		t.Fatalf("a snapshot reached the wire")
	}
	return out
}

// TestOneCounterSpansBothFamilies is the wire's only ordering claim, asserted:
// after the version line, every line carries seq, the numbers are 1..N with no
// gap and no repeat, and BOTH families appear inside the one order. Two
// counters wearing one name would pass any test that read the families apart.
func TestOneCounterSpansBothFamilies(t *testing.T) {
	lines := streamLines(t)

	if lines[0]["protocol"] != "pi-go-stream" {
		t.Fatalf("the stream did not open with its version: %v", lines[0])
	}

	families := map[string]int{}
	for i, line := range lines[1:] {
		family, _ := line["family"].(string)
		families[family]++
		seq, ok := line["seq"].(float64)
		if !ok {
			t.Fatalf("line %d carries no seq: %v", i+1, line)
		}
		if int(seq) != i+1 {
			t.Fatalf("line %d has seq %v: the order has a gap or a repeat", i+1, seq)
		}
	}
	if families["run"] == 0 || families["reply"] == 0 {
		t.Fatalf("a family is missing from the stream: %v", families)
	}
	if families["run"]+families["reply"] != len(lines)-1 {
		t.Fatalf("a line belongs to neither family: %v", families)
	}
}

// TestTheStreamCarriesTheReplyAndItsLifecycle pins the wire's shape for one
// streamed answer, the way the golden trace pins the lifecycle: a consumer
// watches the reply form block by block, and the run events frame it.
func TestTheStreamCarriesTheReplyAndItsLifecycle(t *testing.T) {
	lines := streamLines(t)

	var runKinds, replyKinds []string
	var sawIndexOnDelta, thinkingKeptApart bool
	for _, line := range lines[1:] {
		kind, _ := line["kind"].(string)
		switch line["family"] {
		case "run":
			runKinds = append(runKinds, kind)
		case "reply":
			replyKinds = append(replyKinds, kind)
			if kind == "text_delta" && line["content_index"] == float64(1) {
				sawIndexOnDelta = true
			}
			if kind == "thinking_end" && line["content"] == "let me think" {
				thinkingKeptApart = true
			}
		}
	}

	wantRun := []string{"agent_start", "turn_start", "model_request", "model_response", "turn_end", "agent_end"}
	if strings.Join(runKinds, " ") != strings.Join(wantRun, " ") {
		t.Fatalf("the lifecycle is not the golden one:\ngot  %v\nwant %v", runKinds, wantRun)
	}
	want := []string{"start", "thinking_start", "thinking_delta", "thinking_end",
		"text_start", "text_delta", "text_delta", "text_end", "done"}
	if strings.Join(replyKinds, " ") != strings.Join(want, " ") {
		t.Fatalf("the reply protocol is not the recorded one:\ngot  %v\nwant %v", replyKinds, want)
	}
	if !sawIndexOnDelta {
		t.Fatal("a delta arrived without the block index that attributes it")
	}
	if !thinkingKeptApart {
		t.Fatal("thinking did not close as its own block with its own content")
	}
	last := lines[len(lines)-1]
	if last["family"] != "run" || last["kind"] != "agent_end" {
		t.Fatalf("the stream does not end with agent_end: %v", last)
	}
}
