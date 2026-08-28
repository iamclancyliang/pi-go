// Command pi is the coding agent.
//
// A composition root: it assembles modules and carries no behaviour of its own.
// What it decides — which mode to run, which provider to reach, what to say
// when a flag cannot be honoured — lives in internal/cli, where it can be
// tested without building a binary and running it.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/iamclancyliang/pi-go/internal/cli"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// version is what --version reports. Set by the build; "dev" when it is not.
var version = "dev"

func main() { os.Exit(run(os.Args[1:])) }

func run(argv []string) int {
	args := cli.ParseArgs(argv)
	streams := cli.Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}

	for _, d := range args.Diagnostics {
		if d.Warning {
			fmt.Fprintf(streams.Err, "pi: warning: %s\n", d.Message)
			continue
		}
		fmt.Fprintf(streams.Err, "pi: %s\n", d.Message)
	}
	if args.Failed() {
		return 2
	}
	for _, name := range args.UnknownNames() {
		fmt.Fprintf(streams.Err, "pi: warning: --%s is not a flag this build knows; ignoring it\n", name)
	}

	if args.Help {
		fmt.Fprint(streams.Out, usage)
		return 0
	}
	if args.Version {
		fmt.Fprintf(streams.Out, "pi-go %s\n", version)
		return 0
	}

	mode := cli.ResolveAppMode(args, isTerminal(os.Stdin), isTerminal(os.Stdout))
	switch mode {
	case cli.AppJSON, cli.AppRPC:
		// Refused rather than approximated. Both protocols are defined by an
		// event and payload schema that this repository has not yet recorded
		// from the pinned source, and emitting an invented shape would teach a
		// client something it would later have to unlearn.
		fmt.Fprintf(streams.Err,
			"pi: --mode %s is not implemented yet; its event schema is not recorded\n", mode)
		return 2
	}

	port, providerName, model, err := cli.Open(args, http.DefaultTransport)
	if err != nil {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}
	registry := tools.NewRegistry()
	if !args.NoTools {
		if registry, err = tools.NewBuiltInRegistry(root); err != nil {
			fmt.Fprintf(streams.Err, "pi: %v\n", err)
			return 1
		}
	}

	system := args.SystemPrompt
	if system == "" {
		system = cli.DefaultSystemPrompt
	}
	conversation, err := cli.OpenConversation(args, root, system)
	if err != nil {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}
	defer conversation.Close()

	rt := cli.Runtime{
		Model:        port,
		ModelName:    model,
		Tools:        registry,
		System:       system,
		Provider:     providerName,
		Conversation: conversation,
		Args:         args,
		WorkingDir:   root,
	}

	// One interrupt cancels the run in progress. A second is left to the
	// default handler, so a wedged process can still be killed from the same
	// keyboard rather than needing another terminal.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if mode == cli.AppPrint {
		return cli.RunPrint(ctx, rt, streams, args.Messages)
	}
	return cli.RunInteractive(ctx, rt, streams)
}

// isTerminal reports whether a stream is a terminal.
//
// Half of the mode decision rests on this: the same command line runs
// interactively in a terminal and prints when either stream is redirected.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

const usage = `pi-go — a coding agent

Usage:
  pi [flags] [prompt ...]

Flags:
  -p, --print [prompt]     answer once and exit, rather than starting a session
  -c, --continue           carry on the most recent conversation here
  -r, --resume [id]        reopen a conversation by id; bare means the most recent
      --no-session         keep the conversation in memory only
      --session-dir DIR    where conversations are kept
      --mode text|json|rpc how to run; text lets the terminal decide
      --provider NAME      deepseek, openai or qwen
      --model NAME         the model to ask for
      --api-key KEY        the credential, instead of the environment
      --system-prompt TEXT what to tell the agent it is
      --no-tools           offer the model no tools
  -h, --help               this text
  -v, --version            the version

Without --print, pi starts a session when both input and output are terminals,
and answers once otherwise — so a redirected run is a one-shot even without the
flag.

Conversations are recorded under ~/.pi-go/agent/sessions, grouped by the
directory they ran in, so --continue offers the work you were just doing here.

In a session, /help lists the commands — and says which of Pi's are not here.

Credentials come from the environment: DEEPSEEK_API_KEY, OPENAI_API_KEY or
DASHSCOPE_API_KEY. With no --provider, the first one that is set is used.
`
