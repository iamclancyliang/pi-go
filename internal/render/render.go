// Package render turns runtime values into text for a human to read.
//
// It is separate from the command so the formatting can be tested directly. A
// renderer living in `main` is only reachable by running the binary and reading
// its output, so the case that silently produces an empty line is exactly the one
// nobody notices.
package render

import (
	"fmt"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
)

// Event describes one event, and reports whether the kind was recognised.
//
// The bool is what makes the completeness check possible: a kind that falls
// through renders as an empty detail, which reads as "this event carried no
// information" rather than "nobody taught the renderer about it".
func Event(e events.Event) (string, bool) {
	switch e.Kind {
	case events.KindAgentStart, events.KindTurnStart:
		// These carry no detail of their own; the line is the event.
		return "", true
	case events.KindModelRequest:
		return fmt.Sprintf("model=%s messages=%d", e.Detail.Model, e.Detail.MessageCount), true
	case events.KindModelResponse:
		if len(e.Detail.ToolCallIDs) > 0 {
			return fmt.Sprintf("model=%s toolCalls=%v", e.Detail.Model, e.Detail.ToolCallIDs), true
		}
		return fmt.Sprintf("model=%s text=%q", e.Detail.Model, e.Detail.Text), true
	case events.KindModelChanged:
		return fmt.Sprintf("%s -> %s", e.Detail.From, e.Detail.To), true
	case events.KindToolStart:
		return fmt.Sprintf("%s id=%s args=%s", e.ToolName, e.ToolCallID, e.Detail.Args), true
	case events.KindToolEnd:
		if e.Detail.Err != "" {
			return fmt.Sprintf("%s id=%s err=%s", e.ToolName, e.ToolCallID, e.Detail.Err), true
		}
		return fmt.Sprintf("%s id=%s result=%q", e.ToolName, e.ToolCallID,
			Truncate(e.Detail.Result, 40)), true
	case events.KindToolResult:
		// Distinguished from the end on purpose: the end says the call
		// finished, this says the result became history.
		return fmt.Sprintf("%s id=%s recorded=%q", e.ToolName, e.ToolCallID,
			Truncate(e.Detail.Result, 40)), true
	case events.KindTurnEnd, events.KindAgentEnd:
		if e.Detail.Err != "" {
			return fmt.Sprintf("reason=%s err=%s", e.Detail.Reason, e.Detail.Err), true
		}
		return fmt.Sprintf("reason=%s", e.Detail.Reason), true
	default:
		return "", false
	}
}

// Trace renders a whole event stream, header included.
func Trace(evs []events.Event) string {
	var b strings.Builder
	b.WriteString("=== EVENT TRACE ===\n")
	fmt.Fprintf(&b, "%-4s %-6s %-16s %s\n", "seq", "turn", "kind", "detail")
	for _, e := range evs {
		detail, _ := Event(e)
		fmt.Fprintf(&b, "%-4d %-6d %-16s %s\n", e.Seq, e.TurnIndex, e.Kind, detail)
	}
	return b.String()
}

// Session renders session truth as the transcript it is.
func Session(s session.Snapshot) string {
	var b strings.Builder
	b.WriteString("\n=== SESSION TRUTH ===\n")
	fmt.Fprintf(&b, "system: %q\n", s.System)
	for i, m := range s.Messages {
		fmt.Fprintf(&b, "%2d %-9s %q", i, m.Role, Truncate(m.Content, 48))
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, " [call %s %s %s]", tc.ID, tc.Name, tc.Args)
		}
		if m.ToolCallID != "" {
			fmt.Fprintf(&b, " [result for %s]", m.ToolCallID)
		}
		b.WriteString("\n")
	}
	if len(s.Unmatched) > 0 {
		fmt.Fprintf(&b, "unmatched tool calls: %v\n", s.Unmatched)
	} else {
		b.WriteString("unmatched tool calls: none\n")
	}
	return b.String()
}

// Capabilities renders what the host declares it can do.
func Capabilities(c runtime.Capabilities) string {
	return fmt.Sprintf("\n=== DECLARED HOST CAPABILITIES ===\n"+
		"streaming=%v durableStorage=%v toolDenial=%v extensionTransport=%q\n",
		c.Streaming, c.DurableStorage, c.ToolDenial, c.ExtensionTransport)
}

// Truncate shortens text for display, counting characters rather than bytes.
func Truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
