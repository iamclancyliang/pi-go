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

	// Observers receive the event stream.
	Observers []events.Observer

	// Now overrides the clock, for deterministic traces in tests.
	Now func() time.Time
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
	batch := newToolBatch(a.emitter, a.cfg.Session)

	observed := &observingPort{
		inner:         a.cfg.Model,
		emitter:       a.emitter,
		session:       a.cfg.Session,
		modelName:     a.cfg.ModelName,
		batch:         batch,
		sequentialFor: a.sequentialFor,
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
		Model:       newEinoChatModel(observed, a.cfg.ModelName),
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
				a.cfg.Session.Append(ai.Message{
					Role:    fromEinoRole(item.Role),
					Content: item.Content,
				})
			}

			// The input handed to eino is pi-go's PROJECTION of
			// session truth, not eino's own accumulated history.
			// This is what makes steering survivable: eino
			// truncates at a safe point and starts a new execution,
			// so continuity has to come from truth we hold.
			proj := a.cfg.Session.Project()
			return &adk.GenInputResult[*schema.Message, *schema.Message]{
				Input:    &adk.TypedAgentInput[*schema.Message]{Messages: toEinoMessages(proj.Messages)},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(_ context.Context, _ *adk.TurnLoop[*schema.Message, *schema.Message], _ []*schema.Message) (adk.TypedAgent[*schema.Message], error) {
			return agent, nil
		},
		OnAgentEvents: func(_ context.Context, tc *adk.TurnContext[*schema.Message, *schema.Message], evs *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.Message]]) error {
			for {
				if _, ok := evs.Next(); !ok {
					break
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
			a.emitter.emit(events.KindTurnEnd, func(e *events.Event) {
				e.Detail.Reason = reason
			})
			return nil
		},
	})
	return loop, nil
}

// observingPort emits model_request / model_response and records the
// assistant's reply as session truth.
type observingPort struct {
	inner         ai.Port
	emitter       *emitter
	session       *session.Session
	modelName     string
	batch         *toolBatch
	sequentialFor func(name string) bool
}

func (o *observingPort) Generate(ctx context.Context, req ai.Request) (ai.Response, error) {
	requested := req.Model
	if requested == "" {
		requested = o.modelName
	}

	o.emitter.emit(events.KindModelRequest, func(e *events.Event) {
		e.Detail.MessageCount = len(req.Messages)
		e.Detail.Model = requested
	})

	resp, err := o.inner.Generate(ctx, req)
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

	o.session.Append(ai.Message{
		Role:      ai.RoleAssistant,
		Content:   resp.Content,
		ToolCalls: resp.ToolCalls,
	})

	// The round opens HERE, where the calls are still in the order the model
	// asked for them and before the tools node has dispatched any of them.
	// Source order is not recoverable later: once execution starts, the only
	// order anything can observe is the order things happened to finish.
	if len(resp.ToolCalls) > 0 && o.batch != nil {
		o.batch.register(resp.ToolCalls, o.sequentialFor)
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
