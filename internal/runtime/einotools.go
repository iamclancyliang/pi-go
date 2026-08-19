package runtime

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/iamclancyliang/pi-go/internal/tools"
)

// sequentialFor reports whether a named tool declares it cannot run concurrently.
//
// Asked per call, so the answer applies to the batch that contains it. Asking it
// of the whole registry made one sequential tool serialise every batch for the
// life of the process, including batches that never call it.
func (a *Agent) sequentialFor(name string) bool {
	for _, t := range a.cfg.Tools.All() {
		if t.Name() == name {
			return t.Execution().Sequential
		}
	}
	return false
}

// einoTools adapts the registry to the tool type eino executes.
func (a *Agent) einoTools(batch *toolBatch) ([]tool.BaseTool, error) {
	registered := a.cfg.Tools.All()
	out := make([]tool.BaseTool, 0, len(registered))
	for _, t := range registered {
		out = append(out, &observedTool{inner: t, batch: batch})
	}
	return out, nil
}

// observedTool hands a registered tool to the round that owns its ordering.
//
// The policy check, the events and the session record all belong to the round
// rather than to the tool: a tool author cannot forget them, a third-party tool
// cannot skip the policy check by not calling it, and a refusal can be decided
// before any call in the round has run.
type observedTool struct {
	inner tools.Tool
	batch *toolBatch
}

// Info implements tool.BaseTool.
func (t *observedTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.inner.Name(),
		Desc: t.inner.Description(),
	}, nil
}

// InvokableRun implements tool.InvokableTool.
func (t *observedTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	callID := toolCallIDFrom(ctx)

	// The round decides when this call may run, announces it, and decides
	// whether it may run at all; a tool cannot know where it sits in a round.
	if refusal, refused := t.batch.begin(ctx, callID); refused {
		// Already ended by the round, at the point the refusal was decided.
		// A denial is a RESULT, not an error: the model must see that the
		// call was refused and why, or it will retry forever.
		return refusal, nil
	}

	result, err := t.inner.Call(ctx, args)
	if err != nil {
		// A failing tool is an observable outcome. The failure text is
		// returned to the model rather than propagated as a Go error,
		// because aborting the run would make a recoverable tool error
		// indistinguishable from a harness crash.
		msg := fmt.Sprintf("error: %v", err)
		t.batch.finish(callID, msg, err)
		return msg, nil
	}

	t.batch.finish(callID, result, nil)
	return result, nil
}

var _ tool.InvokableTool = (*observedTool)(nil)

// toolCallIDFrom recovers the originating tool call ID from the context.
//
// The ID is what pairs tool_start/tool_end with the model's request, and what
// session truth uses to decide whether a call is settled. Proving that pairing
// by position or by role shape is exactly the mistake the spike work kept
// making, so it is read from eino's call context rather than inferred.
func toolCallIDFrom(ctx context.Context) string {
	return compose.GetToolCallID(ctx)
}

// prepareCall applies the policy seam before a call may run.
//
// It happens in the round rather than inside the tool so a refusal can end the
// call at the point it was decided: before the calls after it are announced. Run
// from inside the tool it could only end after every call had already started,
// which is a different, and wrong, thing to show a reader of the stream.
func (a *Agent) prepareCall(ctx context.Context, name, callID, args string) (string, bool) {
	registered, ok := a.cfg.Tools.Lookup(name)
	if !ok {
		// An unknown tool is refused rather than dispatched: the framework
		// would otherwise fail it later, out of the round's ordering.
		return fmt.Sprintf("denied: no tool named %s", name), true
	}
	decision := a.cfg.Policy.Before(ctx, PolicyCall{
		ToolCallID: callID,
		ToolName:   name,
		Args:       args,
		Execution:  registered.Execution(),
	})
	if !decision.Denied {
		return "", false
	}
	return fmt.Sprintf("denied: %s", decision.Reason), true
}
