package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	agenticopenai "github.com/cloudwego/eino-ext/components/model/agenticopenai"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// Stream delivers the reply as it arrives.
//
// This is the only place a request to this provider is built and sent. Generate
// collects what this produces rather than repeating the work: two
// request-building paths drift, and only one of them ends up exercised.
func (p *Port) Stream(ctx context.Context, req ai.Request) (<-chan ai.StreamEvent, error) {
	if req.Model != "" && req.Model != p.cfg.Model {
		return nil, fail(FailureRefused, 0,
			fmt.Sprintf("this port serves %q; the request named %q", p.cfg.Model, req.Model))
	}
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
	outputCap := p.cfg.MaxOutputTokens
	chat, err := agenticopenai.NewResponsesModel(ctx, &agenticopenai.ResponsesConfig{
		APIKey:  key,
		BaseURL: p.cfg.BaseURL,
		Model:   req.Model,
		// The cap has to reach the request. Requiring it at construction says
		// nothing about what was sent, and a reply with no cap is a bill nobody
		// chose.
		MaxTokens: &outputCap,
		// The adapter's own retry is switched off. Retrying inside the SDK
		// would send billable requests this repository never counted and could
		// not classify.
		MaxRetries: &noRetries,
		HTTPClient: p.httpClient(held, key),
	})
	if err != nil {
		return nil, fmt.Errorf("openai: building the model: %w", err)
	}

	// Tools travel with the call. A request that omits them leaves the model
	// unable to ask for anything, which looks like a model that chose not to.
	var opts []einomodel.Option
	if specs := toolSpecs(req.Tools); len(specs) > 0 {
		opts = append(opts, einomodel.WithTools(specs))
	}

	reader, err := chat.Stream(ctx, messages, opts...)
	if err != nil {
		// A refusal was classified at the transport, where the status and the
		// provider's own code still existed. Preferring it keeps a caller
		// branching on a value rather than on the adapter's prose.
		if refused := held.refusal(); refused != nil {
			return nil, refused
		}
		return nil, fmt.Errorf("openai: starting the stream: %w", err)
	}

	out := make(chan ai.StreamEvent)
	go func() {
		defer close(out)
		defer reader.Close()

		// Cancellation reaches a blocked read through the request's context:
		// the transport aborts the body, the read fails, and this notices. That
		// is the RoundTripper contract rather than something this code can
		// enforce, so a transport that ignores the context would hold this
		// goroutine open — which is worth knowing about the transport rather
		// than papering over here.
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

	// Seeded empty: the model that served a reply comes from the reply. Seeding
	// it with the configured name would report a model nobody confirmed, and a
	// substitution would be invisible.
	acc := ai.NewAccumulator("")
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
			// A caller who stopped waiting gets an abort, not an error about
			// the provider: the reply was cut short by them, and reporting it
			// as a failure invites a retry of what they just cancelled.
			if ctxErr := ctx.Err(); ctxErr != nil {
				cancelled = true
				return
			}
			if refused := held.refusal(); refused != nil {
				fail(refused)
				return
			}
			fail(fmt.Errorf("openai: reading the stream: %w", err))
			return
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			cancelled = true
			return
		}
		for _, block := range chunk.ContentBlocks {
			kind, text, call, ok := describe(block)
			if !ok {
				// A block this package does not handle. Reporting it is the
				// point: silently dropping content would leave a caller with a
				// reply that is missing something nobody mentioned.
				fail(fmt.Errorf("openai: unsupported content block %q", block.Type))
				return
			}
			// Checked HERE, as the block arrives. Validating at the end would
			// let renumbered content reach the consumer first, and a consumer
			// cannot unsee what it has already been given.
			if err := checkAnnounced(held); err != nil {
				fail(err)
				return
			}
			at, seen := blocks[providerIndex(block)]
			if !seen {
				// A block ends before the next begins, so a consumer rendering
				// as it goes is never told a block is still growing when it is
				// not. Tool-call blocks are the exception: a provider may
				// interleave their fragments, and closing one when another
				// appears would leave its remaining arguments nowhere to land.
				for open, openKind := range kinds {
					// Tool-call blocks stay open only for each other: a
					// provider may interleave their fragments. Any other kind
					// beginning means every open block has finished.
					if openKind == ai.BlockToolCall && kind == ai.BlockToolCall {
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
	reason, statusErr := failureFromStatus(final.Status, final.IncompleteReason, final.ErrorCode)
	if statusErr != nil {
		fail(statusErr)
		return
	}
	// Checked before the reply is completed: the accumulator has one ending, and
	// a reply already declared finished cannot then be reported as a failure.
	if f := p.overflow(reason, usageFrom(final)); f != nil {
		ev, accErr := acc.Fail(ai.StopError, f)
		if accErr != nil {
			return
		}
		if ev.Final != nil {
			ev.Final.Usage = usageFrom(final)
			if final.Model != "" {
				ev.Final.Model = final.Model
			}
		}
		sendTerminal(ev)
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
// one this package handles.
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

// toolSpecs converts this repository's tool descriptions for the adapter.
func toolSpecs(specs []ai.ToolSpec) []*schema.ToolInfo {
	out := make([]*schema.ToolInfo, 0, len(specs))
	for _, spec := range specs {
		out = append(out, &schema.ToolInfo{Name: spec.Name, Desc: spec.Description})
	}
	return out
}

// checkAnnounced reports a stream whose own indices skip.
//
// The adapter renumbers items and their content contiguously from zero whatever
// the provider sent, so a gap is invisible after conversion, and accepting one
// would report an order the provider never sent.
func checkAnnounced(held *capture) error {
	for at, announced := range held.announcedIndices() {
		if announced != at {
			return fmt.Errorf(
				"openai: the provider announced item index %d where %d was expected; "+
					"refusing to renumber a stream that skips", announced, at)
		}
	}
	for item, announced := range held.announcedContent() {
		for at, index := range announced {
			if index != at {
				return fmt.Errorf(
					"openai: item %d announced content index %d where %d was expected; "+
						"refusing to renumber a stream that skips", item, index, at)
			}
		}
	}
	return nil
}
