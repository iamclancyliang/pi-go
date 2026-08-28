package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// DefaultSystemPrompt is what the agent is told it is when nothing else says.
const DefaultSystemPrompt = "You are pi-go, a coding agent. " +
	"Use the tools available to you to inspect and change files in the working directory. " +
	"Prefer the read, ls, find and grep tools over running shell commands to look around."

// Streams are where a run reads from and writes to.
//
// Passed in rather than reached for, so a test drives a whole mode without a
// terminal — and so the two output streams stay distinguishable, which is what
// makes a failing run usable in a pipeline.
type Streams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// Runtime is everything a mode needs to talk to a model.
type Runtime struct {
	Model     ai.Port
	ModelName string
	Tools     *tools.Registry
	System    string

	// Provider is shown to the user, never branched on. Which account a
	// session is spending belongs on screen: two providers can serve
	// similarly-named models, and the bill goes to only one of them.
	Provider string
}

// RunPrint sends each prompt in turn and writes the final answer.
//
// One-shot, and the exit status carries the outcome: a run whose last reply
// ended in a failure exits non-zero with the reason on stderr, because a
// pipeline reading only stdout must not mistake an error for an answer.
func RunPrint(ctx context.Context, rt Runtime, streams Streams, prompts []string) int {
	if len(prompts) == 0 {
		fmt.Fprintln(streams.Err, "pi: no prompt given")
		return 1
	}

	sess := session.New(rt.System)
	agent, err := runtime.New(runtime.Config{
		Model:     rt.Model,
		ModelName: rt.ModelName,
		Tools:     rt.Tools,
		Session:   sess,
	})
	if err != nil {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}

	for _, prompt := range prompts {
		if err := agent.Run(ctx, prompt); err != nil {
			// The reason goes to stderr and nothing goes to stdout: a caller
			// piping stdout into another program must receive an answer or
			// nothing, never an apology.
			fmt.Fprintf(streams.Err, "pi: %v\n", err)
			return 1
		}
	}

	answer, ok := lastAnswer(sess)
	if !ok {
		fmt.Fprintln(streams.Err, "pi: the model produced no answer")
		return 1
	}
	fmt.Fprintln(streams.Out, answer)
	return 0
}

// RunInteractive reads prompts a line at a time and answers each in turn.
//
// A line-based loop, NOT Pi's full-screen interface. The two are different
// features: this is the conversation, and the interface that renders it — the
// components, the key handling, the live redraw — is its own body of work that
// has not been ported. Said plainly rather than approximated, because a
// half-drawn interface is worse than an honest prompt.
func RunInteractive(ctx context.Context, rt Runtime, streams Streams) int {
	sess := session.New(rt.System)
	agent, err := runtime.New(runtime.Config{
		Model:     rt.Model,
		ModelName: rt.ModelName,
		Tools:     rt.Tools,
		Session:   sess,
	})
	if err != nil {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}

	fmt.Fprintf(streams.Out, "pi-go · %s/%s · %d tools · Ctrl-D to exit\n",
		rt.Provider, rt.ModelName, len(rt.Tools.All()))

	lines := bufio.NewScanner(streams.In)
	// A prompt can be long; the default limit would cut one mid-sentence and
	// send the fragment.
	lines.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for {
		fmt.Fprint(streams.Out, "\n> ")
		if !lines.Scan() {
			break
		}
		prompt := strings.TrimSpace(lines.Text())
		if prompt == "" {
			continue
		}
		if prompt == "/exit" || prompt == "/quit" {
			break
		}

		// The session carries across turns, so this is one conversation rather
		// than a series of unrelated questions.
		if err := agent.Run(ctx, prompt); err != nil {
			if ctx.Err() != nil {
				// The user stopped it. Ending the loop as well would exit on a
				// keystroke meant to interrupt one answer.
				fmt.Fprintln(streams.Err, "\npi: stopped")
				return 130
			}
			fmt.Fprintf(streams.Err, "pi: %v\n", err)
			continue
		}
		if answer, ok := lastAnswer(sess); ok {
			fmt.Fprintln(streams.Out, answer)
		}
	}

	if err := lines.Err(); err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}
	fmt.Fprintln(streams.Out)
	return 0
}

// lastAnswer is the most recent thing the assistant actually said.
//
// Read from durable truth rather than accumulated as events arrive: the session
// is what the next turn is built from, so an answer taken from anywhere else
// could differ from what the model will be shown it said.
func lastAnswer(sess *session.Session) (string, bool) {
	messages := sess.Snapshot().Messages
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role == ai.RoleAssistant && strings.TrimSpace(m.Content) != "" {
			return m.Content, true
		}
	}
	return "", false
}
