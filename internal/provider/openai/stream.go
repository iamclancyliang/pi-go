package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	agenticopenai "github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/schema"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// Stream delivers the reply as it arrives.
//
// This is the only place a request to this provider is built and sent. Generate
// collects what this produces rather than repeating the work: two
// request-building paths drift, and only one of them ends up exercised.
func (p *Port) Stream(ctx context.Context, req ai.Request) (<-chan ai.StreamEvent, error) {
	if req.Model == "" {
		// No catalog to consult and no default invented: a request that names
		// nothing would otherwise reach whichever model this port happens to
		// hold, and the reply would not say which.
		return nil, fmt.Errorf("openai: no model named for this request")
	}
	messages, err := toAgentic(req.Messages)
	if err != nil {
		return nil, err
	}
	key, err := p.resolve(ctx)
	if err != nil {
		return nil, err
	}

	// This call's own record, held by the client it is about to use.
	held := &capture{}
	noRetries := 0
	model, err := agenticopenai.NewResponsesModel(ctx, &agenticopenai.ResponsesConfig{
		APIKey:  key,
		BaseURL: p.cfg.BaseURL,
		Model:   req.Model,
		// The adapter's own retry is switched off. Retrying inside the SDK
		// would send billable requests this repository never counted and could
		// not classify.
		MaxRetries: &noRetries,
		HTTPClient: p.httpClient(held),
	})
	if err != nil {
		return nil, fmt.Errorf("openai: building the model: %w", err)
	}

	reader, err := model.Stream(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("openai: starting the stream: %w", err)
	}

	out := make(chan ai.StreamEvent)
	go func() {
		defer close(out)
		defer reader.Close()
		p.pump(ctx, reader, held, out)
	}()
	return out, nil
}

// pump turns the adapter's chunks into this repository's event protocol.
//
// Block identity comes from the provider's own index, carried through by the
// adapter. Renumbering it here to make a stream look contiguous would hide a
// malformed stream rather than report one.
func (p *Port) pump(ctx context.Context, reader *schema.StreamReader[*schema.AgenticMessage],
	held *capture, out chan<- ai.StreamEvent) {

	acc := ai.NewAccumulator(p.cfg.Model)
	blocks := map[int]int{}         // provider index -> block index
	kinds := map[int]ai.BlockKind{} // block index -> kind, while it is open
	next := 0

	cancelled := false
	// A CONTENT event may be abandoned when the caller stops listening: it is
	// one of many, and the reply is being cut short anyway.
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
	sendTerminal := func(ev ai.StreamEvent) { out <- ev }
	fail := func(err error) {
		ev, accErr := acc.Fail(ai.StopError, err)
		if accErr != nil {
			return
		}
		if ev.Final != nil {
			ev.Final.Usage = usageFrom(held.last())
			if served := held.last().Model; served != "" {
				ev.Final.Model = served
			}
		}
		sendTerminal(ev)
	}

	// A cancelled stream still ends with a terminal carrying what had already
	// arrived. A consumer that watched a reply appear should not have it vanish
	// because they stopped it, and a channel that simply closes says nothing
	// about what they have.
	defer func() {
		if !cancelled {
			return
		}
		ev, accErr := acc.Fail(ai.StopAborted, ctx.Err())
		if accErr != nil {
			return
		}
		if ev.Final != nil {
			ev.Final.Usage = usageFrom(held.last())
		}
		sendTerminal(ev)
	}()

	startEv, err := acc.Begin()
	if err != nil {
		return
	}
	if !send(startEv) {
		fail(context.Canceled)
		return
	}

	for {
		chunk, err := reader.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fail(fmt.Errorf("openai: reading the stream: %w", err))
			return
		}
		for _, block := range chunk.ContentBlocks {
			kind, text, call, ok := describe(block)
			if !ok {
				// A block this tranche does not support. Reporting it is the
				// point: silently dropping content would leave a caller with a
				// reply that is missing something nobody mentioned.
				fail(fmt.Errorf("openai: unsupported content block %q", block.Type))
				return
			}
			at, known := blocks[providerIndex(block)]
			if !known {
				// A block ends before the next begins, so a consumer rendering
				// as it goes is never told a block is still growing when it is
				// not. Tool-call blocks are the exception: a provider may
				// interleave their fragments, and closing one when another
				// appears would leave its remaining arguments nowhere to land.
				for open, openKind := range kinds {
					if openKind == ai.BlockToolCall {
						continue
					}
					closed, err := acc.Close(open)
					if err != nil {
						fail(err)
						return
					}
					delete(kinds, open)
					if !send(closed) {
						return
					}
				}
				at = next
				next++
				blocks[providerIndex(block)] = at
				kinds[at] = kind
			}
			events, err := acc.Push(ai.Chunk{Index: at, Kind: kind, Delta: text, Call: call})
			if err != nil {
				fail(err)
				return
			}
			if !send(events...) {
				return
			}
		}
	}

	// Everything still open closes before the reply ends.
	open := make([]int, 0, len(kinds))
	for at := range kinds {
		open = append(open, at)
	}
	sort.Ints(open)
	for _, at := range open {
		closed, err := acc.Close(at)
		if err != nil {
			fail(err)
			return
		}
		if !send(closed) {
			return
		}
	}

	// The ending comes from what the provider said, captured before the adapter
	// reinterpreted it.
	final := held.last()
	reason, statusErr := failureFromStatus(final.Status, final.IncompleteReason)
	if statusErr != nil {
		fail(statusErr)
		return
	}
	done, err := acc.Done(reason, usageFrom(final))
	if err != nil {
		fail(err)
		return
	}
	if done.Final != nil {
		if final.Model != "" {
			done.Final.Model = final.Model
		}
		for _, earlier := range held.earlier() {
			done.Final.EarlierAttempts = append(done.Final.EarlierAttempts, usageFrom(earlier))
		}
	}
	sendTerminal(done)
}

// providerIndex is the provider's own position for a block.
func providerIndex(block *schema.ContentBlock) int {
	if block.StreamingMeta != nil {
		return block.StreamingMeta.Index
	}
	return 0
}

// describe maps a block onto this repository's kinds, reporting whether it is
// one this tranche supports.
func describe(block *schema.ContentBlock) (ai.BlockKind, string, ai.ToolCall, bool) {
	switch block.Type {
	case schema.ContentBlockTypeAssistantGenText:
		if block.AssistantGenText == nil {
			return "", "", ai.ToolCall{}, false
		}
		return ai.BlockText, block.AssistantGenText.Text, ai.ToolCall{}, true
	case schema.ContentBlockTypeReasoning:
		if block.Reasoning == nil {
			return "", "", ai.ToolCall{}, false
		}
		return ai.BlockThinking, block.Reasoning.Text, ai.ToolCall{}, true
	case schema.ContentBlockTypeFunctionToolCall:
		if block.FunctionToolCall == nil {
			return "", "", ai.ToolCall{}, false
		}
		return ai.BlockToolCall, block.FunctionToolCall.Arguments, ai.ToolCall{
			ID:   block.FunctionToolCall.CallID,
			Name: block.FunctionToolCall.Name,
		}, true
	default:
		return "", "", ai.ToolCall{}, false
	}
}
