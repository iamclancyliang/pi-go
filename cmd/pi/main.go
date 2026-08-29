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
	"strings"
	"syscall"

	"github.com/iamclancyliang/pi-go/internal/cli"
	"github.com/iamclancyliang/pi-go/internal/tools"
	"github.com/iamclancyliang/pi-go/internal/tui"
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

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}

	// Settings before anything they shape. The trust question is asked only in
	// interactive mode: print mode has no prompt, and a project must not become
	// trusted because nobody could object.
	var askTrust func(string) bool
	if mode == cli.AppInteractive {
		askTrust = func(prompt string) bool {
			fmt.Fprintf(streams.Out, "%s [y/N]: ", prompt)
			var answer string
			fmt.Fscanln(streams.In, &answer)
			answer = strings.ToLower(strings.TrimSpace(answer))
			return answer == "y" || answer == "yes"
		}
	}
	cfg, err := cli.ResolveConfig(args, root, askTrust)
	if err != nil {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}
	args = cli.ApplyDefaults(args, cfg.Effective)

	port, providerName, model, err := cli.Open(args, http.DefaultTransport)
	if err != nil {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}

	registry := tools.NewRegistry()
	if !args.NoTools {
		if registry, err = cli.BuildTools(root, cfg.Effective); err != nil {
			fmt.Fprintf(streams.Err, "pi: %v\n", err)
			return 1
		}
	}

	// Built from the tool set this run offers and the project's own
	// instructions, not from a constant.
	system := cli.BuildSystemPrompt(args, registry, root, cfg.AgentDir)
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
		Thinking:     args.Thinking,
		Args:         args,
		WorkingDir:   root,
		Transport:    http.DefaultTransport,
		Config:       cfg,
	}

	if mode == cli.AppInteractive {
		// The editing prompt exists only where there is a terminal to edit in.
		// Failing to open one falls back to plain lines rather than failing
		// the run: the conversation matters more than the editing.
		if prompter, err := tui.NewPrompter(nil); err == nil {
			defer prompter.Close()
			rt.ReadLine = prompter.ReadLine
		}
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
      --system-prompt TEXT replace the assembled prompt
      --append-system-prompt TEXT   add to it; repeatable
      --thinking LEVEL     off, minimal, low, medium, high, xhigh or max
      --no-context-files   ignore the project's AGENTS.md and the like
      --no-tools           offer the model no tools
  -h, --help               this text
  -v, --version            the version

Without --print, pi starts a session when both input and output are terminals,
and answers once otherwise — so a redirected run is a one-shot even without the
flag.

Conversations are recorded under ~/.pi-go/agent/sessions, grouped by the
directory they ran in, so --continue offers the work you were just doing here.

In a session, /help lists the commands — and says which of Pi's are not here.
/tree shows the shape of a conversation and goes back to any point in it;
/fork and /clone copy one into a new conversation, leaving the original alone.
/compact summarises the older part before the context fills up.

Settings live in <agent-dir>/settings.json, and a project may carry its own in
.pi-go/settings.json — read only once you trust the project (/trust), because
settings include the shell every command runs in.

The system prompt is assembled from the tools this run offers and the project's
own instructions — AGENTS.md, AGENTS.override.md or CLAUDE.md, read from the
agent directory and every ancestor of the working directory, nearest last.

Credentials come from --api-key, then from what /login saved, then from the
environment: DEEPSEEK_API_KEY, OPENAI_API_KEY or DASHSCOPE_API_KEY. With no
--provider, the first one with a credential is used.
`
