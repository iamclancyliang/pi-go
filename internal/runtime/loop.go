// Package runtime is pi-go's agent loop and the owner of its observable
// contracts.
//
// It builds on eino's prebuilt `adk.TurnLoop`. That dependency is an
// implementation detail of this package: eino types never leave it, and models
// are reached solely through pi-go's own model port. Replacing the framework
// therefore stays a change to this package rather than to its callers.
//
// What eino provides: loop orchestration, cancellation, safe points,
// checkpointing.
// What pi-go keeps: session truth, the model port, and the observable event
// contract — including events eino does not emit at all, notably model_changed.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// idleSettleWindow is how long the loop must be idle before it is considered
// finished.
//
// eino's TurnLoop is long-running: Run is non-blocking and the loop waits for
// more input rather than exiting after one turn. Stop(UntilIdleFor) is the
// deterministic exit. Without it Wait blocks forever — which, in the spike
// work, hung CI.
const idleSettleWindow = 50 * time.Millisecond

// Config assembles a runtime.
type Config struct {
	// Model is the model port. Required.
	Model ai.Port

	// ModelName labels the model in events.
	ModelName string

	// Tools is the tool registration seam. Required, may be empty.
	Tools *tools.Registry

	// Session holds conversational truth. Required.
	Session *session.Session

	// Policy is the pre-execution policy seam. Defaults to AllowAll.
	Policy Policy

	// ReplyObservers receive each reply as it arrives, block by block.
	//
	// Nothing else exposes the block structure: the framework is handed a
	// flattened view and the run event stream deliberately carries lifecycle
	// only, so a renderer that wants to show a reply forming subscribes here.
	ReplyObservers []ReplyObserver

	// Observers receive the event stream.
	Observers []events.Observer

	// Now overrides the clock, for deterministic traces in tests.
	Now func() time.Time

	// Summarize shortens the conversation when the model refuses a request for
	// exceeding its context. Optional; without it an overflow is terminal.
	//
	// It is given durable truth and returns what should stand in for it: a
	// summary, and the messages to keep verbatim.
	Summarize func(ctx context.Context, truth []ai.Message) (summary string, retainedTail []ai.Message, err error)

	// PrepareNextTurn runs after each turn and may change what the turns AFTER
	// it use. Optional.
	//
	// It never applies to the turn that just ran: a turn's model is the one it
	// was executed with, and changing that afterwards would describe a
	// conversation that did not happen.
	PrepareNextTurn func(ctx context.Context) NextTurn
}

// NextTurn is what one turn may change for the turns that follow it.
type NextTurn struct {
	// ModelName selects the model from the next turn onward. Empty leaves it
	// unchanged.
	ModelName string
}

// Agent runs prompts through the loop.
type Agent struct {
	cfg     Config
	emitter *emitter
	caps    Capabilities
	state   *StateNamespace
}

// New builds an Agent.
func New(cfg Config) (*Agent, error) {
	switch {
	case cfg.Model == nil:
		return nil, errors.New("runtime: Config.Model is required")
	case cfg.Tools == nil:
		return nil, errors.New("runtime: Config.Tools is required")
	case cfg.Session == nil:
		return nil, errors.New("runtime: Config.Session is required")
	}
	if cfg.Policy == nil {
		cfg.Policy = AllowAll
	}
	return &Agent{
		cfg:     cfg,
		emitter: newEmitter(cfg.Now, cfg.Observers...),
		caps:    V0Capabilities(),
		state:   NewStateNamespace(),
	}, nil
}

// Capabilities reports what this host provides (host capability discovery seam).
func (a *Agent) Capabilities() Capabilities { return a.caps }

// State returns the per-extension state namespace seam.
func (a *Agent) State() *StateNamespace { return a.state }

// Run submits one prompt and returns when the resulting work has settled.
//
// It is Start followed immediately by Wait, for callers with nothing to submit
// mid-run.
func (a *Agent) Run(ctx context.Context, prompt string) error {
	r, err := a.Start(ctx, prompt)
	if err != nil {
		return err
	}
	return r.Wait()
}

// Run is a started, still-live agent run.
//
// It exists because steering and follow-up are defined by WHEN a message
// arrives relative to work already in flight. A one-shot call cannot
// express "while a tool round is active", so it cannot test the contract
// either.
type Run struct {
	agent *Agent
	loop  *adk.TurnLoop[*schema.Message, *schema.Message]
	ctx   context.Context
}

// Start submits a prompt and returns while the run is still in flight.
func (a *Agent) Start(ctx context.Context, prompt string) (*Run, error) {
	// An unfinished call from a previous process is answered BEFORE anything is
	// asked. The conversation holds a tool call with no result, and a model shown
	// that either waits for an answer that is not coming or is invited to act as
	// though the call never happened — which is the reading that repeats an
	// effect. See Recover.
	if waiting := a.cfg.Session.UnsettledIntents(); len(waiting) > 0 {
		err := fmt.Errorf("%w: %s", ErrAwaitingRecovery, waiting[0].Tool)
		a.failStart(err)
		return nil, err
	}

	a.emitter.emit(events.KindAgentStart, nil)

	loop, err := a.buildLoop(ctx)
	if err != nil {
		a.failStart(err)
		return nil, err
	}

	if ok, _ := loop.Push(schema.UserMessage(prompt)); !ok {
		err := errors.New("runtime: loop rejected the prompt")
		a.failStart(err)
		return nil, err
	}

	loop.Run(ctx)
	return &Run{agent: a, loop: loop, ctx: ctx}, nil
}

func (a *Agent) failStart(err error) {
	a.emitter.emit(events.KindAgentEnd, func(e *events.Event) {
		e.Detail.Reason = "error"
		e.Detail.Err = err.Error()
	})
}

// Follow queues a follow-up message.
//
// A plain Push. eino buffers it and hands it to the NEXT GenInput iteration, so
// the in-flight turn finishes untouched and the message is consumed only once
// the current run would otherwise stop.
func (r *Run) Follow(text string) error {
	if ok, _ := r.loop.Push(schema.UserMessage(text)); !ok {
		return errors.New("runtime: loop rejected the follow-up")
	}
	return nil
}

// Steer injects a message into the work already in flight.
//
// Push with WithPreempt(AfterToolCalls): eino lets the current tool-call round
// finish, then truncates at that safe point and starts a NEW execution.
//
// Continuity is NOT provided by eino. The new execution is seeded from pi-go's
// projection of session truth in GenInput — that is the whole reason the
// runtime holds truth itself. Without it the same preempt yields a context
// containing only the system and steering messages, and every completed tool
// result is gone.
func (r *Run) Steer(text string) error {
	ok, resolved := r.loop.Push(
		schema.UserMessage(text),
		adk.WithPreempt[*schema.Message, *schema.Message](adk.AfterToolCalls),
	)
	if !ok {
		return errors.New("runtime: loop rejected the steering message")
	}

	// Wait until the preempt request has actually been resolved.
	//
	// Accepting the push only means the message was buffered; the preempt
	// signal may not have been observed yet. Returning at that point races the
	// turn: if the tool finishes first, the turn completes normally and the
	// steering silently becomes an ordinary follow-up — the user asked to
	// redirect work in flight and instead got a queued message, with nothing
	// reporting the difference.
	//
	// The original enqueues a steering message synchronously before its call
	// returns, so a caller that has been told the steer was accepted can rely
	// on it applying at the next safe point. Waiting here reproduces that
	// guarantee rather than inventing a stronger or weaker one: it does not
	// wait for the steering to be *delivered to the model*, only for the
	// interruption to be registered.
	if resolved != nil {
		select {
		case <-resolved:
		case <-r.ctx.Done():
			return r.ctx.Err()
		}
	}
	return nil
}

// Wait settles the run and emits agent_end.
func (r *Run) Wait() error {
	// eino's TurnLoop is long-running: it waits for more input rather than
	// exiting after a turn. UntilIdleFor is the deterministic exit; without
	// it Wait blocks forever.
	r.loop.Stop(adk.UntilIdleFor(idleSettleWindow))
	exit := r.loop.Wait()

	// ExitReason is an error, and nil means a clean exit — including the
	// normal path where our GenInput called Stop() with nothing buffered.
	reason := "stop"
	var exitErr error
	if exit != nil && exit.ExitReason != nil {
		reason = "error"
		exitErr = exit.ExitReason
	}
	// A stop the tools asked for is a normal ending. The framework ends the run
	// by cancelling it, so without reading the cause back a deliberate stop is
	// reported as a broken run — the caller sees an error for the one outcome
	// that was requested.
	if (exit != nil && exit.StopCause == stopCauseToolTerminate) ||
		errors.Is(exitErr, errToolTerminate) {
		reason = stopCauseToolTerminate
		exitErr = nil
	}
	// Cancellation outranks a reported error: when the context is done, the
	// error is usually just the cancellation surfacing, and reporting it as
	// "error" would hide a deliberate abort.
	if err := r.ctx.Err(); err != nil {
		reason = "aborted"
		exitErr = err
	}

	r.agent.emitter.emit(events.KindAgentEnd, func(e *events.Event) {
		e.Detail.Reason = reason
		if exitErr != nil {
			e.Detail.Err = exitErr.Error()
		}
	})
	return exitErr
}

// buildLoop assembles the eino agent and TurnLoop.
func (a *Agent) buildLoop(ctx context.Context) (*adk.TurnLoop[*schema.Message, *schema.Message], error) {
	// The model port is wrapped in an observing decorator BEFORE it is
	// adapted to eino. Emission therefore belongs to the runtime, and the
	// ai package stays free of event concerns — the event observation seam
	// is owned here.
	// One coordinator for this loop. Each model response opens a new round in
	// it, so the order of a round is decided in one place instead of inside the
	// tools, which cannot see each other.
	batch := newToolBatch(a.emitter, a.cfg.Session, a.prepareCall,
		a.cfg.Tools.Declaration)

	observed := &observingPort{
		inner:          a.cfg.Model,
		replyObservers: a.cfg.ReplyObservers,
		emitter:        a.emitter,
		session:        a.cfg.Session,
		modelName:      a.cfg.ModelName,
		batch:          batch,
		sequentialFor:  a.sequentialFor,
		summarize:      a.cfg.Summarize,
	}

	einoTools, err := a.einoTools(batch)
	if err != nil {
		return nil, err
	}

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "pi-go",
		Description: "pi-go v0 tracer bullet agent",
		// No instruction is given to the framework. The session projection
		// already carries the system message, and it is what pi-go feeds in on
		// every request; passing it here as well puts it in the context twice,
		// which is a change to the prompt the model actually sees.
		Instruction: "",
		Model:       newEinoChatModel(observed, observed.currentModel),
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: einoTools,
				// The node always runs calls concurrently; pi-go's own
				// coordinator serialises a round when that round contains
				// a tool that declared it cannot tolerate concurrency.
				//
				// This setting is fixed when the agent is built, so using it
				// to express a PER-CALL contract forces one answer for the
				// whole process: a single sequential tool anywhere in the
				// registry then serialises every batch, including batches
				// that never call it.
				ExecuteSequentially: false,
			},
		},
		// Handlers is intentionally empty at v0.
		//
		// ORDER IS A CORRECTNESS CONSTRAINT, NOT STYLE. Measured in
		// TestWrapModelCompositionOrder: eino composes WrapModel
		// handlers lazily, outermost-first in registration order, and a
		// handler that SUBSTITUTES the model never calls through — so
		// every handler registered after it is never invoked, with no
		// error and no trace. pi's per-turn model selection is exactly
		// such a substitution. When model selection is added it must be
		// registered LAST (innermost), and this list must stay the
		// single place that order is decided.
		Handlers: nil,
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: building agent: %w", err)
	}

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[*schema.Message, *schema.Message]{
		GenInput: func(_ context.Context, l *adk.TurnLoop[*schema.Message, *schema.Message], items []*schema.Message) (*adk.GenInputResult[*schema.Message, *schema.Message], error) {
			if len(items) == 0 {
				// Nothing buffered: the conversation is done for
				// now. Stopping here is what makes Run return
				// rather than idling.
				l.Stop()
				return nil, nil
			}
			a.emitter.beginTurn()
			a.emitter.emit(events.KindTurnStart, nil)

			// Consumed items become truth HERE, at the point they
			// enter the conversation — not when they were pushed.
			//
			// This ordering matters for follow-up: a message pushed
			// mid-turn that was recorded immediately would sit in
			// history between an assistant's tool calls and their
			// results, which is chronologically true but structurally
			// wrong. Recording at consumption keeps truth readable
			// as a conversation.
			for _, item := range items {
				if item == nil {
					continue
				}
				// A message that could not be recorded must not enter the
				// conversation. Continuing would run a turn whose input is
				// absent from history, so a restart would resume from a
				// conversation that never had it.
				if err := a.cfg.Session.Append(ai.Message{
					Role:    fromEinoRole(item.Role),
					Content: item.Content,
				}); err != nil {
					return nil, err
				}
			}

			// The input handed to eino is pi-go's PROJECTION of
			// session truth, not eino's own accumulated history.
			// This is what makes steering survivable: eino
			// truncates at a safe point and starts a new execution,
			// so continuity has to come from truth we hold.
			proj := a.cfg.Session.Project()
			_, streams := a.cfg.Model.(ai.StreamingPort)
			return &adk.GenInputResult[*schema.Message, *schema.Message]{
				Input: &adk.TypedAgentInput[*schema.Message]{
					Messages: toEinoMessages(proj.Messages),
					// Asked for only when the provider can actually do it. A
					// runtime that requests streaming from a model that answers
					// in one piece gets one chunk and learns nothing, while one
					// that never requests it leaves the whole path unreachable.
					EnableStreaming: streams,
				},
				RunOpts: []adk.AgentRunOption{
					// Fires after a round of tool calls completes and BEFORE
					// the next model call. That is the only point where a
					// round can end the conversation without a further call:
					// the results are already recorded, and the graph has not
					// yet reached the model.
					adk.WithAfterToolCallsHook(func(context.Context) error {
						if !batch.ShouldTerminate() {
							return nil
						}
						// TWO SIGNALS, TWO JOBS. Neither replaces the other.
						//
						// The returned error stops the ROUND, synchronously, at
						// the one point before the graph would proceed to the
						// model. Without it the round goes on to make that next
						// model call: Stop() is delivered asynchronously and on
						// its own races it, and sometimes loses.
						//
						// Stop() ends the LOOP. Without it the loop stays open
						// until it has been idle for the settle window, so a run
						// that was asked to stop holds its caller for that long
						// after the decision was taken.
						l.Stop(adk.WithImmediate(), adk.WithStopCause(stopCauseToolTerminate))
						return errToolTerminate
					}),
				},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(_ context.Context, _ *adk.TurnLoop[*schema.Message, *schema.Message], _ []*schema.Message) (adk.TypedAgent[*schema.Message], error) {
			return agent, nil
		},
		OnAgentEvents: func(_ context.Context, tc *adk.TurnContext[*schema.Message, *schema.Message], evs *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.Message]]) error {
			// The FIRST failure decides the turn. Draining without looking
			// leaves a failed turn indistinguishable from a successful one,
			// so the loop goes on to the next turn and consumes whatever was
			// queued while this one was failing.
			var failure error
			for {
				event, ok := evs.Next()
				if !ok {
					break
				}
				if event != nil && event.Err != nil && failure == nil {
					failure = event.Err
				}
			}
			// Stopped is a channel closed only when a Stop actually
			// cancelled this turn — not whenever Stop was called.
			// A non-blocking receive is the documented way to read
			// it; treating the channel itself as truthy would report
			// every turn as stopped.
			reason := "stop"
			if tc != nil && tc.Stopped != nil {
				select {
				case <-tc.Stopped:
					reason = "stopped"
				default:
				}
			}
			// A result that could not be recorded ends the turn: the model was
			// told the call happened, so continuing would build on a history
			// that disagrees with what the model was shown.
			if failure == nil {
				failure = batch.recordingFailure()
			}
			failure = unwrapOwn(failure)

			// A round that asked to stop is a normal ending, not a failure.
			// The framework reports it by cancelling the turn, which is
			// indistinguishable from any other cancellation unless the cause
			// is read back: reported as an error, a deliberate stop would
			// surface as a broken run and would stop the agent for the wrong
			// stated reason.
			if (tc != nil && tc.StopCause() == stopCauseToolTerminate) ||
				errors.Is(failure, errToolTerminate) {
				reason = stopCauseToolTerminate
				failure = nil
			} else if failure != nil {
				reason = "error"
			}
			a.emitter.emit(events.KindTurnEnd, func(e *events.Event) {
				e.Detail.Reason = reason
				if failure != nil {
					e.Detail.Err = failure.Error()
				}
			})

			// The model for the FOLLOWING turns is chosen here, once this turn
			// is closed out. A change is ANNOUNCED, because the framework
			// reports none: without an event of pi-go's own, a run that
			// switched models halfway looks identical to one that did not, and
			// nothing afterwards can explain why the later answers differ.
			if failure == nil && a.cfg.PrepareNextTurn != nil {
				next := a.cfg.PrepareNextTurn(ctx)
				if from, to, changed := observed.selectModel(next.ModelName); changed {
					a.emitter.emit(events.KindModelChanged, func(e *events.Event) {
						e.Detail.From = from
						e.Detail.To = to
					})
				}
			}

			// A turn that failed or was cut short ends the agent WITHOUT
			// looking for anything queued behind it. A message sent while a
			// turn was failing is not consumed: the queue does not always
			// drain, and a caller that assumes it does acts on that message
			// a turn later than the sender believes, or not at all.
			if failure != nil {
				return failure
			}
			return nil
		},
	})
	return loop, nil
}

// ownError marks an error pi-go produced itself.
//
// eino wraps whatever a node returns in an error of its own, whose message
// carries framework internals: an error-type tag and the path of the node that
// failed. That text reaching a caller contradicts what this package promises —
// eino is an implementation detail, and replacing it must not be visible
// outside.
//
// A tag rather than text matching, because recovering the original by parsing the
// wrapper's message would depend on a format eino never promised and would break
// silently when it changes. Unwrap keeps the chain intact, so a caller's
// errors.Is still sees everything it saw before.
type ownError struct{ err error }

func (o ownError) Error() string { return o.err.Error() }
func (o ownError) Unwrap() error { return o.err }

// unwrapOwn recovers the error pi-go produced from whatever wrapped it.
//
// Applied ONCE, where a turn decides what failed. Everything a caller can see
// comes from there — the turn's own event, the run's exit reason, and the error
// returned — so cleaning it again downstream would add a second site that
// changes no answer, leaving neither testable.
func unwrapOwn(err error) error {
	var own ownError
	if errors.As(err, &own) {
		return own.err
	}
	return err
}

// stopCauseToolTerminate marks a stop the tools asked for, so it can be told
// apart from a cancellation or a failure when the turn is closed out.
const stopCauseToolTerminate = "tool_terminate"

// errToolTerminate cuts the round at the hook, which is the only synchronous
// point before the next model call. It is pi-go's own signal and never reaches a
// caller: it is normalised where the turn and the run are closed out.
var errToolTerminate = errors.New("runtime: the round asked to stop")

// observingPort emits model_request / model_response and records the
// assistant's reply as session truth.
type observingPort struct {
	mu             sync.Mutex
	inner          ai.Port
	replyObservers []ReplyObserver
	emitter        *emitter
	session        *session.Session
	modelName      string
	batch          *toolBatch
	sequentialFor  func(name string) bool
	summarize      func(ctx context.Context, truth []ai.Message) (string, []ai.Message, error)

	// failed is why recovery could not be attempted, when that is a different
	// failure from the one being recovered from.
	failedMu sync.Mutex
	failed   error
}

// recoverFromOverflow compacts once and asks again, or gives up.
//
// The budget is one attempt per user input, counted durably. Asking again without
// shortening the context would resend what was just refused, and a second refusal
// is the operation failing — not an empty answer, which reads to a caller as the
// model having nothing to say.
func (o *observingPort) recoverFromOverflow(ctx context.Context, req ai.Request, spent ai.Usage, cause error) (ai.Response, error) {
	o.emitter.emit(events.KindModelResponse, func(e *events.Event) {
		e.Detail.Err = cause.Error()
	})

	retry, err := o.shortenForRetry(ctx, req, cause, spent)
	if err != nil {
		return ai.Response{}, err
	}
	if retry == nil {
		return ai.Response{}, failureError(o.session.Failure(), cause)
	}
	return o.Generate(ctx, *retry)
}

// selectModel changes the model used from the next request onward, and reports
// whether that was a change.
func (o *observingPort) selectModel(name string) (from, to string, changed bool) {
	if name == "" {
		return "", "", false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if name == o.modelName {
		return "", "", false
	}
	from, o.modelName = o.modelName, name
	return from, name, true
}

func (o *observingPort) currentModel() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.modelName
}

// CodeContextOverflow is the terminal state of an input that stayed too large
// even after the context was shortened.
const CodeContextOverflow = "context_overflow_after_compaction"

// OutcomeStatus is how an operation stands when it is reopened.
type OutcomeStatus string

const (
	// OutcomeOpen means nothing terminal is on record. The operation has not
	// settled, and it moves forward only when a caller submits input.
	OutcomeOpen OutcomeStatus = "open"

	// OutcomeFailed means the operation ended and cannot be retried as it
	// stands. Reopening it returns this same result every time.
	OutcomeFailed OutcomeStatus = "failed"
)

// Outcome is an operation's result, readable without running the operation.
//
// A terminal state reachable only by re-running the work is not a result: the
// caller pays for the model call and the compaction again just to be told what
// the session already recorded. Failure carries the recorded reason and is nil
// unless Status is OutcomeFailed.
type Outcome struct {
	Status  OutcomeStatus
	Failure *session.OperationFailure
}

// Failed reports whether the operation is terminally failed.
func (o Outcome) Failed() bool { return o.Status == OutcomeFailed }

// Err renders a failed outcome as an error, or nil when there is none.
//
// It is the same error a call would have raised, so the two ways of learning
// the operation failed cannot report it differently.
func (o Outcome) Err() error {
	if o.Failure == nil {
		return nil
	}
	return failureError(o.Failure, nil)
}

// Reopen returns the operation's outcome without submitting input.
//
// This is what makes a recorded terminal state worth recording: after a restart
// the caller reads the result already reached instead of asking the model the
// same question again. It calls neither the model nor the compactor, and it
// changes nothing.
//
// Submitting input is a different act. It starts a new operation, which clears
// the terminal state and the budget the previous input earned.
func (a *Agent) Reopen() Outcome {
	if failure := a.cfg.Session.Failure(); failure != nil {
		return Outcome{Status: OutcomeFailed, Failure: failure}
	}
	return Outcome{Status: OutcomeOpen}
}

// failureError renders a recorded failure as an error.
//
// Every path that reports a failure comes through here — the one that raises it
// as it happens and the one that reads it back after a restart — so the two
// cannot describe the same failure differently.
//
// cause is what actually went wrong, when the caller still has it: the provider's
// own error at the moment of refusal carries detail the durable record does not
// keep. A caller reading the failure back later has no cause to offer, so the
// class is recovered from the code instead, which is what keeps the wrapped
// sentinel the same either way.
func failureError(failure *session.OperationFailure, cause error) error {
	if cause == nil && failure.Code == CodeContextOverflow {
		cause = ai.ErrContextOverflow
	}
	if cause == nil {
		return fmt.Errorf("runtime: %s: %s", failure.Code, failure.Detail)
	}
	return fmt.Errorf("runtime: %s: %s: %w", failure.Code, failure.Detail, cause)
}

func (o *observingPort) Generate(ctx context.Context, req ai.Request) (ai.Response, error) {
	// An input that already failed terminally is not asked again. Reopening a
	// conversation and retrying it would spend the same money to reach the same
	// conclusion, and the caller would see a fresh failure rather than the one
	// that was already recorded.
	if failure := o.session.Failure(); failure != nil {
		return ai.Response{}, failureError(failure, nil)
	}

	requested := req.Model
	if requested == "" {
		requested = o.currentModel()
	}

	o.emitter.emit(events.KindModelRequest, func(e *events.Event) {
		e.Detail.MessageCount = len(req.Messages)
		e.Detail.Model = requested
	})

	resp, err := o.inner.Generate(ctx, req)
	if errors.Is(err, ai.ErrContextOverflow) {
		// The refused call still reports what it cost, and it was still billed.
		return o.recoverFromOverflow(ctx, req, resp.Usage, err)
	}
	if err != nil {
		o.emitter.emit(events.KindModelResponse, func(e *events.Event) {
			e.Detail.Model = requested
			e.Detail.Err = err.Error()
		})
		return ai.Response{}, err
	}

	// eino executes a model swap but never interprets it, so if the model
	// that served the call differs from the one requested, pi-go is the
	// only thing that can say so.
	if served := resp.Model; served != "" && served != requested {
		o.emitter.emit(events.KindModelChanged, func(e *events.Event) {
			e.Detail.From = requested
			e.Detail.To = served
		})
	}

	ids := make([]string, 0, len(resp.ToolCalls))
	for _, tc := range resp.ToolCalls {
		ids = append(ids, tc.ID)
	}
	o.emitter.emit(events.KindModelResponse, func(e *events.Event) {
		e.Detail.Model = resp.Model
		e.Detail.Text = resp.Content
		e.Detail.ToolCallIDs = ids
	})

	// The reply is recorded before it is acted on. A reply that was answered
	// but never recorded leaves the tool calls that follow it referring to a
	// request that history does not contain.
	if err := o.session.Append(ai.Message{
		Role:      ai.RoleAssistant,
		Content:   resp.Content,
		ToolCalls: resp.ToolCalls,
	}); err != nil {
		return ai.Response{}, err
	}

	// The round opens HERE, where the calls are still in the order the model
	// asked for them and before the tools node has dispatched any of them.
	// Source order is not recoverable later: once execution starts, the only
	// order anything can observe is the order things happened to finish.
	if len(resp.ToolCalls) > 0 && o.batch != nil {
		o.batch.register(ctx, resp.ToolCalls, o.sequentialFor, resp.Truncated)
	}
	return resp, nil
}

func toEinoMessages(in []ai.Message) []*schema.Message {
	out := make([]*schema.Message, 0, len(in))
	for _, m := range in {
		msg := &schema.Message{
			Role:       toEinoRole(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
				ID:       tc.ID,
				Function: schema.FunctionCall{Name: tc.Name, Arguments: tc.Args},
			})
		}
		out = append(out, msg)
	}
	return out
}

func toEinoRole(r ai.Role) schema.RoleType {
	switch r {
	case ai.RoleSystem:
		return schema.System
	case ai.RoleUser:
		return schema.User
	case ai.RoleAssistant:
		return schema.Assistant
	case ai.RoleTool:
		return schema.Tool
	default:
		return schema.RoleType(r)
	}
}

// Stream delivers a reply as it arrives, with the same guards the whole-answer
// path applies.
//
// Without this the observing port would not satisfy ai.StreamingPort, the model
// adapter's type assertion would fail, and every reply would quietly fall back to
// arriving in one piece — streaming would be implemented and never reached.
func (o *observingPort) Stream(ctx context.Context, req ai.Request) (<-chan ai.StreamEvent, error) {
	// An input that already failed terminally is not asked again, streamed or
	// otherwise. Reopening it would spend the same money to reach the same
	// conclusion.
	if failure := o.session.Failure(); failure != nil {
		return nil, failureError(failure, nil)
	}

	streaming, ok := o.inner.(ai.StreamingPort)
	if !ok {
		return nil, errStreamUnsupported
	}

	requested := req.Model
	if requested == "" {
		requested = o.currentModel()
	}
	o.emitter.emit(events.KindModelRequest, func(e *events.Event) {
		e.Detail.MessageCount = len(req.Messages)
		e.Detail.Model = requested
	})

	events0, err := streaming.Stream(ctx, req)
	if err != nil {
		o.emitter.emit(events.KindModelResponse, func(e *events.Event) {
			e.Detail.Model = requested
			e.Detail.Err = err.Error()
		})
		// A reply that failed before it began is still a reply that ended. An
		// observer told nothing waits for an end that is not coming, and cannot
		// tell this from a stream still being set up.
		o.publishReply(ctx, preStartFailure(err))
		return nil, err
	}

	out := make(chan ai.StreamEvent)
	go func() {
		defer close(out)
		o.pump(ctx, req, requested, events0, out, false)
	}()
	return out, nil
}

// pump forwards one attempt, and may replace it with a shortened retry.
//
// delivered says whether any content has already reached the consumer. It is the
// whole reason recovery is conditional here: see the overflow branch.
func (o *observingPort) pump(ctx context.Context, req ai.Request, requested string,
	in <-chan ai.StreamEvent, out chan<- ai.StreamEvent, delivered bool) {

	// Iterative rather than recursive. A retry replaces the attempt rather than
	// nesting inside it, so the recovery budget is what limits attempts and
	// nothing depends on stack depth to stop.
	announced := false
	for {
		var retried <-chan ai.StreamEvent

		for event := range in {
			// ONE START PER REPLY. A retry is another attempt at the same reply,
			// not a second reply: the consumer was told once that this reply
			// began, and telling it again would describe two.
			if event.Kind == ai.StreamStart {
				if announced {
					continue
				}
				announced = true
			}

			if event.Kind == ai.StreamError && event.Final != nil &&
				errors.Is(event.Final.Cause, ai.ErrContextOverflow) {

				// RECOVERY IS ONLY SAFE BEFORE ANYTHING WAS SHOWN.
				//
				// A retry is a second attempt at the same reply. If the first
				// attempt already delivered blocks, the consumer has them, and
				// the retry's blocks would arrive after them as though one reply
				// had said both things. The whole-answer path never has to
				// decide this, because nothing is visible until it is complete.
				if !delivered {
					if next, ok := o.reopen(ctx, req, requested, event); ok {
						retried = next
						break
					}
					if cause := o.recoveryFailure(); cause != nil {
						event = unrecordable(event, cause)
					}
				}
				o.publishReply(ctx, event)
				forward(ctx, out, event)
				o.recordTerminal(ctx, event, requested)
				return
			}

			o.publishReply(ctx, event)
			if event.Terminal() {
				// Recorded BEFORE the ending crosses: the framework acts on the
				// calls the ending carries, and it must not act before the round
				// that governs them has been opened.
				//
				// If the record failed, the ending crosses as a FAILURE. Passing
				// it on as a success would have the framework run calls from a
				// reply history does not contain, leaving results for a request
				// nothing asked.
				if err := o.observeTerminal(ctx, event, requested); err != nil {
					if o.batch != nil {
						o.batch.recordStreamFailure(err)
					}
					event = unrecordable(event, err)
				}
				forward(ctx, out, event)
				return
			}
			if !forward(ctx, out, event) {
				// Nobody is reading any more. The reply still has an ending, and
				// an observer that never receives one waits for an end that is
				// not coming — so the rest is drained to reach it rather than
				// abandoned here.
				o.drainToEnd(ctx, in, requested)
				return
			}
			switch {
			case event.Kind != ai.StreamStart:
				delivered = true
			}
		}

		if retried == nil {
			return
		}
		in, delivered = retried, false
	}
}

// reopen shortens the context and starts another stream, or reports that
// recovery is over.
func (o *observingPort) reopen(ctx context.Context, req ai.Request, requested string,
	failed ai.StreamEvent) (<-chan ai.StreamEvent, bool) {

	streaming, ok := o.inner.(ai.StreamingPort)
	if !ok {
		return nil, false
	}
	retry, err := o.shortenForRetry(ctx, req, failed.Final.Cause, failed.Final.Usage)
	if err != nil {
		// Recovery itself failed. Reporting the refusal it was recovering from
		// sends a reader after the context size when the shortening is what
		// broke, so the reason that surfaces is this one.
		o.failedMu.Lock()
		o.failed = err
		o.failedMu.Unlock()
		if o.batch != nil {
			o.batch.recordStreamFailure(err)
		}
		return nil, false
	}
	if retry == nil {
		return nil, false
	}

	o.emitter.emit(events.KindModelRequest, func(e *events.Event) {
		e.Detail.MessageCount = len(retry.Messages)
		e.Detail.Model = requested
	})
	next, err := streaming.Stream(ctx, *retry)
	if err != nil {
		if o.batch != nil {
			o.batch.recordStreamFailure(err)
		}
		return nil, false
	}
	return next, true
}

// shortenForRetry spends the recovery budget and reports what to send next.
//
// ONE transition for both ways of asking. The budget, the terminal failure, the
// shortening and the projection to retry from are the same decision whichever
// shape the answer arrives in; two copies would let them drift, and the one that
// drifted would be the one nobody tested.
//
// It returns the request to retry with. A nil request means recovery is over: the
// terminal failure has already been recorded, so the caller reports the original
// refusal rather than inventing a different one.
func (o *observingPort) shortenForRetry(ctx context.Context, req ai.Request,
	cause error, spent ai.Usage) (*ai.Request, error) {

	if err := o.session.RecordOverflowAttempt(cause.Error(), spent); err != nil {
		return nil, err
	}

	if o.summarize == nil || o.session.OverflowAttempts() > 1 {
		detail := "recovery already spent"
		if o.summarize == nil {
			detail = "no way to shorten the context"
		}
		// Recorded before it is reported, so reopening finds the same terminal
		// state instead of starting the same losing attempt again.
		recorded := &session.OperationFailure{Code: CodeContextOverflow, Detail: detail}
		if err := o.session.Fail(recorded.Code, recorded.Detail); err != nil {
			return nil, err
		}
		return nil, nil
	}

	summary, retained, err := o.summarize(ctx, o.session.Truth())
	if err != nil {
		return nil, fmt.Errorf("runtime: shortening the context: %w", err)
	}
	if err := o.session.Compact(summary, retained); err != nil {
		return nil, err
	}

	// Ask again from the SHORTENED projection. Reusing the request would send
	// the context that was just refused.
	retry := req
	retry.Messages = o.session.Project().Messages
	return &retry, nil
}

// errStreamUnsupported reports that the provider behind this port answers only in
// one piece.
var errStreamUnsupported = errors.New("runtime: this model does not stream")

// observeTerminal publishes what a finished stream means to the event surface.
//
// The per-block events are not republished here. They are the reply's own
// protocol, carried on the model port for whoever renders it; this stream
// describes the run, and folding thousands of deltas into it would drown the
// events a client watches for.
func (o *observingPort) observeTerminal(ctx context.Context, event ai.StreamEvent, requested string) error {
	final := event.Final
	if final == nil {
		return nil
	}

	// eino executes a model swap but never interprets it, so a reply served by a
	// model other than the one asked for is only visible if pi-go says so.
	if served := final.Model; served != "" && served != requested {
		o.emitter.emit(events.KindModelChanged, func(e *events.Event) {
			e.Detail.From = requested
			e.Detail.To = served
		})
	}

	text, calls := flatten(final)
	ids := make([]string, 0, len(calls))
	for _, c := range calls {
		ids = append(ids, c.ID)
	}
	o.emitter.emit(events.KindModelResponse, func(e *events.Event) {
		e.Detail.Model = requested
		e.Detail.Text = text
		e.Detail.ToolCallIDs = ids
		if event.Kind == ai.StreamError {
			e.Detail.Err = final.ErrorMessage
		}
	})

	// A FAILED reply is not recorded. Nothing acts on it, and writing a reply
	// the model did not finish would leave history asserting something was said.
	if event.Kind != ai.StreamDone {
		return nil
	}

	// Recorded before it is acted on, exactly as a whole answer is: tool calls
	// that follow must not refer to a request history does not contain.
	if err := o.session.Append(ai.Message{
		Role:      ai.RoleAssistant,
		Content:   text,
		ToolCalls: calls,
	}); err != nil {
		return err
	}

	// The round opens here, in the order the model asked for the calls. A
	// streamed reply has no different claim on that ordering than any other.
	//
	// A reply that stopped at the length limit is CUT SHORT, and its calls carry
	// whatever arguments had arrived when the cut fell. Cut arguments can still
	// parse — half a path is a path — so the round is opened as truncated and
	// every call it carried is refused rather than run.
	if len(calls) > 0 && o.batch != nil {
		o.batch.register(ctx, calls, o.sequentialFor, final.StopReason == ai.StopLength)
	}
	return nil
}

// flatten reduces a streamed reply to what history keeps.
//
// History has no use for block boundaries — it records what was said, and the
// block structure belongs to the reply as it arrived. Text blocks are joined in
// order; thinking is left out, because it is the model reasoning rather than
// something it said.
func flatten(m *ai.AssistantMessage) (string, []ai.ToolCall) {
	var text string
	var calls []ai.ToolCall
	for _, block := range m.Blocks {
		switch block.Kind {
		case ai.BlockText:
			text += block.Text
		case ai.BlockToolCall:
			calls = append(calls, block.Call)
		}
	}
	return text, calls
}

// recordTerminal publishes and records a finished stream, keeping any failure.
//
// The failure is carried to the turn rather than returned here: this runs on the
// forwarding goroutine, which has no caller to return to, and a reply that could
// not be recorded must end the turn rather than let the round proceed on a
// history that is missing it.
func (o *observingPort) recordTerminal(ctx context.Context, event ai.StreamEvent, requested string) {
	if err := o.observeTerminal(ctx, event, requested); err != nil && o.batch != nil {
		o.batch.recordStreamFailure(err)
	}
}

// ReplyObserver receives a reply as it arrives.
//
// Separate from events.Observer, which watches the run: lifecycle events describe
// what the agent is doing, and folding thousands of content deltas into that
// stream drowns the events a client watches for. This is the surface a renderer
// subscribes to, and without it the block structure the port produces has nowhere
// to go.
type ReplyObserver interface {
	// Reply is called for every event of every reply, in order, before the
	// stream advances.
	//
	// IT IS CALLED SYNCHRONOUSLY, and that is the contract rather than an
	// implementation detail: an observer that blocks holds up delivery to
	// everyone, including the runtime. Buffering instead would let a slow
	// observer fall arbitrarily far behind and then see a reply that finished
	// long ago, which is not "as it arrives" in any useful sense — and the
	// memory it took to pretend otherwise would be unbounded.
	//
	// An observer that cannot keep up should hand the event to its own buffer
	// and return, choosing what to drop. That decision belongs to whoever is
	// rendering, which is the only place that knows what may be skipped.
	//
	// Exactly one terminal event arrives per reply. A retry after a refused
	// attempt is the same reply continuing, not a second one.
	Reply(event ai.StreamEvent)
}

// ReplyObserverFunc adapts a function to ReplyObserver.
type ReplyObserverFunc func(ai.StreamEvent)

// Reply implements ReplyObserver.
func (f ReplyObserverFunc) Reply(e ai.StreamEvent) { f(e) }

// publishReply hands one event to every reply observer.
//
// Fed from the events the runtime forwards, not from the provider's raw stream:
// a refused attempt's terminal and the retry's start are both real events from a
// provider, and both describe a reply that is still arriving. An observer told
// about them would render one reply as two, the first of which appears to fail.
func (o *observingPort) publishReply(ctx context.Context, event ai.StreamEvent) {
	// A cancelled run stops publishing, except for the terminal. Continuing to
	// deliver content after the caller gave up wastes their time on a reply they
	// stopped; withholding the terminal too would leave an observer waiting for
	// an end that never comes, with no way to tell a cancelled reply from one
	// still arriving.
	if ctx.Err() != nil && !event.Terminal() {
		return
	}
	for _, observer := range o.replyObservers {
		observer.Reply(event)
	}
}

// forward hands one event on, and reports whether the reply is still wanted.
//
// A send that only waits for a reader waits forever when there is no longer one:
// an abandoned run leaves this goroutine blocked, and the provider's goroutine
// behind it, for the life of the process. Watching the run's own context is what
// lets both notice that nobody is listening.
func forward(ctx context.Context, out chan<- ai.StreamEvent, event ai.StreamEvent) bool {
	select {
	case out <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

// drainToEnd consumes what is left of an abandoned reply, publishing its ending.
//
// Content is dropped: the reader that would have shown it is gone. The terminal
// is not, because an observer is still owed one — and draining rather than
// walking away is also what lets the provider's goroutine finish instead of
// blocking on a send nobody will take.
func (o *observingPort) drainToEnd(ctx context.Context, in <-chan ai.StreamEvent, requested string) {
	for event := range in {
		if !event.Terminal() {
			continue
		}
		o.publishReply(ctx, event)
		o.recordTerminal(ctx, event, requested)
		return
	}
}

// unrecordable turns a reply that could not be written into a failed one.
//
// The content is kept: it did arrive, and an observer that watched it appear
// should not see it vanish. What changes is the ending, because a reply the
// record does not hold cannot be acted on.
func unrecordable(event ai.StreamEvent, cause error) ai.StreamEvent {
	failed := *event.Final
	failed.StopReason = ai.StopError
	failed.ErrorMessage = cause.Error()
	failed.Cause = cause
	event.Kind = ai.StreamError
	event.Final = &failed
	return event
}

// preStartFailure is the ending of a reply that never began.
func preStartFailure(cause error) ai.StreamEvent {
	return ai.StreamEvent{
		Kind: ai.StreamError,
		Final: &ai.AssistantMessage{
			StopReason:   ai.StopError,
			ErrorMessage: cause.Error(),
			Cause:        cause,
		},
	}
}

// recoveryFailure reports why recovery could not be attempted, if that is what
// happened, and clears it.
func (o *observingPort) recoveryFailure() error {
	o.failedMu.Lock()
	defer o.failedMu.Unlock()
	cause := o.failed
	o.failed = nil
	return cause
}
