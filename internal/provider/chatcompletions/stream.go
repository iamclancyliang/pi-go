// The streaming half of a chat-completions port.
//
// Shared for the same reason the capture and the conversion are: of the 423
// lines this began as, ONE named the provider it was written for — the call
// that builds the adapter's model. Everything else is what this repository
// promises about any reply: blocks opened and closed in order, tool calls whose
// positions the provider chose, a terminal frame carrying usage and the served
// model, and a refusal classified where the status still existed.
//
// A port supplies a Dialect and keeps its own vocabulary. It supplies nothing
// else.
package chatcompletions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// Stream answers as the reply arrives.
func (p *Port) Stream(ctx context.Context, req ai.Request) (<-chan ai.StreamEvent, error) {
	if req.Model == "" {
		// No catalog exists here to consult and no default is invented: a
		// request naming no model would otherwise reach whichever model the
		// configuration happened to hold, and the reply would not say which.
		return nil, p.fail(ai.FailureRefused, 0, "no model named for this request")
	}
	if req.Model != p.cfg.Model {
		// This port serves one model. Serving another because a request asked
		// for it would answer from a model the caller's configuration never
		// chose, and the reply carries no sign of the substitution.
		return nil, p.fail(ai.FailureRefused, 0, fmt.Sprintf(
			"this port serves %q; the request named %q", p.cfg.Model, req.Model))
	}
	messages, err := ToMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	key := p.cfg.Credential.Key()

	// This call's own record, held by the client it is about to use.
	held := NewTransport(p.cfg.Transport, p.cfg.Classifier, key)
	outputCap := p.cfg.MaxOutputTokens
	chat, err := p.cfg.NewModel(ctx, ModelRequest{
		APIKey:  key,
		BaseURL: p.cfg.BaseURL,
		Model:   req.Model,
		// The cap has to reach the request. Requiring it at construction says
		// nothing about what was sent, and a reply with no cap is a bill nobody
		// chose.
		MaxOutputTokens: outputCap,
		HTTPClient:      HTTPClient(held),
	})
	if err != nil {
		return nil, p.wireFailure("building the model", key, err)
	}

	var model einomodel.BaseChatModel = chat
	// Tools travel with the call. A request that omits them leaves the model
	// unable to ask for anything, which looks like a model that chose not to.
	if specs := ToolSpecs(req.Tools); len(specs) > 0 {
		bound, bindErr := chat.WithTools(specs)
		if bindErr != nil {
			return nil, p.wireFailure("binding tools", key, bindErr)
		}
		model = bound
	}

	reader, err := model.Stream(ctx, messages)
	if err != nil {
		// A refusal was classified at the transport, where the status and the
		// provider's own code still existed. Preferring it keeps a caller
		// branching on a value rather than on the adapter's prose.
		if refused := held.Capture.Refusal(); refused != nil {
			return nil, refused
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, p.wireFailure("starting the stream", key, err)
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
// Text and reasoning arrive as two fields that may alternate, so a boundary
// between them is a change of field. Tool calls carry the provider's own
// position, which is what keeps the fragments of one call together; that
// position is checked against the wire separately — see checkAnnounced —
// because the adapter is trusted to carry it, not to be the record of it.
func (p *Port) pump(ctx context.Context, reader *schema.StreamReader[*schema.Message],
	held *Transport, out chan<- ai.StreamEvent) {

	// Seeded empty: the model that served a reply comes from the reply. Seeding
	// it with the configured name would report a model nobody confirmed, and a
	// substitution would be invisible.
	acc := ai.NewAccumulator("")
	calls := map[int]int{}          // provider tool-call index -> block index
	kinds := map[int]ai.BlockKind{} // block index -> kind, while it is open
	next := 0

	cancelled := false
	// Why a stopped call stopped. Usually the caller's own context, but a
	// transport can report a stop it was told about before that context is
	// observably done — and then ctx.Err() is nil while the call is over all
	// the same, so the reason has to be carried rather than asked for later.
	var stoppedBy error

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
	// A TERMINAL event may not. It is the only one the stream will ever
	// produce, and abandoning it loses exactly the event that tells the
	// consumer how the reply ended.
	sendTerminal := func(ev ai.StreamEvent) { out <- ev }

	abort := func(err error) {
		ev, accErr := acc.Fail(ai.StopError, err)
		if accErr != nil {
			return
		}
		if ev.Final != nil {
			ev.Final.Usage = UsageOf(held.Capture.Last())
			if served := held.Capture.Last().Model; served != "" {
				ev.Final.Model = served
			}
		}
		sendTerminal(ev)
	}

	// A stopped stream still ends with a Terminal carrying what had already
	// arrived. A consumer that watched a reply appear should not have it vanish
	// because it stopped, and a channel that simply closes says nothing about
	// what they have.
	defer func() {
		if !cancelled {
			return
		}
		cause := stoppedBy
		if cause == nil {
			cause = ctx.Err()
		}
		ev, accErr := acc.Fail(ai.StopAborted, cause)
		if accErr != nil {
			return
		}
		if ev.Final != nil {
			ev.Final.Usage = UsageOf(held.Capture.Last())
		}
		sendTerminal(ev)
	}()

	startEv, err := acc.Begin()
	if err != nil {
		return
	}
	if !send(startEv) {
		abort(context.Canceled)
		return
	}

	// closeOthers ends every open block except the ones that may still receive
	// fragments. A block ends before the next begins, so a consumer rendering
	// as it goes is never told a block is still growing when it is not.
	closeOthers := func(keep func(int, ai.BlockKind) bool) bool {
		open := make([]int, 0, len(kinds))
		for at := range kinds {
			open = append(open, at)
		}
		sort.Ints(open)
		for _, at := range open {
			if keep(at, kinds[at]) {
				continue
			}
			closed, err := acc.Close(at)
			if err != nil {
				abort(err)
				return false
			}
			delete(kinds, at)
			if !send(closed) {
				return false
			}
		}
		return true
	}

	for {
		chunk, err := reader.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				cancelled = true
				return
			}
			// A stop reported through the error rather than through the context
			// ends the same way. Ending it as a failure would put a reply in the
			// record as one the provider broke.
			if ai.Stopped(err) {
				cancelled, stoppedBy = true, err
				return
			}
			if refused := held.Capture.Refusal(); refused != nil {
				abort(refused)
				return
			}
			abort(p.wireFailure("reading the stream", p.cfg.Credential.Key(), err))
			return
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			cancelled = true
			return
		}

		// Checked HERE, as each chunk arrives. Validating at the end would let
		// content whose position the provider never gave reach the consumer
		// first, and a consumer cannot unsee what it has been given.
		if err := p.checkAnnounced(held); err != nil {
			abort(err)
			return
		}

		if chunk.ReasoningContent != "" {
			at, ok := openBlock(kinds, ai.BlockThinking)
			if !ok {
				if !closeOthers(func(int, ai.BlockKind) bool { return false }) {
					return
				}
				at = next
				next++
				kinds[at] = ai.BlockThinking
			}
			events, err := acc.Push(ai.Chunk{
				Index: at, Kind: ai.BlockThinking, Delta: chunk.ReasoningContent})
			if err != nil {
				abort(err)
				return
			}
			if !send(events...) {
				return
			}
		}

		if chunk.Content != "" {
			at, ok := openBlock(kinds, ai.BlockText)
			if !ok {
				if !closeOthers(func(int, ai.BlockKind) bool { return false }) {
					return
				}
				at = next
				next++
				kinds[at] = ai.BlockText
			}
			events, err := acc.Push(ai.Chunk{Index: at, Kind: ai.BlockText, Delta: chunk.Content})
			if err != nil {
				abort(err)
				return
			}
			if !send(events...) {
				return
			}
		}

		for _, tc := range chunk.ToolCalls {
			if tc.Index == nil {
				// Unreachable while the wire check above holds, and kept
				// because the two answer for different sources: that one says
				// the provider sent a position, this one says the conversion
				// still has it. Without it a conversion that dropped one would
				// be a nil dereference rather than a report.
				abort(p.fail(ai.FailureUnknown, 0,
					"a tool call fragment reached this port with no position"))
				return
			}
			at, seen := calls[*tc.Index]
			if !seen {
				// Tool-call blocks stay open for each other: a provider may
				// interleave their fragments, and closing one when another
				// appears would leave its remaining arguments nowhere to land.
				if !closeOthers(func(_ int, k ai.BlockKind) bool { return k == ai.BlockToolCall }) {
					return
				}
				at = next
				next++
				calls[*tc.Index] = at
				kinds[at] = ai.BlockToolCall
			}
			// The arguments travel as the delta, and the identity separately:
			// the accumulator appends every delta, so passing the same
			// fragment as both would write it twice.
			events, err := acc.Push(ai.Chunk{
				Index: at,
				Kind:  ai.BlockToolCall,
				Delta: tc.Function.Arguments,
				Call:  ai.ToolCall{ID: tc.ID, Name: tc.Function.Name},
			})
			if err != nil {
				abort(err)
				return
			}
			if !send(events...) {
				return
			}
		}
	}

	// Checked again once the stream has ended. The check inside the loop only
	// runs when a chunk arrives, so an Announcement carrying nothing at all
	// would otherwise reach the end unexamined.
	if err := p.checkAnnounced(held); err != nil {
		abort(err)
		return
	}

	if !closeOthers(func(int, ai.BlockKind) bool { return false }) {
		return
	}

	// The ending comes from what the provider said, captured before the adapter
	// reinterpreted it.
	final := held.Capture.Last()
	reason, endErr := p.endingFrom(final.FinishReason, final.ErrorCode)
	if endErr != nil {
		abort(endErr)
		return
	}
	// Checked before the reply is completed: the accumulator has one ending, and
	// a reply already declared finished cannot then be reported as a failure.
	if f := p.overflow(reason, UsageOf(final)); f != nil {
		ev, accErr := acc.Fail(ai.StopError, f)
		if accErr != nil {
			return
		}
		if ev.Final != nil {
			ev.Final.Usage = UsageOf(final)
			if final.Model != "" {
				ev.Final.Model = final.Model
			}
		}
		sendTerminal(ev)
		return
	}
	done, err := acc.Done(reason, UsageOf(final))
	if err != nil {
		abort(err)
		return
	}
	if done.Final != nil && final.Model != "" {
		done.Final.Model = final.Model
	}
	sendTerminal(done)
}

// openBlock finds the open block of a kind, if one is open.
func openBlock(kinds map[int]ai.BlockKind, kind ai.BlockKind) (int, bool) {
	for at, k := range kinds {
		if k == kind {
			return at, true
		}
	}
	return 0, false
}

// checkAnnounced reports a stream whose own tool-call positions do not hold.
//
// The adapter carries the provider's position, so this could be read from the
// converted chunks — but then the check would be asking the same source it is
// checking. These come from the provider's own bytes, upstream of any
// conversion, so a conversion that started renumbering would be caught rather
// than believed.
func (p *Port) checkAnnounced(held *Transport) error {
	for _, what := range held.Capture.AnonymousFragments() {
		return p.fail(ai.FailureUnknown, 0, fmt.Sprintf(
			"the provider sent %s; refusing to infer a position it did not send", what))
	}
	opened := map[int]string{}
	highest := -1
	for _, a := range held.Capture.Announcements() {
		if a.Named {
			if id, already := opened[a.Index]; already {
				// A position that opens twice describes two calls in one place.
				// Accepting it would merge them, and the arguments of the first
				// would end up on the second.
				return p.fail(ai.FailureUnknown, 0, fmt.Sprintf(
					"the provider opened position %d twice, as %q and %q", a.Index, id, a.ID))
			}
			if a.Index != highest+1 {
				// Positions that skip describe calls that were never sent.
				return p.fail(ai.FailureUnknown, 0, fmt.Sprintf(
					"the provider opened position %d where %d was expected; "+
						"refusing to renumber a stream that skips", a.Index, highest+1))
			}
			highest = a.Index
			opened[a.Index] = a.ID
			continue
		}
		if _, known := opened[a.Index]; !known {
			// A continuation of a call that was never opened has nothing to
			// continue, and guessing which one it meant is guessing.
			return p.fail(ai.FailureUnknown, 0, fmt.Sprintf(
				"the provider continued position %d, which it never opened", a.Index))
		}
	}
	return nil
}

// endingFrom maps a chat-completions finish reason onto a reply ending.
//
// The finish reasons are the dialect's and are the same everywhere; the error
// codes are the provider's, so those go through the classifier first. A failure
// reported inside a 200 that named its own reason must not be reclassified by
// the ending: an exhausted balance called an interruption reads as "try again
// later" for something that cannot succeed.
func (p *Port) endingFrom(finish, errorCode string) (ai.StopReason, error) {
	if errorCode != "" {
		if failed, ok := p.cfg.Classifier.TerminalFailure(errorCode); ok {
			return ai.StopError, failed
		}
		return ai.StopError, p.fail(ai.FailureUnknown, 0, "the reply failed: "+errorCode)
	}

	switch finish {
	case "stop":
		return ai.StopEnd, nil
	case "tool_calls", "function_call":
		return ai.StopToolUse, nil
	case "length":
		return ai.StopLength, nil
	case "content_filter":
		return ai.StopError, p.fail(ai.FailureRefused, 0, "the provider's filters removed the content")
	case "":
		// The stream ended without the provider saying why, so the reply is not
		// known to be complete. Reporting it as finished would hand back a
		// partial answer as the model's last word.
		return ai.StopError, p.fail(ai.FailureUnknown, 0, "the stream ended without a finish reason")
	default:
		return ai.StopError, p.fail(ai.FailureUnknown, 0,
			fmt.Sprintf("unrecognised finish reason %q", finish))
	}
}
