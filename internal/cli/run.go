package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/compaction"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/jsonstream"
	"github.com/iamclancyliang/pi-go/internal/rpc"
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

	// Conversation is where this run's history lives. Required: a run with no
	// session has one that keeps nothing, which is a decision made where the
	// flags are read rather than assumed here.
	Conversation *Conversation

	// Args and WorkingDir are what a command needs to open ANOTHER
	// conversation — /new and /resume must land in the same session directory
	// this run was told to use, not in whatever the default happens to be.
	Args       Args
	WorkingDir string

	// Transport is how a newly opened provider reaches the network, so a model
	// switched to mid-session goes through the same one this run was given.
	Transport http.RoundTripper

	// Config is what this run resolved: effective settings, and how the
	// project's trust question was answered. /settings and /trust read it.
	Config Config

	// Thinking is how much reasoning to ask for on every turn.
	Thinking ai.ThinkingLevel

	// ReadLine, when set, replaces the plain line scanner with an editing
	// prompt — the terminal path. Nil reads lines from Streams.In, which is
	// what tests and redirected input use. A seam rather than a TTY check
	// here, so the decision lives where the terminal is actually known.
	ReadLine func(prompt string) (string, bool, error)
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

	if rt.Conversation == nil || rt.Conversation.Session == nil {
		// A composition root's mistake rather than a user's, but reported
		// rather than dereferenced: a crash here names a line in this file and
		// says nothing about what went wrong.
		fmt.Fprintln(streams.Err, "pi: no conversation was opened for this run")
		return 1
	}
	sess := rt.Conversation.Session
	agent, err := runtime.New(runtime.Config{
		Model:     rt.Model,
		ModelName: rt.ModelName,
		Tools:     rt.Tools,
		Session:   sess,
		Thinking:  rt.Thinking,
		// A one-shot can overflow too, and a refusal with no recovery is a
		// failed run where a shortened context would have answered.
		Summarize: (&compaction.Compactor{Model: rt.Model, ModelName: rt.ModelName}).Summarize,
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

// RunJSON sends each prompt in turn and writes the event stream to stdout.
//
// One-shot like RunPrint, but stdout carries the protocol rather than an
// answer: the version line, then every lifecycle and reply event as JSON
// lines. The answer is inside the stream — a model_response carries its text —
// so nothing else is written there, and a consumer parses lines or nothing.
//
// Failures behave as in print mode — the reason on stderr, the exit code
// non-zero — with one addition: the stream itself failing to write is a
// failure of the run, because a consumer that got half a stream with no error
// would read an interrupted run as a quiet one.
func RunJSON(ctx context.Context, rt Runtime, streams Streams, prompts []string) int {
	if len(prompts) == 0 {
		fmt.Fprintln(streams.Err, "pi: no prompt given")
		return 1
	}
	if rt.Conversation == nil || rt.Conversation.Session == nil {
		fmt.Fprintln(streams.Err, "pi: no conversation was opened for this run")
		return 1
	}

	// The writer opens the stream immediately: the version line precedes every
	// event, including agent_start, so a consumer knows what it is reading
	// before there is anything to read.
	writer := jsonstream.NewWriter(streams.Out)

	sess := rt.Conversation.Session
	agent, err := runtime.New(runtime.Config{
		Model:     rt.Model,
		ModelName: rt.ModelName,
		Tools:     rt.Tools,
		Session:   sess,
		Thinking:  rt.Thinking,
		Summarize: (&compaction.Compactor{Model: rt.Model, ModelName: rt.ModelName}).Summarize,
		// The writer is both observers, which is the wiring the shared
		// counter exists for: one consumer, two families, one order.
		Observers:      []events.Observer{writer},
		ReplyObservers: []runtime.ReplyObserver{writer},
	})
	if err != nil {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}

	code := 0
	for _, prompt := range prompts {
		if err := agent.Run(ctx, prompt); err != nil {
			// The stream already carries the failure — agent_end names it —
			// but stderr and the exit code repeat it for the caller that
			// checks status before parsing anything.
			fmt.Fprintf(streams.Err, "pi: %v\n", err)
			code = 1
			break
		}
	}
	if err := writer.Err(); err != nil {
		fmt.Fprintf(streams.Err, "pi: the event stream broke: %v\n", err)
		return 1
	}
	return code
}

// agentRunner narrows the runtime's Agent to what the channel drives. Start
// returns the runtime's own *Run, which already has the channel's contract;
// the adapter exists only because Go does not let a concrete return type
// satisfy an interface's.
type agentRunner struct{ agent *runtime.Agent }

func (a agentRunner) Start(ctx context.Context, prompt string) (rpc.Run, error) {
	return a.agent.Start(ctx, prompt)
}

// RunRPC drives the command channel: commands on stdin, responses and events on
// stdout, one JSON object per line.
//
// A prompt runs on its own goroutine while stdin keeps being read, which is
// what lets abort, steer and follow_up arrive during one. Everything on stdout
// is numbered by the writer under its own lock, so the wire's order is the
// order things were written whichever goroutine wrote them.
//
// It returns when stdin reaches EOF, after letting a running prompt finish. A
// command that fails is answered with a typed failure and the loop continues;
// only a broken stdout stream, or stdin itself failing, ends the run.
func RunRPC(ctx context.Context, rt Runtime, streams Streams) int {
	if rt.Conversation == nil || rt.Conversation.Session == nil {
		fmt.Fprintln(streams.Err, "pi: no conversation was opened for this run")
		return 1
	}

	writer := jsonstream.NewWriter(streams.Out)

	sess := rt.Conversation.Session
	agent, err := runtime.New(runtime.Config{
		Model:          rt.Model,
		ModelName:      rt.ModelName,
		Tools:          rt.Tools,
		Session:        sess,
		Thinking:       rt.Thinking,
		Summarize:      (&compaction.Compactor{Model: rt.Model, ModelName: rt.ModelName}).Summarize,
		Observers:      []events.Observer{writer},
		ReplyObservers: []runtime.ReplyObserver{writer},
	})
	if err != nil {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}

	channel := rpc.NewChannel(agentRunner{agent}, rpc.NewState(sess, rt.Provider, rt.ModelName))
	if err := rpc.Loop(ctx, streams.In, writer, channel); err != nil {
		if err == context.Canceled {
			return 0
		}
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}
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
	if rt.Conversation == nil || rt.Conversation.Session == nil {
		// A composition root's mistake rather than a user's, but reported
		// rather than dereferenced: a crash here names a line in this file and
		// says nothing about what went wrong.
		fmt.Fprintln(streams.Err, "pi: no conversation was opened for this run")
		return 1
	}
	// The agent is rebuilt whenever the conversation changes, because a loop
	// holds the session it was built with: a command that opened another one
	// while the agent still pointed at the old would write the next turn into
	// the conversation the user just left.
	current := rt.Conversation
	// Copied so the commands may update it — /reload re-resolves — without
	// reaching back into the caller's value.
	config := rt.Config
	// The model can change mid-session, so what answers is held here rather
	// than read from the configuration each time.
	port, provider, modelName := rt.Model, rt.Provider, rt.ModelName
	var agent *runtime.Agent
	build := func() error {
		next, err := runtime.New(runtime.Config{
			Model:     port,
			ModelName: modelName,
			Tools:     rt.Tools,
			Session:   current.Session,
			Thinking:  rt.Thinking,
			// The same summariser /compact uses. Without it an overflow is
			// terminal: the request is refused, and a conversation long enough
			// to overflow once will do it again on every turn after.
			Summarize: (&compaction.Compactor{Model: port, ModelName: modelName}).Summarize,
		})
		if err != nil {
			return err
		}
		agent = next
		return nil
	}
	if err := build(); err != nil {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}

	ctxt := &commandContext{
		session:      current.Session,
		conversation: current,
		out:          streams.Out,
		errOut:       streams.Err,
		args:         rt.Args,
		workingDir:   rt.WorkingDir,
		config:       &config,

		modelProvider: func() string { return provider },
		modelName:     func() string { return modelName },
	}
	// The scanner is shared with the prompt loop, so a confirmation reads the
	// next line the user types rather than opening a second reader that would
	// race the first for the same input.
	var lines *bufio.Scanner
	ctxt.confirm = func() bool {
		if lines == nil || !lines.Scan() {
			return false
		}
		answer := strings.ToLower(strings.TrimSpace(lines.Text()))
		// Only an explicit yes. Anything else — including end of input — leaves
		// the outward-facing thing undone, which is the safe way to be wrong.
		return answer == "y" || answer == "yes"
	}
	// What the port and the tool set were BUILT from, frozen at startup. The
	// live config moves — /settings edits it — so comparing a reload against
	// the live view would report "nothing changed" for exactly the changes
	// that only a new run can pick up.
	builtFrom := rt.Config.Effective
	ctxt.reloadConfig = func() (bool, error) {
		resolved, err := ResolveConfig(rt.Args, rt.WorkingDir, nil)
		if err != nil {
			return false, err
		}
		changed := resolved.Effective.DefaultProvider != builtFrom.DefaultProvider ||
			resolved.Effective.DefaultModel != builtFrom.DefaultModel ||
			resolved.Effective.ShellPath != builtFrom.ShellPath ||
			resolved.Effective.ShellCommandPrefix != builtFrom.ShellCommandPrefix ||
			resolved.Effective.SessionDir != builtFrom.SessionDir
		config = resolved
		return changed, nil
	}
	ctxt.compact = func(instructions string) error {
		// Summarised with whatever model is answering now, so a conversation
		// switched to a cheaper model is compacted by it too.
		c := &compaction.Compactor{
			Model: port, ModelName: modelName, Instructions: instructions,
			KeepRecentTokens: config.Effective.Compaction.KeepRecentTokens,
		}
		summary, kept, err := c.Summarize(ctx, current.Session.Truth())
		if err != nil {
			return err
		}
		return current.Session.Compact(summary, kept)
	}
	ctxt.switchModel = func(toProvider, toModel string) error {
		next := rt.Args
		next.Model = toModel
		if toProvider != "" {
			next.Provider = toProvider
		} else {
			// No provider named means the one already answering, not whichever
			// credential happens to be found first.
			next.Provider = provider
		}
		opened, openedProvider, openedModel, err := Open(next, rt.Transport)
		if err != nil {
			return err
		}
		port, provider, modelName = opened, openedProvider, openedModel
		return build()
	}
	ctxt.reload = func() error {
		// Rebuilt from the store the conversation already has, rather than
		// reopened from its path: reopening would read the leaf back off disk
		// and undo the move that was just made in memory.
		restored, err := session.Restore(context.Background(), rt.System, current.Store)
		if err != nil {
			return err
		}
		current.Session = restored
		ctxt.session = restored
		return build()
	}
	ctxt.reopen = func(args Args) error {
		opened, err := OpenConversation(args, rt.WorkingDir, rt.System)
		if err != nil {
			return err
		}
		// The old one is closed only once the new one is open, so a failure
		// leaves the session the user was in rather than none at all.
		_ = current.Close()
		current = opened
		ctxt.session, ctxt.conversation = opened.Session, opened
		return build()
	}
	defer func() { _ = current.Close() }()

	if !config.Effective.QuietStartup {
		fmt.Fprintf(streams.Out, "pi-go · %s/%s · %d tools · /help for commands\n",
			provider, modelName, len(rt.Tools.All()))
	}
	// Said aloud, because continuing silently cannot be told from starting
	// fresh until the model answers something it should not have known.
	switch {
	case current.Resumed:
		fmt.Fprintf(streams.Out, "resumed %d messages · %s\n",
			len(current.Session.Snapshot().Messages), current.ID)
	case current.Path != "":
		// The id is shown at the start rather than the end: a session ended by
		// closing the terminal never reaches an ending, and an id the user
		// never saw is one they cannot resume.
		fmt.Fprintf(streams.Out, "session %s · -c to continue it later\n", current.ID)
	}

	lines = bufio.NewScanner(streams.In)
	// A prompt can be long; the default limit would cut one mid-sentence and
	// send the fragment.
	lines.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	readLine := func() (string, bool) {
		if rt.ReadLine != nil {
			line, ok, err := rt.ReadLine("> ")
			if err != nil {
				fmt.Fprintf(streams.Err, "pi: %v\n", err)
				return "", false
			}
			return line, ok
		}
		fmt.Fprint(streams.Out, "\n> ")
		if !lines.Scan() {
			return "", false
		}
		return lines.Text(), true
	}

	for {
		line, more := readLine()
		if !more {
			break
		}
		prompt := strings.TrimSpace(line)
		if prompt == "" {
			continue
		}
		if handled, stop := dispatch(ctxt, prompt); handled {
			if stop {
				break
			}
			continue
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
		if answer, ok := lastAnswer(current.Session); ok {
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
