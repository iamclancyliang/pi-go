package runtime

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// sequentialRequired reports whether any registered tool declares that it
// cannot run concurrently.
//
// Registered rather than per-batch because eino decides sequencing at the tools
// node, which is built once. See the ExecuteSequentially comment in loop.go for
// why the coarser answer is the safe one.
func (a *Agent) sequentialRequired() bool {
	for _, t := range a.cfg.Tools.All() {
		if t.Execution().Sequential {
			return true
		}
	}
	return false
}

// einoTools adapts the registry to the tool type eino executes.
func (a *Agent) einoTools() ([]tool.BaseTool, error) {
	registered := a.cfg.Tools.All()
	out := make([]tool.BaseTool, 0, len(registered))
	for _, t := range registered {
		out = append(out, &observedTool{
			inner:   t,
			emitter: a.emitter,
			session: a.cfg.Session,
			policy:  a.cfg.Policy,
		})
	}
	return out, nil
}

// observedTool wraps a pi-go tool with the policy seam, event emission and
// session recording.
//
// All three happen here rather than inside the tools themselves so that a tool
// author cannot forget them — and so a third-party tool cannot skip the policy
// check by not calling it.
type observedTool struct {
	inner   tools.Tool
	emitter *emitter
	session *session.Session
	policy  Policy
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

	t.emitter.emit(events.KindToolStart, func(e *events.Event) {
		e.ToolCallID = callID
		e.ToolName = t.inner.Name()
		e.Detail.Args = args
	})

	decision := t.policy.Before(ctx, PolicyCall{
		ToolCallID: callID,
		ToolName:   t.inner.Name(),
		Args:       args,
		Execution:  t.inner.Execution(),
	})
	if decision.Denied {
		// A denial is a RESULT, not an error: the model must see that
		// the call was refused and why, or it will retry forever. It is
		// also recorded as truth, so the denial survives in history.
		msg := fmt.Sprintf("denied: %s", decision.Reason)
		t.finish(callID, msg, nil)
		return msg, nil
	}

	result, err := t.inner.Call(ctx, args)
	if err != nil {
		// A failing tool is an observable outcome. The failure text is
		// returned to the model rather than propagated as a Go error,
		// because aborting the run would make a recoverable tool error
		// indistinguishable from a harness crash.
		msg := fmt.Sprintf("error: %v", err)
		t.finish(callID, msg, err)
		return msg, nil
	}

	t.finish(callID, result, nil)
	return result, nil
}

// finish records the tool result as truth and emits tool_end.
//
// Truth is appended BEFORE the event is emitted so that any observer reacting
// to tool_end already sees a consistent session.
func (t *observedTool) finish(callID, result string, err error) {
	t.session.Append(ai.Message{
		Role:       ai.RoleTool,
		Content:    result,
		ToolCallID: callID,
	})
	t.emitter.emit(events.KindToolEnd, func(e *events.Event) {
		e.ToolCallID = callID
		e.ToolName = t.inner.Name()
		e.Detail.Result = result
		if err != nil {
			e.Detail.Err = err.Error()
		}
	})
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
