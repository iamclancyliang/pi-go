package jsonstream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
)

func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	scanner := bufio.NewScanner(buf)
	for scanner.Scan() {
		var m map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			t.Fatalf("a line is not JSON: %q: %v", scanner.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

// TestTheStreamOpensByNamingItsVersion: a consumer must be able to refuse a
// shape it does not know before misreading a single event.
func TestTheStreamOpensByNamingItsVersion(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.OnEvent(events.Event{Seq: 1, Kind: events.KindAgentStart, Time: time.Unix(0, 0)})

	got := lines(t, &buf)
	if len(got) != 2 {
		t.Fatalf("wanted the version line and one event, got %d lines", len(got))
	}
	if got[0]["protocol"] != "pi-go-stream" || got[0]["version"] != float64(Version) {
		t.Fatalf("the first line does not name the protocol: %v", got[0])
	}
	if got[1]["family"] != "run" || got[1]["kind"] != "agent_start" || got[1]["seq"] != float64(1) {
		t.Fatalf("the run line lost its identity: %v", got[1])
	}
}

// TestAReplyLineNeverCarriesTheSnapshot. The in-process event hands renderers
// the accumulated reply on every delta; serialised, that is output quadratic in
// the length of the answer. The wire's rule is the one Pi's own transform
// enforces: deltas build, the terminal is authoritative.
func TestAReplyLineNeverCarriesTheSnapshot(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	snapshot := &ai.AssistantMessage{Blocks: []ai.Block{{Kind: ai.BlockText, Text: "so far"}}}
	w.Reply(ai.StreamEvent{Seq: 2, Kind: ai.StreamTextDelta, ContentIndex: 0, Delta: "so", Partial: snapshot})

	raw := buf.String()
	if strings.Contains(raw, "partial") || strings.Contains(raw, "so far") {
		t.Fatalf("the snapshot reached the wire: %s", raw)
	}
	got := lines(t, &buf)
	line := got[1]
	if line["family"] != "reply" || line["delta"] != "so" || line["seq"] != float64(1) {
		t.Fatalf("the delta line lost its content: %v", line)
	}
	// Index zero is a real block, and omitting it would make the first block's
	// events unattributable.
	if v, present := line["content_index"]; !present || v != float64(0) {
		t.Fatalf("content_index 0 was dropped: %v", line)
	}
}

// TestATerminalCarriesTheWholeReplyAndItsSpend, because on error that is how a
// failed reply still shows what it said and what it cost.
func TestATerminalCarriesTheWholeReplyAndItsSpend(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	zero := 0
	w.Reply(ai.StreamEvent{
		Seq:  3,
		Kind: ai.StreamError,
		Final: &ai.AssistantMessage{
			Blocks:       []ai.Block{{Kind: ai.BlockText, Text: "half an ans"}},
			StopReason:   ai.StopError,
			ErrorMessage: "the provider hung up",
			Model:        "fake-1",
			Usage:        ai.Usage{InputTokens: 7, OutputTokens: 2, CacheReadTokens: &zero, Reported: true},
		},
	})

	line := lines(t, &buf)[1]
	final, ok := line["final"].(map[string]any)
	if !ok {
		t.Fatalf("the terminal has no final: %v", line)
	}
	if final["error_message"] != "the provider hung up" || final["stop_reason"] != "error" {
		t.Fatalf("the failure lost its reason: %v", final)
	}
	usage := final["usage"].(map[string]any)
	if usage["input_tokens"] != float64(7) || usage["reported"] != true {
		t.Fatalf("the spend did not survive: %v", usage)
	}
	// A measured zero is a zero on the wire; only silence is omitted.
	if v, present := usage["cache_read_tokens"]; !present || v != float64(0) {
		t.Fatalf("a measured zero cache read was dropped: %v", usage)
	}
	if _, present := usage["reasoning_tokens"]; present {
		t.Fatalf("an unreported count was invented: %v", usage)
	}
}

// TestAContentIndexAppearsOnlyWhereItMeansSomething: start, done and error
// have no block to point at, and writing a zero there would claim they do.
func TestAContentIndexAppearsOnlyWhereItMeansSomething(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.Reply(ai.StreamEvent{Seq: 1, Kind: ai.StreamStart})
	w.Reply(ai.StreamEvent{Seq: 2, Kind: ai.StreamToolCallEnd, ContentIndex: 1,
		Call: ai.ToolCall{ID: "call_1", Name: "read", Args: `{"path":"a"}`}})

	got := lines(t, &buf)
	if _, present := got[1]["content_index"]; present {
		t.Fatalf("start claimed a block: %v", got[1])
	}
	end := got[2]
	if end["content_index"] != float64(1) {
		t.Fatalf("toolcall_end lost its block: %v", end)
	}
	call := end["tool_call"].(map[string]any)
	if call["id"] != "call_1" || call["name"] != "read" {
		t.Fatalf("the finished call lost its identity: %v", call)
	}
}

type failingWriter struct{ after int }

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.after <= 0 {
		return 0, errors.New("the pipe closed")
	}
	f.after--
	return len(p), nil
}

// TestTheFirstWriteFailureLatches: a broken pipe reported once at the end is a
// diagnosis; reported on every event it is noise that hides it.
func TestTheFirstWriteFailureLatches(t *testing.T) {
	w := NewWriter(&failingWriter{after: 1})
	if w.Err() != nil {
		t.Fatalf("the version line failed: %v", w.Err())
	}
	w.OnEvent(events.Event{Seq: 1, Kind: events.KindAgentStart})
	if w.Err() == nil {
		t.Fatal("a failed write went unreported")
	}
	first := w.Err()
	w.OnEvent(events.Event{Seq: 2, Kind: events.KindAgentEnd})
	if w.Err() != first {
		t.Fatal("a later write replaced the first failure")
	}
}

// TestTheOrderIsTheWriteOrderUnderConcurrency is the wire's one promise, made
// falsifiable: two goroutines write as fast as they can, and the seq on each
// line must equal that line's position. A number allocated before the lock
// rather than under it lets a lower number reach the wire after a higher one,
// which this burst is wide enough to catch.
func TestTheOrderIsTheWriteOrderUnderConcurrency(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	const perWriter = 2000
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < perWriter; i++ {
			w.OnEvent(events.Event{Kind: events.KindTurnStart})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < perWriter; i++ {
			w.Reply(ai.StreamEvent{Kind: ai.StreamTextDelta, Delta: "x"})
		}
	}()
	wg.Wait()

	got := lines(t, &buf)
	for i, line := range got[1:] {
		if seq, _ := line["seq"].(float64); int(seq) != i+1 {
			t.Fatalf("line %d carries seq %v: a number reached the wire out of order", i+1, seq)
		}
	}
}
