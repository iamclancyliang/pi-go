package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/eino-contrib/jsonschema"

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
//
// The argument schema crosses here as the JSON Schema document the tool built,
// not as a framework type the tool constructed: a tool that named eino's schema
// types would put the framework in every tool author's imports, which is the
// dependency this boundary exists to prevent.
func (t *observedTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	info := &schema.ToolInfo{
		Name: t.inner.Name(),
		Desc: t.inner.Description(),
	}
	doc, err := t.inner.Parameters().JSON()
	if err != nil {
		return nil, fmt.Errorf("runtime: tool %q has an unusable parameter schema: %w", t.inner.Name(), err)
	}
	if doc == nil {
		// A tool that takes no arguments says so by carrying no schema. An
		// empty object would instead tell the model there is a shape to fill.
		return info, nil
	}
	parsed := &jsonschema.Schema{}
	if err := json.Unmarshal(doc, parsed); err != nil {
		return nil, fmt.Errorf("runtime: tool %q produced a schema that is not JSON Schema: %w", t.inner.Name(), err)
	}
	info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(parsed)
	return info, nil
}

// InvokableRun implements tool.InvokableTool.
func (t *observedTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	callID := toolCallIDFrom(ctx)

	// The round decides when this call may run, announces it, and decides
	// whether it may run at all; a tool cannot know where it sits in a round.
	switch decided := t.batch.begin(ctx, callID); {
	case decided.Dropped:
		// The round was cut before this call. It must not run: a tool that
		// ignores cancellation would otherwise still do its work, and the only
		// sign would be its side effects, since the round reports nothing for
		// a call it cut.
		return "", context.Canceled
	case decided.Settled:
		// Already ended by the round, at the point the refusal was decided.
		// A denial is a RESULT, not an error: the model must see that the
		// call was refused and why, or it will retry forever.
		return decided.Refusal, nil
	}

	result, err := t.inner.Call(ctx, args)
	if err != nil && ctx.Err() != nil {
		// The round was cut, not the tool. A cut call produces no result:
		// reporting one would tell the model an attempt was made and reported
		// on, when it was abandoned.
		t.batch.drop(callID)
		return "", err
	}
	if err != nil {
		// A failing tool is an observable outcome. The failure text is
		// returned to the model rather than propagated as a Go error,
		// because aborting the run would make a recoverable tool error
		// indistinguishable from a harness crash.
		msg := fmt.Sprintf("error: %v", err)
		t.batch.finish(callID, msg, false, err)
		return msg, nil
	}

	t.batch.finish(callID, result.Content, result.Terminate, nil)
	return result.Content, nil
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
