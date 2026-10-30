package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	goruntime "runtime"
	"sort"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/interactive"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/tools"
	"github.com/iamclancyliang/pi-go/internal/tui"
)

// RunFullScreen runs the conversation in the full-screen interface on term
// — Pi's interactive mode — taking prompts from its editor until the user
// leaves. prompts given on the command line are sent first, in turn.
func RunFullScreen(ctx context.Context, rt Runtime, streams Streams, term tui.Terminal, version string, prompts []string) int {
	if rt.Conversation == nil || rt.Conversation.Session == nil {
		fmt.Fprintln(streams.Err, "pi: no conversation was opened for this run")
		return 1
	}

	// The agent reports to the mode, which does not exist yet when the agent
	// is first built; nothing is reported before the first prompt.
	var mode *interactive.Mode
	observer := events.ObserverFunc(func(e events.Event) { mode.Observer().OnEvent(e) })
	replies := runtime.ReplyObserverFunc(func(e ai.StreamEvent) { mode.ReplyObserver()(e) })
	s, err := newInteractiveSession(ctx, rt, streams, []events.Observer{observer}, []runtime.ReplyObserver{replies})
	if err != nil {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}
	defer s.close()

	home, _ := os.UserHomeDir()
	env := interactive.NewEnv(interactive.DarkTheme(tui.Capabilities().TrueColor),
		interactive.NewKeybindings(goruntime.GOOS, nil), rt.WorkingDir, home)
	ui := tui.NewUI(term)
	effective := s.config.Effective
	ui.SetShowHardwareCursor(ShowHardwareCursor(effective, os.Getenv))
	ui.SetClearOnShrink(ClearOnShrink(effective, os.Getenv))
	host := &fullScreenHost{s: s, providers: AvailableProviders(rt.Args)}
	mode = interactive.NewMode(ui, env, host, interactive.Options{
		Version:                version,
		QuietStartup:           effective.QuietStartup,
		Notices:                s.startNotices(),
		InitialMessages:        prompts,
		HideThinking:           effective.HideThinkingBlock,
		OutputPad:              OutputPad(effective),
		EditorPaddingX:         EditorPaddingX(effective),
		AutocompleteMaxVisible: AutocompleteMaxVisible(effective),
		DoubleEscapeAction:     DoubleEscapeAction(effective),
	})
	if err := mode.Run(); err != nil {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}
	if current, _, _, _, _ := s.snapshot(); current.Path != "" {
		resume := "pi"
		if rt.Args.SessionDir != "" {
			resume += " --session-dir " + quoteIfNeeded(rt.Args.SessionDir)
		}
		resume += " --session " + current.ID
		fmt.Fprintf(streams.Out, "\x1b[2mTo resume this session:\x1b[22m %s\n", resume)
	}
	return 0
}

func quoteIfNeeded(s string) string {
	if strings.ContainsAny(s, " \t\"'") {
		return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
	}
	return s
}

// fullScreenHost is the interactive session as the full-screen mode drives
// it.
type fullScreenHost struct {
	s *interactiveSession
	// providers is how many providers have a credential, counted at start and
	// after a login or logout.
	providers int
	// listed is what each provider reported when last asked, guarded by s.mu.
	listed map[string][]ai.ModelRef
}

func (h *fullScreenHost) Start(ctx context.Context, prompt string) (interactive.Run, error) {
	_, agent, _, _, _ := h.s.snapshot()
	return agent.Start(ctx, prompt)
}

func (h *fullScreenHost) Command(ctx context.Context, text string, out *bytes.Buffer, confirm func(string) bool) (bool, bool) {
	c := h.s.ctxt
	c.out, c.errOut = out, out
	// A question is what the command has said so far.
	c.confirm = func() bool {
		question := out.String()
		out.Reset()
		return confirm(strings.TrimRight(question, "\n"))
	}
	return dispatch(c, text)
}

func (h *fullScreenHost) Conversation() []ai.Message {
	current, _, _, _, _ := h.s.snapshot()
	return current.Session.Project().Messages
}

func (h *fullScreenHost) Footer() interactive.FooterState {
	current, _, provider, model, thinking := h.s.snapshot()
	used := current.Session.Usage()
	state := interactive.FooterState{
		Cwd:           h.s.rt.WorkingDir,
		SessionName:   current.Session.Name(),
		Input:         used.InputTokens,
		Output:        used.OutputTokens,
		ContextWindow: autoCompactionFor(h.s.rt.AutoCompaction, provider, model).ContextWindow,
		AutoCompact:   h.s.rt.AutoCompaction.Enabled,
		Provider:      provider,
		Model:         model,
		ShowProvider:  h.providers > 1,
	}
	if used.CacheReadTokens != nil {
		state.CacheRead = *used.CacheReadTokens
	}
	if ai.SupportsThinking(provider, model) {
		state.Thinking = string(thinking)
		if state.Thinking == "" {
			state.Thinking = "off"
		}
	}
	return state
}

func (h *fullScreenHost) SlashCommands() []tui.SlashCommand {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]tui.SlashCommand, 0, len(names)+1)
	for _, name := range names {
		out = append(out, tui.SlashCommand{Name: name, Description: commands[name].Summary})
	}
	// The scope selector exists only here, where it can be drawn.
	out = append(out, tui.SlashCommand{Name: "scoped-models", Description: "choose which models cycling goes through"})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (h *fullScreenHost) Bash(ctx context.Context, command string, exclude bool, onChunk func(string)) (tools.BashResult, error) {
	if h.s.ctxt.bash == nil {
		return tools.BashResult{}, fmt.Errorf("shell execution is not configured for this run")
	}
	return h.s.ctxt.bash(ctx, command, exclude, onChunk)
}

func (h *fullScreenHost) Files() tui.PathFinder {
	return func(ctx context.Context, base, query string, limit int) []tui.PathMatch {
		var out []tui.PathMatch
		for _, m := range tools.SearchPaths(ctx, base, query, limit) {
			out = append(out, tui.PathMatch{Path: m.Path, IsDir: m.IsDir})
		}
		return out
	}
}
