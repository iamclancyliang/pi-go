package deepseek

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

type wireUsage struct {
	PromptTokens        int  `json:"prompt_tokens"`
	CompletionTokens    int  `json:"completion_tokens"`
	TotalTokens         int  `json:"total_tokens"`
	PromptCacheHit      *int `json:"prompt_cache_hit_tokens"`
	PromptTokensDetails *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens *int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// toUsage keeps "not reported" distinct from "reported zero".
func (w *wireUsage) toUsage() ai.Usage {
	// prompt_tokens is the whole prompt, cached part included. InputTokens is
	// the uncached remainder, matching how Pi reports it: keeping the total
	// here and then adding the cache count anywhere downstream counts the same
	// tokens twice.
	cached := 0
	if w.PromptTokensDetails != nil && w.PromptTokensDetails.CachedTokens != nil {
		cached = *w.PromptTokensDetails.CachedTokens
	} else if w.PromptCacheHit != nil {
		cached = *w.PromptCacheHit
	}
	input := w.PromptTokens - cached
	if input < 0 {
		input = 0
	}
	u := ai.Usage{
		InputTokens:  input,
		OutputTokens: w.CompletionTokens,
		Reported:     true,
	}
	if w.PromptTokensDetails != nil && w.PromptTokensDetails.CachedTokens != nil ||
		w.PromptCacheHit != nil {
		v := cached
		u.CacheReadTokens = &v
	}
	if w.CompletionTokensDetails != nil && w.CompletionTokensDetails.ReasoningTokens != nil {
		v := *w.CompletionTokensDetails.ReasoningTokens
		u.ReasoningTokens = &v
	}
	return u
}

type wireChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content          string     `json:"content"`
			ReasoningContent string     `json:"reasoning_content"`
			ToolCalls        []wireCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *wireUsage `json:"usage"`
}

// Stream delivers the reply as it arrives.
//
// This is the only place a request to this provider is built and sent. Generate
// collects what this produces rather than repeating the work, because two
// request-building paths drift and only one of them ends up exercised.
func (p *Port) Stream(ctx context.Context, req ai.Request) (<-chan ai.StreamEvent, error) {
	body := p.buildRequest(req, true, p.cfg.MaxOutputTokens)
	if body.Model == "" {
		// No catalog exists here to consult and no default is invented: a
		// request that names no model would otherwise reach whichever model
		// the configuration happened to hold.
		return nil, fail(FailureRefused, 0, "no model named for this request")
	}
	resp, attempts, err := p.send(ctx, body)
	if err != nil {
		return nil, err
	}

	out := make(chan ai.StreamEvent)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		p.pump(ctx, resp.Body, out, attempts)
	}()
	return out, nil
}

// pump turns the wire into events. It emits exactly one terminal event.
//
// Block identity belongs to the accumulator, not to this file. Text and
// reasoning arrive as two fields that may alternate, so a boundary between them
// is a change of field. Tool calls do carry a wire index, which is what keeps
// the fragments of one call together; that index is the provider's numbering of
// calls, not a block position, so it is mapped rather than used directly.
func (p *Port) pump(ctx context.Context, body io.Reader, out chan<- ai.StreamEvent, earlier []Attempt) {
	acc := ai.NewAccumulator(p.cfg.Model)
	earlierUsage := make([]ai.Usage, 0, len(earlier))
	for _, a := range earlier {
		earlierUsage = append(earlierUsage, a.Usage)
	}
	var (
		usage  ai.Usage
		served string
		index  int
		kind   ai.BlockKind

		started bool
		// callBlocks maps a wire tool-call index to the block that holds it,
		// so fragments of the same call keep landing in the same block.
		callBlocks = map[int]int{}
		sawCall    bool

		// open is every block index not yet closed. More than one can be open
		// at a time, because a provider may interleave the fragments of several
		// tool calls, and a reply cannot end while any of them is unfinished.
		open = map[int]bool{}
	)

	cancelled := false
	// Why a stopped call stopped. Usually the caller's own context, but the
	// body can report a stop it was told about before that context is
	// observably done — and then ctx.Err() is nil while the call is over all
	// the same, so the reason has to be carried rather than asked for later.
	var stoppedBy error
	// A CONTENT event may be abandoned when the caller stops listening: it is
	// one of many and the reply is being cut short anyway.
	send := func(events ...ai.StreamEvent) bool {
		for _, ev := range events {
			select {
			case out <- ev:
			case <-ctx.Done():
				cancelled = true
				return false
			}
		}
		return true
	}
	// A TERMINAL event may not. It is the only one the stream will ever
	// produce, and abandoning it on a cancelled context loses exactly the event
	// that tells the consumer the reply was aborted. Consumers of this port
	// drain until the channel closes, so this returns as soon as the event is
	// taken.
	sendTerminal := func(ev ai.StreamEvent) { out <- ev }

	// A cancelled stream still ends with a terminal event carrying what had
	// already arrived. A consumer that watched a reply appear should not have
	// it vanish because they stopped it, and a channel that simply closes tells
	// them nothing about what they have.
	endCancelled := func() {
		cause := stoppedBy
		if cause == nil {
			cause = ctx.Err()
		}
		ev, err := acc.Fail(ai.StopAborted, cause)
		if err != nil {
			return
		}
		if ev.Final != nil {
			ev.Final.Usage = usage
			ev.Final.EarlierAttempts = earlierUsage
		}
		// Sent by blocking, not offered. A non-blocking send drops the event
		// whenever the consumer is not waiting at that instant, which loses the
		// only terminal the stream will ever produce — and loses it randomly,
		// so it looks like a rare flake rather than a missing guarantee. A
		// consumer of this port drains until the channel closes, so this
		// returns as soon as it takes the event.
		out <- ev
	}
	defer func() {
		if cancelled {
			endCancelled()
		}
	}()
	fail := func(f Failure, detail string) {
		ev, err := acc.Fail(ai.StopError, fail(f, 0, detail))
		if err != nil {
			return
		}
		// A failure carries what the call had already used. The provider read
		// the request whether or not it could answer it, so dropping the counts
		// here would make failed calls look free.
		if ev.Final != nil {
			ev.Final.Usage = usage
			ev.Final.EarlierAttempts = earlierUsage
			if served != "" {
				ev.Final.Model = served
			}
		}
		sendTerminal(ev)
	}

	// closeBlocks closes the named blocks in order and reports whether the
	// stream may continue.
	closeBlocks := func(indices ...int) bool {
		sort.Ints(indices)
		for _, at := range indices {
			if !open[at] {
				continue
			}
			closed, err := acc.Close(at)
			if err != nil {
				fail(FailureUnknown, err.Error())
				return false
			}
			delete(open, at)
			if !send(closed) {
				return false
			}
		}
		return true
	}
	closeAllBlocks := func() bool {
		all := make([]int, 0, len(open))
		for at := range open {
			all = append(all, at)
		}
		return closeBlocks(all...)
	}

	startEv, err := acc.Begin()
	if err != nil {
		return
	}
	if !send(startEv) {
		fail(FailureUnknown, "the consumer went away")
		return
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk wireChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			fail(FailureUnknown, "undecodable chunk: "+err.Error())
			return
		}
		if chunk.Usage != nil {
			usage = chunk.Usage.toUsage()
		}
		// What served the request is what the provider says served it, not what
		// was asked for: reporting the requested model would make a substitution
		// invisible.
		if chunk.Model != "" {
			served = chunk.Model
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]

		// Reasoning and text are separate fields on the wire; both are blocks
		// here, because a consumer has to know which it is reading.
		for _, piece := range []struct {
			k    ai.BlockKind
			text string
		}{
			{ai.BlockThinking, choice.Delta.ReasoningContent},
			{ai.BlockText, choice.Delta.Content},
		} {
			if piece.text == "" {
				continue
			}
			if started && kind != piece.k {
				// Every open block closes, not just the last one: several
				// tool-call blocks can be open when text follows them.
				if !closeAllBlocks() {
					return
				}
				index++
			}
			started, kind = true, piece.k
			open[index] = true
			events, err := acc.Push(ai.Chunk{Index: index, Kind: piece.k, Delta: piece.text})
			if err != nil {
				fail(FailureUnknown, err.Error())
				return
			}
			if !send(events...) {
				return
			}
		}

		// A tool call arrives across many chunks: the first carries id and
		// name, the rest carry argument fragments and repeat only the wire
		// index. Closing and reopening a block per delta would turn one call
		// into several — most of them nameless.
		for _, call := range choice.Delta.ToolCalls {
			blockIndex, known := callBlocks[call.Index]
			if !known {
				// Close a TEXT or THINKING block before the first call, but
				// never a tool-call block: a provider may interleave the
				// fragments of several calls (0, 1, 0, 1), and closing the
				// earlier one would leave its remaining arguments with nowhere
				// to land.
				if started && kind != ai.BlockToolCall {
					if !closeBlocks(index) {
						return
					}
					index++
				} else if started {
					index++
				}
				blockIndex = index
				callBlocks[call.Index] = blockIndex
				open[blockIndex] = true
				started, kind = true, ai.BlockToolCall
			}
			events, err := acc.Push(ai.Chunk{
				Index: blockIndex,
				Kind:  ai.BlockToolCall,
				Delta: call.Function.Arguments,
				Call:  ai.ToolCall{ID: call.ID, Name: call.Function.Name},
			})
			if err != nil {
				fail(FailureUnknown, err.Error())
				return
			}
			if !send(events...) {
				return
			}
		}

		if choice.FinishReason == nil {
			continue
		}
		if len(choice.Delta.ToolCalls) > 0 || len(callBlocks) > 0 {
			sawCall = true
		}
		ok, truncated, failure := stopReason(*choice.FinishReason)
		if !ok {
			// A 200 is not by itself a success: two documented stop reasons
			// report a failure inside one.
			fail(failure, "stop reason "+*choice.FinishReason)
			return
		}
		if started && !closeAllBlocks() {
			return
		}
		reason := ai.StopEnd
		switch {
		case truncated:
			reason = ai.StopLength
		case *choice.FinishReason == "tool_calls" || sawCall:
			// A reply asking for tools has not finished answering, and a caller
			// that reads it as an ending would drop the request.
			reason = ai.StopToolUse
		}
		// Two count-based overflow checks, before the reply is completed: the
		// accumulator has one ending, and a reply already declared finished
		// cannot then be reported as a failure. Both read typed numbers and
		// neither reads text. They run only against a window someone measured
		// or was given — a rounded figure from documentation would classify
		// replies this provider accepts as overflows and buy a shortened retry
		// of each.
		if f := p.overflow(reason, usage); f != nil {
			ev, err := acc.Fail(ai.StopError, f)
			if err != nil {
				return
			}
			// The call consumed what it consumed, overflow or not.
			if ev.Final != nil {
				ev.Final.Usage = usage
				ev.Final.EarlierAttempts = earlierUsage
				if served != "" {
					ev.Final.Model = served
				}
			}
			sendTerminal(ev)
			return
		}
		done, err := acc.Done(reason, usage)
		if err != nil {
			fail(FailureUnknown, err.Error())
			return
		}
		if done.Final != nil {
			done.Final.EarlierAttempts = earlierUsage
			if served != "" {
				done.Final.Model = served
			}
		}
		sendTerminal(done)
		return
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		// A stop reported through the error rather than through the context
		// ends as one. Classifying it would report the caller's own stop as a
		// provider failure, and a deadline would leave as retryable — while
		// flattening it to text would lose the cause from errors.Is entirely.
		if stopped(err) {
			cancelled, stoppedBy = true, err
			return
		}
		fail(FailureTransient, scrub(err.Error(), ""))
		return
	}
	// The stream ended without a stop reason, so the reply is not known to be
	// complete. Reporting it as finished would hand back a partial answer as
	// the model's last word.
	fail(FailureUnknown, "the stream ended without a finish reason")
}

// overflow reports a context overflow inferred from reported counts.
//
// Absent usage disables both checks rather than reading as zero, which is why
// Usage keeps "not reported" apart from "reported zero": treating silence as
// zero would make the second check fire on every reply.
func (p *Port) overflow(reason ai.StopReason, usage ai.Usage) error {
	window := p.cfg.ContextWindow
	if window <= 0 || !usage.Reported {
		return nil
	}
	input := usage.InputTokens
	if usage.CacheReadTokens != nil {
		input += *usage.CacheReadTokens
	}

	// A reply that ended normally while its input exceeded the window: the
	// provider accepted more than fits and silently dropped the rest.
	if reason == ai.StopEnd && input > window {
		return fmt.Errorf("%w: %d input tokens against a %d window",
			ai.ErrContextOverflow, input, window)
	}

	// A length stop that produced nothing, with the window full: the input
	// consumed the whole context and left no room to answer in.
	if reason == ai.StopLength && usage.OutputTokens == 0 && input >= window*99/100 {
		return fmt.Errorf("%w: %d input tokens filled a %d window, leaving no output",
			ai.ErrContextOverflow, input, window)
	}
	return nil
}
