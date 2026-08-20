package ai

import (
	"errors"
	"strings"
	"testing"
)

// TestSnapshotsDoNotShareStorage pins the divergence from Pi.
//
// Pi hands every event a reference to the one message it keeps mutating, so a
// consumer that holds an event watches it change and can change it back. Here a
// consumer gets a copy. The guarantee is checked in BOTH directions, because
// either alone is satisfied by a copy that is only one level deep.
func TestSnapshotsDoNotShareStorage(t *testing.T) {
	acc := NewAccumulator("fake-1")
	if _, err := acc.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	first, err := acc.Push(Chunk{Index: 0, Kind: BlockText, Delta: "hel"})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	held := first[len(first)-1].Partial

	// The producer keeps going: the held snapshot must not follow it.
	if _, err := acc.Push(Chunk{Index: 0, Kind: BlockText, Delta: "lo"}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if got := held.Blocks[0].Text; got != "hel" {
		t.Errorf("held snapshot = %q, want %q: it followed the producer", got, "hel")
	}

	// The consumer scribbles on what it was given: nothing downstream may see it.
	held.Blocks[0].Text = "REWRITTEN"
	held.Blocks = append(held.Blocks, Block{Kind: BlockText, Text: "INVENTED"})

	done, err := acc.Done(StopEnd, Usage{})
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if got := done.Final.Blocks[0].Text; got != "hello" {
		t.Errorf("final text = %q, want %q: a consumer rewrote the reply", got, "hello")
	}
	if got := len(done.Final.Blocks); got != 1 {
		t.Errorf("final blocks = %d, want 1: a consumer added one", got)
	}
}

// TestBlocksKeepTheirOwnIdentity pins that indices are stable and attributable.
//
// Two text blocks are the case that cannot be recovered downstream: once their
// text is concatenated there is no boundary left, so if identity is not carried
// here it is gone for good.
func TestBlocksKeepTheirOwnIdentity(t *testing.T) {
	acc := NewAccumulator("fake-1")
	if _, err := acc.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	steps := []Chunk{
		{Index: 0, Kind: BlockText, Delta: "first"},
		{Index: 1, Kind: BlockThinking, Delta: "pondering"},
		{Index: 2, Kind: BlockText, Delta: "second"},
		{Index: 0, Kind: BlockText, Delta: "-more"},
	}
	var deltas []StreamEvent
	for _, c := range steps {
		events, err := acc.Push(c)
		if err != nil {
			t.Fatalf("Push %+v: %v", c, err)
		}
		for _, e := range events {
			if e.Kind == StreamTextDelta || e.Kind == StreamThinkingDelta {
				deltas = append(deltas, e)
			}
		}
	}

	// Interleaving is allowed, so a returning block keeps its original index.
	wantIndex := []int{0, 1, 2, 0}
	for i, e := range deltas {
		if e.ContentIndex != wantIndex[i] {
			t.Errorf("delta %d attributed to block %d, want %d", i, e.ContentIndex, wantIndex[i])
		}
		// The block named by the event is the one that grew.
		block := e.Partial.Blocks[e.ContentIndex]
		if !strings.Contains(block.Text, e.Delta) {
			t.Errorf("delta %q is not in the block it names (%q)", e.Delta, block.Text)
		}
	}

	done, err := acc.Done(StopEnd, Usage{})
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if got := len(done.Final.Blocks); got != 3 {
		t.Fatalf("blocks = %d, want 3: two text blocks were merged", got)
	}
	if done.Final.Blocks[0].Text != "first-more" || done.Final.Blocks[2].Text != "second" {
		t.Errorf("blocks = %q / %q, want %q / %q",
			done.Final.Blocks[0].Text, done.Final.Blocks[2].Text, "first-more", "second")
	}
}

// TestAChunkWithoutIdentityIsRefused pins that boundaries are never guessed.
func TestAChunkWithoutIdentityIsRefused(t *testing.T) {
	cases := []struct {
		name  string
		chunk Chunk
	}{
		{"no kind", Chunk{Index: 0, Delta: "x"}},
		{"negative index", Chunk{Index: -1, Kind: BlockText, Delta: "x"}},
		{"index skips a block", Chunk{Index: 3, Kind: BlockText, Delta: "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			acc := NewAccumulator("fake-1")
			if _, err := acc.Begin(); err != nil {
				t.Fatalf("Begin: %v", err)
			}
			if _, err := acc.Push(c.chunk); !errors.Is(err, ErrBlockIdentity) {
				t.Errorf("error = %v, want it to refuse for want of identity", err)
			}
		})
	}
}

// TestAKindMayNotChangeUnderAnIndex pins that identity means the same block.
func TestAKindMayNotChangeUnderAnIndex(t *testing.T) {
	acc := NewAccumulator("fake-1")
	if _, err := acc.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := acc.Push(Chunk{Index: 0, Kind: BlockText, Delta: "hello"}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	_, err := acc.Push(Chunk{Index: 0, Kind: BlockThinking, Delta: "?"})
	if !errors.Is(err, ErrBlockIdentity) {
		t.Errorf("error = %v, want a refusal: index 0 is text and cannot become thinking", err)
	}
}

// TestFailureKeepsWhatArrived pins the guarantee pi-go makes and Pi does not.
func TestFailureKeepsWhatArrived(t *testing.T) {
	acc := NewAccumulator("fake-1")
	if _, err := acc.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := acc.Push(Chunk{Index: 0, Kind: BlockText, Delta: "half an ans"}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	event, err := acc.Fail(StopAborted, errors.New("caller cancelled"))
	if err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if event.Kind != StreamError {
		t.Fatalf("kind = %q, want %q", event.Kind, StreamError)
	}
	if event.Final.StopReason != StopAborted {
		t.Errorf("reason = %q, want %q: cancellation is not a separate outcome",
			event.Final.StopReason, StopAborted)
	}
	if len(event.Final.Blocks) != 1 || event.Final.Blocks[0].Text != "half an ans" {
		t.Errorf("blocks = %+v, want the partial answer kept", event.Final.Blocks)
	}
	if event.Final.ErrorMessage == "" {
		t.Error("no error text: a failure a caller cannot read is one it cannot act on")
	}
}

// TestAnOpenBlockIsNotClosedForYou pins that ends are not invented.
func TestAnOpenBlockIsNotClosedForYou(t *testing.T) {
	acc := NewAccumulator("fake-1")
	if _, err := acc.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := acc.Push(Chunk{Index: 0, Kind: BlockText, Delta: "unfinished"}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	event, err := acc.Fail(StopError, errors.New("provider hung up"))
	if err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if event.Kind != StreamError {
		t.Fatalf("kind = %q, want an error terminal", event.Kind)
	}
	// No text_end was produced: nothing said that block finished.
	if event.Content != "" {
		t.Errorf("the terminal carried block content %q, inventing an end", event.Content)
	}
}

// TestAToolCallAccumulatesItsArguments pins the third block kind.
//
// A tool call's arguments arrive in fragments like any other content, but they
// are not text: they belong to the call, and a consumer reading the block's text
// would find nothing. The completed call is carried on the end event, because
// that is the first moment its arguments are whole enough to act on.
func TestAToolCallAccumulatesItsArguments(t *testing.T) {
	acc := NewAccumulator("fake-1")
	if _, err := acc.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	opening := ToolCall{ID: "call-1", Name: "read_files"}
	if _, err := acc.Push(Chunk{Index: 0, Kind: BlockToolCall, Call: opening, Delta: `{"pa`}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	events, err := acc.Push(Chunk{Index: 0, Kind: BlockToolCall, Delta: `th":"/tmp/x"}`})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	last := events[len(events)-1]
	if last.Kind != StreamToolCallDelta {
		t.Fatalf("kind = %q, want %q", last.Kind, StreamToolCallDelta)
	}
	block := last.Partial.Blocks[0]
	if block.Text != "" {
		t.Errorf("tool call arguments landed in the block's text (%q)", block.Text)
	}
	if got := block.Call.Args; got != `{"pa` {
		// The delta that just arrived is included, so the block holds both halves.
		if got != `{"path":"/tmp/x"}` {
			t.Errorf("arguments = %q, want the fragments joined", got)
		}
	}

	end, err := acc.Close(0)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if end.Kind != StreamToolCallEnd {
		t.Fatalf("kind = %q, want %q", end.Kind, StreamToolCallEnd)
	}
	if end.Call.ID != "call-1" || end.Call.Name != "read_files" {
		t.Errorf("call = %+v, want the identity it opened with", end.Call)
	}
	if end.Call.Args != `{"path":"/tmp/x"}` {
		t.Errorf("call arguments = %q, want the fragments joined", end.Call.Args)
	}
	if end.Content != "" {
		t.Errorf("a tool call reported text content %q", end.Content)
	}
}

// TestAClosedBlockTakesNoMore pins that a finished block stays finished.
//
// Its end event already told a consumer what the block says. Appending after that
// would leave the consumer holding a value the stream has since contradicted.
func TestAClosedBlockTakesNoMore(t *testing.T) {
	acc := NewAccumulator("fake-1")
	if _, err := acc.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := acc.Push(Chunk{Index: 0, Kind: BlockText, Delta: "done"}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if _, err := acc.Close(0); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := acc.Push(Chunk{Index: 0, Kind: BlockText, Delta: "more"}); !errors.Is(err, ErrBlockIdentity) {
		t.Errorf("error = %v, want a refusal: the block was already reported closed", err)
	}
	if _, err := acc.Close(0); !errors.Is(err, ErrBlockIdentity) {
		t.Errorf("closing twice = %v, want a refusal", err)
	}
}

// TestAStreamEndsOnce pins that a finished stream cannot be reopened or ended
// twice.
//
// A second terminal would give a consumer two different final answers for one
// reply, and nothing in the protocol says which to believe.
func TestAStreamEndsOnce(t *testing.T) {
	t.Run("no second start", func(t *testing.T) {
		acc := NewAccumulator("fake-1")
		if _, err := acc.Begin(); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if _, err := acc.Begin(); err == nil {
			t.Error("a second start was allowed, so a consumer would see one reply begin twice")
		}
	})

	t.Run("no terminal after done", func(t *testing.T) {
		acc := NewAccumulator("fake-1")
		if _, err := acc.Begin(); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if _, err := acc.Done(StopEnd, Usage{}); err != nil {
			t.Fatalf("Done: %v", err)
		}
		if _, err := acc.Done(StopEnd, Usage{}); err == nil {
			t.Error("a second done was allowed")
		}
		if _, err := acc.Fail(StopError, errors.New("late")); err == nil {
			t.Error("a failure after a successful end was allowed")
		}
	})

	t.Run("no terminal after failure", func(t *testing.T) {
		acc := NewAccumulator("fake-1")
		if _, err := acc.Begin(); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if _, err := acc.Fail(StopError, errors.New("first")); err != nil {
			t.Fatalf("Fail: %v", err)
		}
		if _, err := acc.Fail(StopError, errors.New("second")); err == nil {
			t.Error("a second failure was allowed")
		}
		if _, err := acc.Done(StopEnd, Usage{}); err == nil {
			t.Error("a success after a failure was allowed")
		}
	})
}
