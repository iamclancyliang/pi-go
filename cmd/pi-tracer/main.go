// Command pi-tracer runs the v0 tracer bullet and prints its event trace and
// session snapshot.
//
// It is a composition root: it assembles modules and carries no behaviour of
// its own. Everything it prints comes from the runtime's published event
// stream, so what a developer reads here is what a client would see.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "pi-tracer: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		prompt  = flag.String("prompt", "Read README.md and list the files.", "prompt to submit")
		asJSON  = flag.Bool("json", false, "emit the trace as JSON instead of a table")
		timeout = flag.Duration("timeout", 30*time.Second, "overall timeout")
	)
	flag.Parse()

	registry, _, _ := tools.NewFixtureRegistry()
	sess := session.New("You are pi-go.")

	// The fake model is scripted: v0 is a contract tracer bullet, not a
	// provider integration. A real provider goes behind the same model port
	// without the runtime noticing.
	model := &ai.Scripted{
		Name: "fake-1",
		Replies: []ai.Response{
			ai.AssistantToolCalls(
				ai.ToolCall{ID: "call-1", Name: "file_read", Args: `{"path":"README.md"}`},
				ai.ToolCall{ID: "call-2", Name: "list_files", Args: `{}`},
			),
		},
		StopWhenToolsSettled: true,
		Final:                ai.AssistantText("I read two files."),
	}

	rec := runtime.NewRecorder()
	agent, err := runtime.New(runtime.Config{
		Model:     model,
		ModelName: "fake-1",
		Tools:     registry,
		Session:   sess,
		Policy:    runtime.DenyWrites,
		Observers: []events.Observer{rec},
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	if err := agent.Run(ctx, *prompt); err != nil {
		return err
	}

	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(struct {
			Events       []events.Event       `json:"events"`
			Session      session.Snapshot     `json:"session"`
			Capabilities runtime.Capabilities `json:"capabilities"`
		}{rec.Events(), sess.Snapshot(), agent.Capabilities()})
	}

	printTrace(rec.Events())
	printSession(sess.Snapshot())
	printCapabilities(agent.Capabilities())
	return nil
}

func printTrace(evs []events.Event) {
	fmt.Println("=== EVENT TRACE ===")
	fmt.Printf("%-4s %-6s %-16s %s\n", "seq", "turn", "kind", "detail")
	for _, e := range evs {
		fmt.Printf("%-4d %-6d %-16s %s\n", e.Seq, e.TurnIndex, e.Kind, describe(e))
	}
}

func describe(e events.Event) string {
	switch e.Kind {
	case events.KindModelRequest:
		return fmt.Sprintf("model=%s messages=%d", e.Detail.Model, e.Detail.MessageCount)
	case events.KindModelResponse:
		if len(e.Detail.ToolCallIDs) > 0 {
			return fmt.Sprintf("model=%s toolCalls=%v", e.Detail.Model, e.Detail.ToolCallIDs)
		}
		return fmt.Sprintf("model=%s text=%q", e.Detail.Model, e.Detail.Text)
	case events.KindModelChanged:
		return fmt.Sprintf("%s -> %s", e.Detail.From, e.Detail.To)
	case events.KindToolStart:
		return fmt.Sprintf("%s id=%s args=%s", e.ToolName, e.ToolCallID, e.Detail.Args)
	case events.KindToolEnd:
		if e.Detail.Err != "" {
			return fmt.Sprintf("%s id=%s err=%s", e.ToolName, e.ToolCallID, e.Detail.Err)
		}
		return fmt.Sprintf("%s id=%s result=%q", e.ToolName, e.ToolCallID, truncate(e.Detail.Result, 40))
	case events.KindTurnEnd, events.KindAgentEnd:
		if e.Detail.Err != "" {
			return fmt.Sprintf("reason=%s err=%s", e.Detail.Reason, e.Detail.Err)
		}
		return fmt.Sprintf("reason=%s", e.Detail.Reason)
	default:
		return ""
	}
}

func printSession(s session.Snapshot) {
	fmt.Println("\n=== SESSION TRUTH ===")
	fmt.Printf("system: %q\n", s.System)
	for i, m := range s.Messages {
		line := fmt.Sprintf("%2d %-9s %q", i, m.Role, truncate(m.Content, 48))
		for _, tc := range m.ToolCalls {
			line += fmt.Sprintf(" [call %s %s %s]", tc.ID, tc.Name, tc.Args)
		}
		if m.ToolCallID != "" {
			line += fmt.Sprintf(" [result for %s]", m.ToolCallID)
		}
		fmt.Println(line)
	}
	if len(s.Unmatched) > 0 {
		fmt.Printf("unmatched tool calls: %v\n", s.Unmatched)
	} else {
		fmt.Println("unmatched tool calls: none")
	}
}

func printCapabilities(c runtime.Capabilities) {
	fmt.Println("\n=== DECLARED HOST CAPABILITIES ===")
	fmt.Printf("streaming=%v durableStorage=%v toolDenial=%v extensionTransport=%q\n",
		c.Streaming, c.DurableStorage, c.ToolDenial, c.ExtensionTransport)
}

func truncate(s string, n int) string {
	s = string([]rune(s))
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
