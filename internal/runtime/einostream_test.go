package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// streamingFake is a port that streams a scripted reply.
type streamingFake struct {
	chunks []ai.Chunk
	fail   error
}

func (s *streamingFake) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, errors.New("this fake only streams")
}

func (s *streamingFake) Stream(ctx context.Context, _ ai.Request) (<-chan ai.StreamEvent, error) {
	out := make(chan ai.StreamEvent)
	acc := ai.NewAccumulator("fake-1")
	go func() {
		defer close(out)
		start, err := acc.Begin()
		if err != nil {
			return
		}
		out <- start
		open := -1
		for _, c := range s.chunks {
			// Close the previous block when a different one opens, so the stream
			// carries real end events. Without them a conversion that forwarded
			// completed blocks as well as increments would look correct here.
			if open >= 0 && open != c.Index {
				if e, err := acc.Close(open); err == nil {
					out <- e
				}
			}
			events, err := acc.Push(c)
			if err != nil {
				return
			}
			for _, e := range events {
				out <- e
			}
			open = c.Index
		}
		if open >= 0 && s.fail == nil {
			if e, err := acc.Close(open); err == nil {
				out <- e
			}
		}
		if s.fail != nil {
			if e, err := acc.Fail(ai.StopError, s.fail); err == nil {
				out <- e
			}
			return
		}
		if e, err := acc.Done(ai.StopEnd, ai.Usage{}); err == nil {
			out <- e
		}
	}()
	return out, nil
}

// TestFrameworkSeesIncrementsOnlyOnce pins the conversion outward.
//
// The framework concatenates what it is sent, so anything carrying content twice
// is counted twice. Only the increments cross; the boundaries and the terminal do
// not, because they add nothing the framework can use.
func TestFrameworkSeesIncrementsOnlyOnce(t *testing.T) {
	port := &streamingFake{chunks: []ai.Chunk{
		{Index: 0, Kind: ai.BlockThinking, Delta: "hmm"},
		{Index: 1, Kind: ai.BlockText, Delta: "Hel"},
		{Index: 1, Kind: ai.BlockText, Delta: "lo"},
	}}
	model := newEinoChatModel(port, func() string { return "fake-1" })

	reader, err := model.Stream(context.Background(), nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer reader.Close()

	var text, thinking strings.Builder
	for {
		chunk, err := reader.Recv()
		if err != nil {
			break
		}
		text.WriteString(chunk.Content)
		thinking.WriteString(chunk.ReasoningContent)
	}

	if got := text.String(); got != "Hello" {
		t.Errorf("text = %q, want %q: the framework saw content more than once, or "+
			"not at all", got, "Hello")
	}
	if got := thinking.String(); got != "hmm" {
		t.Errorf("reasoning = %q, want %q", got, "hmm")
	}
}

// TestAFailedReplyFailsTheFrameworkStream pins that a cut-off answer is not
// presented as a whole one.
func TestAFailedReplyFailsTheFrameworkStream(t *testing.T) {
	port := &streamingFake{
		chunks: []ai.Chunk{{Index: 0, Kind: ai.BlockText, Delta: "half"}},
		fail:   errors.New("provider hung up"),
	}
	model := newEinoChatModel(port, func() string { return "fake-1" })

	reader, err := model.Stream(context.Background(), nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer reader.Close()

	var failure error
	for {
		_, err := reader.Recv()
		if err != nil {
			failure = err
			break
		}
	}
	if failure == nil {
		t.Fatal("the stream ended cleanly after a failed reply")
	}
	if strings.Contains(failure.Error(), "EOF") {
		t.Errorf("stream ended with %v, want the provider's failure: a cut-off answer "+
			"was presented as a complete one", failure)
	}
}

// TestANonStreamingPortStillWorks pins the fallback.
//
// It is one chunk because that is the whole answer, which is what a provider
// without streaming has to give — not an imitation of streaming.
func TestANonStreamingPortStillWorks(t *testing.T) {
	model := newEinoChatModel(&ai.Scripted{
		Name:  "fake-1",
		Final: ai.AssistantText("all at once"),
	}, func() string { return "fake-1" })

	reader, err := model.Stream(context.Background(), nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer reader.Close()

	var got strings.Builder
	chunks := 0
	for {
		chunk, err := reader.Recv()
		if err != nil {
			break
		}
		chunks++
		got.WriteString(chunk.Content)
	}
	if got.String() != "all at once" {
		t.Errorf("content = %q, want %q", got.String(), "all at once")
	}
	if chunks != 1 {
		t.Errorf("chunks = %d, want 1", chunks)
	}
	_ = time.Now
	_ = schema.Assistant
}
