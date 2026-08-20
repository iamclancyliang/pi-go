package deepseek

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
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
	u := ai.Usage{
		InputTokens:  w.PromptTokens,
		OutputTokens: w.CompletionTokens,
		Reported:     true,
	}
	if w.PromptTokensDetails != nil && w.PromptTokensDetails.CachedTokens != nil {
		v := *w.PromptTokensDetails.CachedTokens
		u.CacheReadTokens = &v
	} else if w.PromptCacheHit != nil {
		v := *w.PromptCacheHit
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
	resp, err := p.post(ctx, p.buildRequest(req, true, p.cfg.MaxOutputTokens))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, failureFrom(resp)
	}

	out := make(chan ai.StreamEvent)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		p.pump(ctx, resp.Body, out)
	}()
	return out, nil
}

// pump turns the wire into events. It emits exactly one terminal event.
//
// Block identity belongs to the accumulator, not to this file: the wire has no
// block indices, only two fields that may alternate, so a boundary here is a
// change of field and the accumulator decides what that means for the reply.
func (p *Port) pump(ctx context.Context, body io.Reader, out chan<- ai.StreamEvent) {
	acc := ai.NewAccumulator(p.cfg.Model)
	var (
		usage   ai.Usage
		index   int
		kind    ai.BlockKind
		started bool
	)

	send := func(events ...ai.StreamEvent) bool {
		for _, ev := range events {
			select {
			case out <- ev:
			case <-ctx.Done():
				return false
			}
		}
		return true
	}
	fail := func(f Failure, detail string) {
		ev, err := acc.Fail(ai.StopError, &Error{Failure: f, Detail: detail})
		if err != nil {
			return
		}
		send(ev)
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
				closed, err := acc.Close(index)
				if err != nil {
					fail(FailureUnknown, err.Error())
					return
				}
				if !send(closed) {
					return
				}
				index++
			}
			started, kind = true, piece.k
			events, err := acc.Push(ai.Chunk{Index: index, Kind: piece.k, Delta: piece.text})
			if err != nil {
				fail(FailureUnknown, err.Error())
				return
			}
			if !send(events...) {
				return
			}
		}

		for _, call := range choice.Delta.ToolCalls {
			if started {
				closed, err := acc.Close(index)
				if err != nil {
					fail(FailureUnknown, err.Error())
					return
				}
				if !send(closed) {
					return
				}
				index++
			}
			started, kind = true, ai.BlockToolCall
			events, err := acc.Push(ai.Chunk{
				Index: index,
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
		ok, truncated, failure := stopReason(*choice.FinishReason)
		if !ok {
			// A 200 is not by itself a success: two documented stop reasons
			// report a failure inside one.
			fail(failure, "stop reason "+*choice.FinishReason)
			return
		}
		if started {
			closed, err := acc.Close(index)
			if err != nil {
				fail(FailureUnknown, err.Error())
				return
			}
			if !send(closed) {
				return
			}
		}
		reason := ai.StopEnd
		if truncated {
			reason = ai.StopLength
		}
		done, err := acc.Done(reason, usage)
		if err != nil {
			fail(FailureUnknown, err.Error())
			return
		}
		send(done)
		return
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		fail(FailureTransient, err.Error())
		return
	}
	// The stream ended without a stop reason, so the reply is not known to be
	// complete. Reporting it as finished would hand back a partial answer as
	// the model's last word.
	fail(FailureUnknown, "the stream ended without a finish reason")
}
