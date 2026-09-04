// Package jsonstream serialises the run's two event families as JSON lines.
//
// This is what --mode json writes: one object per line on stdout, opening with
// a line that names the protocol version, then lifecycle and reply events as
// they happen. The two families stay apart on the wire the way they are apart
// in the runtime — folding thousands of content deltas into the lifecycle
// stream would drown the events a client watches for — and a single sequence
// number spanning both is what lets a consumer interleave them correctly
// (decided in ADR-0009, with the accounting against Pi's stream).
//
// The shapes here are a published contract, pinned by golden tests: a field
// renamed to suit a refactor is a client broken.
package jsonstream

import (
	"encoding/json"
	"io"
	"sync"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/rpc"
)

// Version is the stream protocol version, named on the first line so a
// consumer can refuse a shape it does not know before misreading one event.
// Pi's stream has no such marker; a reader of it finds out it guessed wrong
// only by what breaks.
const Version = 1

// header is the first line of every stream.
type header struct {
	Protocol string `json:"protocol"`
	Version  int    `json:"version"`
}

// runLine is one lifecycle event on the wire: the event as the runtime
// published it, marked with the family it belongs to.
type runLine struct {
	Family string `json:"family"`
	events.Event
}

// replyLine is one reply event on the wire.
//
// It never carries the in-process snapshot (StreamEvent.Partial): deltas build
// the reply and the terminal line is authoritative, so repeating the
// accumulated message on every delta would make output quadratic in the length
// of an answer — the cost Pi's own wire transform exists to avoid. The
// snapshot stays available in process, for renderers; it is the wire that has
// no use for it.
type replyLine struct {
	Family string             `json:"family"`
	Seq    int                `json:"seq"`
	Kind   ai.StreamEventKind `json:"kind"`

	// ContentIndex is present on the nine block events and absent otherwise.
	// A pointer because index zero is a real block, not an empty field.
	ContentIndex *int `json:"content_index,omitempty"`

	Delta    string        `json:"delta,omitempty"`
	Content  string        `json:"content,omitempty"`
	ToolCall *toolCallLine `json:"tool_call,omitempty"`

	// Final is the complete reply, on done and error alone. On error it
	// carries whatever had arrived, which is how a failed reply still shows
	// what it said and spent.
	Final *finalLine `json:"final,omitempty"`
}

type toolCallLine struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

type blockLine struct {
	Kind     ai.BlockKind  `json:"kind"`
	Text     string        `json:"text,omitempty"`
	ToolCall *toolCallLine `json:"tool_call,omitempty"`
}

// usageLine keeps the ledger's absent-versus-zero distinction on the wire: a
// count the provider never reported is omitted, a measured zero is written as
// zero. Flattening the two would leave a reader unable to tell "did none"
// from "did not say", which is the distinction the ledger exists to keep.
type usageLine struct {
	InputTokens     int  `json:"input_tokens"`
	OutputTokens    int  `json:"output_tokens"`
	CacheReadTokens *int `json:"cache_read_tokens,omitempty"`
	ReasoningTokens *int `json:"reasoning_tokens,omitempty"`
	Reported        bool `json:"reported"`
}

type finalLine struct {
	Model        string      `json:"model,omitempty"`
	StopReason   string      `json:"stop_reason"`
	ErrorMessage string      `json:"error_message,omitempty"`
	Blocks       []blockLine `json:"blocks"`
	Usage        usageLine   `json:"usage"`

	// EarlierAttempts is what attempts before this one reported using: a call
	// that retried spent on every attempt, and a wire that dropped them would
	// under-report exactly the spend the retry created.
	EarlierAttempts []usageLine `json:"earlier_attempts,omitempty"`
}

// Writer serialises every family — lifecycle, reply, and command responses —
// to one stream, and numbers them.
//
// It implements events.Observer and the runtime's reply-observer contract, so
// wiring it into a run is passing it twice; the RPC channel hands it responses
// through WriteResponse. The families arrive from different goroutines, and
// the wire's one promise is that `seq` is the order things were written. That
// promise holds only if a line's number is allocated and the line written under
// ONE lock: a number taken before the lock can reach the wire after a higher
// one taken by another goroutine that got to the lock first. So the counter
// lives here, and the number the runtime assigned in process is replaced on
// the way out — in-process order is for in-process observers; the wire has its
// own, and it is the write order by construction.
//
// The first write error latches and every later write becomes a no-op: a
// broken pipe reported once, at the end, is a diagnosis, and reported on every
// event it is noise that hides it.
type Writer struct {
	mu  sync.Mutex
	seq events.Sequence
	out io.Writer
	err error
}

// NewWriter starts a stream on out by writing the version line.
func NewWriter(out io.Writer) *Writer {
	w := &Writer{out: out}
	w.write(header{Protocol: "pi-go-stream", Version: Version}, nil)
	return w
}

// OnEvent implements events.Observer.
func (w *Writer) OnEvent(e events.Event) {
	line := runLine{Family: "run", Event: e}
	w.write(&line, func(n int) { line.Seq = n })
}

// Reply implements the runtime's ReplyObserver contract.
func (w *Writer) Reply(e ai.StreamEvent) {
	line := replyLine{Family: "reply", Seq: e.Seq, Kind: e.Kind}

	switch e.Kind {
	case ai.StreamTextStart, ai.StreamTextDelta, ai.StreamTextEnd,
		ai.StreamThinkingStart, ai.StreamThinkingDelta, ai.StreamThinkingEnd,
		ai.StreamToolCallStart, ai.StreamToolCallDelta, ai.StreamToolCallEnd:
		index := e.ContentIndex
		line.ContentIndex = &index
	}

	switch e.Kind {
	case ai.StreamTextDelta, ai.StreamThinkingDelta, ai.StreamToolCallDelta:
		line.Delta = e.Delta
	case ai.StreamTextEnd, ai.StreamThinkingEnd:
		line.Content = e.Content
	case ai.StreamToolCallEnd:
		line.ToolCall = &toolCallLine{ID: e.Call.ID, Name: e.Call.Name, Args: e.Call.Args}
	case ai.StreamDone, ai.StreamError:
		line.Final = finalFrom(e.Final)
	}

	w.write(&line, func(n int) { line.Seq = n })
}

// WriteResponse writes one command response. It is how the RPC channel joins
// this stream: the response family lands in the one order among the run and
// reply families, numbered by the same counter under the same lock.
func (w *Writer) WriteResponse(resp rpc.Response) error {
	w.write(&resp, func(n int) { resp.Seq = n })
	return w.Err()
}

// Err reports the first write failure, or nil.
func (w *Writer) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// write numbers and writes one line under the lock. setSeq receives the
// allocated number and must store it into v before it is marshalled; nil for
// the one line that carries no number.
func (w *Writer) write(v any, setSeq func(int)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return
	}
	if setSeq != nil {
		setSeq(w.seq.Next())
	}

	encoded, err := json.Marshal(v)
	if err != nil {
		w.err = err
		return
	}
	if _, err := w.out.Write(append(encoded, '\n')); err != nil {
		w.err = err
	}
}

func finalFrom(m *ai.AssistantMessage) *finalLine {
	if m == nil {
		// A terminal without its message would be a bug in the runtime, but
		// the wire reports what arrived rather than inventing a message.
		return nil
	}
	line := &finalLine{
		Model:        m.Model,
		StopReason:   string(m.StopReason),
		ErrorMessage: m.ErrorMessage,
		Blocks:       make([]blockLine, 0, len(m.Blocks)),
		Usage:        usageFrom(m.Usage),
	}
	for _, b := range m.Blocks {
		out := blockLine{Kind: b.Kind}
		switch b.Kind {
		case ai.BlockToolCall:
			out.ToolCall = &toolCallLine{ID: b.Call.ID, Name: b.Call.Name, Args: b.Call.Args}
		default:
			out.Text = b.Text
		}
		line.Blocks = append(line.Blocks, out)
	}
	for _, u := range m.EarlierAttempts {
		line.EarlierAttempts = append(line.EarlierAttempts, usageFrom(u))
	}
	return line
}

func usageFrom(u ai.Usage) usageLine {
	line := usageLine{
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		Reported:     u.Reported,
	}
	// Copied rather than aliased, so a line already handed to the encoder can
	// never change under it.
	if u.CacheReadTokens != nil {
		v := *u.CacheReadTokens
		line.CacheReadTokens = &v
	}
	if u.ReasoningTokens != nil {
		v := *u.ReasoningTokens
		line.ReasoningTokens = &v
	}
	return line
}
