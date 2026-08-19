package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// Recovery is what a previous process left unfinished.
//
// A process can die between a tool acting and the record of what it did. The
// attempt is on record, so the outcome is UNKNOWN — not absent, which is the
// reading that repeats a destructive action.
type Recovery struct {
	// Awaiting holds the attempts whose tool declared a repeat harmless. They are
	// NOT repeated here.
	//
	// Repeating them automatically would bet safety on every tool author having
	// marked that declaration correctly, and the two mistakes are not symmetric:
	// "did not do it" is visible and can be retried, while "did it twice" may
	// already have changed the user's files and cannot be seen or undone.
	Awaiting []session.ToolIntent
}

// ErrAwaitingRecovery reports that an unfinished call is still waiting for an
// answer.
var ErrAwaitingRecovery = errors.New(
	"runtime: an unfinished tool call is waiting for a decision")

// ErrAlreadySettled reports that a call has an outcome, so there is nothing to
// decide about it.
var ErrAlreadySettled = errors.New("runtime: this call already has an outcome")

// Recover resolves what a previous process left unfinished, and reports what
// still needs an answer.
//
// A call that may not be repeated offers no decision: nothing may run it again,
// so it is settled as an unknown outcome and the model is told. A call that may be
// repeated is returned and left alone — whoever owns the effects of running it a
// second time owns that choice, and it is not one to make on their behalf.
func (a *Agent) Recover(ctx context.Context) (Recovery, error) {
	awaiting, err := session.RecoverUnsettled(ctx, a.cfg.Session, a.cfg.Tools.Declaration)
	if err != nil {
		return Recovery{}, err
	}
	return Recovery{Awaiting: awaiting}, nil
}

// Repeat runs an unfinished call again and settles it with what it produced.
//
// The declared terms are checked AGAIN, against the tool registered now. An answer
// can be given at any time after the question was asked, and a tool that was
// swapped in between is not the tool the attempt agreed to repeat.
func (a *Agent) Repeat(ctx context.Context, intent session.ToolIntent) error {
	// Asked by the slot the attempt reserved, not by its call id: a later
	// operation may reuse a call id, and an outcome belonging to a different
	// attempt would answer for this one.
	if _, settled := a.cfg.Session.Settlement(intent.ResultID); settled {
		return fmt.Errorf("%w: %s", ErrAlreadySettled, intent.ResultID)
	}

	policy, version, known := a.cfg.Tools.Declaration(intent.Tool)
	if !known {
		return fmt.Errorf("runtime: %s is no longer registered, so nothing can "+
			"repeat this call", intent.Tool)
	}
	// The same rule recovery used to ask the question: both sides must say safe,
	// and the version must match. Asked once and answered later, the terms could
	// have changed in between.
	if policy != tools.ReplaySafe || intent.Replay != tools.ReplaySafe ||
		version != intent.ToolVersion {
		return fmt.Errorf("runtime: %s no longer offers the terms this call was "+
			"attempted under, so repeating it is not the same act", intent.Tool)
	}

	registered, _ := a.cfg.Tools.Lookup(intent.Tool)
	decision := a.cfg.Policy.Before(ctx, PolicyCall{
		ToolCallID: intent.CallID,
		ToolName:   intent.Tool,
		Args:       intent.Args,
		Execution:  registered.Execution(),
	})
	if decision.Denied {
		// The repeat did not happen, and the original outcome is still unknown.
		// Settling it here would report the refusal as the answer to a question
		// about a call that ran before the refusal existed.
		return fmt.Errorf("runtime: repeating %s was denied: %s",
			intent.Tool, decision.Reason)
	}

	// A repeat is real work, so it is announced like any other call. Left out of
	// the stream, a recovered run would show results nothing was seen to produce.
	a.emitter.emit(events.KindToolStart, func(e *events.Event) {
		e.ToolCallID = intent.CallID
		e.ToolName = intent.Tool
		e.Detail.Args = intent.Args
	})
	result, err := registered.Call(ctx, intent.Args)
	content := result.Content
	if err != nil {
		content = fmt.Sprintf("error: %v", err)
	}
	a.emitter.emit(events.KindToolEnd, func(e *events.Event) {
		e.ToolCallID = intent.CallID
		e.ToolName = intent.Tool
		if err != nil {
			e.Detail.Err = err.Error()
		}
	})

	if err := a.cfg.Session.Settle(session.ToolSettlement{
		CallID:    intent.CallID,
		ResultID:  intent.ResultID,
		Result:    content,
		Terminate: result.Terminate,
	}, ai.Message{
		Role:       ai.RoleTool,
		Content:    content,
		ToolCallID: intent.CallID,
	}); err != nil {
		return err
	}
	a.emitter.emit(events.KindToolResult, func(e *events.Event) {
		e.ToolCallID = intent.CallID
		e.ToolName = intent.Tool
		e.Detail.Result = content
	})
	return nil
}

// Abandon settles an unfinished call without running it again.
//
// The model is told the outcome is unknown, which is the honest answer: declining
// to repeat says nothing about whether the first attempt took effect.
func (a *Agent) Abandon(intent session.ToolIntent) error {
	if _, settled := a.cfg.Session.Settlement(intent.ResultID); settled {
		return fmt.Errorf("%w: %s", ErrAlreadySettled, intent.ResultID)
	}
	return session.SettleAsUnknown(a.cfg.Session, intent)
}
