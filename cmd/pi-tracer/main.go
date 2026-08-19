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
	"github.com/iamclancyliang/pi-go/internal/render"
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

	fmt.Print(render.Trace(rec.Events()))
	fmt.Print(render.Session(sess.Snapshot()))
	fmt.Print(render.Capabilities(agent.Capabilities()))
	return nil
}
