package interactive

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/compaction"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
	"github.com/iamclancyliang/pi-go/internal/tui"
)

// Run is an agent run in flight.
type Run interface {
	// Steer redirects the work at its next safe point.
	Steer(text string) error
	// Follow queues a message for when the work would otherwise stop.
	Follow(text string) error
	// Wait returns when the run has settled.
	Wait() error
}

// Host is what the interactive mode drives: the agent, the commands and the
// conversation, as the command line has assembled them.
type Host interface {
	// Start begins a run of the agent on prompt; cancelling ctx aborts it.
	// The agent must report to the mode's Observer and ReplyObserver.
	Start(ctx context.Context, prompt string) (Run, error)
	// Command runs text if it is a command — "/name ..." or "!shell" —
	// writing what it says to out, and reports whether it was one and
	// whether the session should end. confirm asks the user yes or no.
	Command(ctx context.Context, text string, out *bytes.Buffer, confirm func(question string) bool) (handled, quit bool)
	// Conversation is what the model would be shown now: the history since
	// the last compaction, after its summary.
	Conversation() []ai.Message
	// Footer is the footer's state, apart from how full the context is,
	// which the mode tracks from the replies.
	Footer() FooterState
	// SlashCommands are what "/" completes.
	SlashCommands() []tui.SlashCommand
	// Files searches the working directory for "@" completion.
	Files() tui.PathFinder
	// Bash runs a shell command the user typed after "!", reporting its
	// output as it arrives; exclude keeps it out of the model's context.
	Bash(ctx context.Context, command string, exclude bool, onChunk func(string)) (tools.BashResult, error)
}

// Options configure the mode.
type Options struct {
	Version      string
	QuietStartup bool
	// Notices are shown under the header at startup — a resumed session,
	// where the session is kept.
	Notices []string
	// InitialMessages are sent first, each once the one before has run.
	InitialMessages []string
}

// Mode is the full-screen interactive mode — Pi's InteractiveMode in its
// regular, main-screen form: the conversation above, then queued messages,
// a status line, the editor and the footer.
type Mode struct {
	host Host
	env  *Env
	ui   *tui.UI
	opts Options

	header  tui.Container
	chat    tui.Container
	pending tui.Container
	status  tui.Container
	editorC tui.Container
	footerC tui.Container

	editor *appEditor
	footer *Footer

	// Loop state.
	running    bool
	cancel     context.CancelFunc
	run        Run
	streaming  *AssistantMessage
	tools      map[string]*ToolExecution
	toolErrors map[string]string
	steering   []string
	followUps  []string
	// unsent are queued messages waiting for the run to have started.
	unsent       []queuedMessage
	activeStatus *statusIndicator
	expanded     bool
	hideThinking bool
	headerText   *expandable
	lastCtrlC    time.Time
	confirming   chan string
	commandBusy  bool
	quitting     bool
	// bashCancel stops the shell command running, if one is; pendingBash
	// are commands run during an agent run, shown with the queued messages
	// until it ends.
	bashCancel  context.CancelFunc
	pendingBash []*BashExecution

	// replyFailed is set when the last reply ended in an error, which it
	// shows itself, so the run's error is not shown a second time.
	replyFailed bool

	contextTokens int
	contextKnown  bool
	beforeCompact int
	compacted     bool
}

type queuedMessage struct {
	text   string
	follow bool
}

// NewMode is the interactive mode on ui. The env's RequestRender and Do are
// set to the ui's.
func NewMode(ui *tui.UI, env *Env, host Host, opts Options) *Mode {
	env.RequestRender = ui.RequestRender
	env.Do = ui.Do
	m := &Mode{host: host, env: env, ui: ui, opts: opts,
		tools: map[string]*ToolExecution{}, toolErrors: map[string]string{}}

	editor := tui.NewEditor(ui, env.Theme.EditorTheme(), tui.EditorOptions{})
	editor.Keybindings = env.Keys
	editor.SetAutocompleteProvider(tui.NewCombinedAutocompleteProvider(host.SlashCommands(), env.Cwd, host.Files()))
	m.editor = &appEditor{Editor: editor, keys: env.Keys, actions: map[string]func(){}}
	editor.OnSubmit = m.submit
	m.bindKeys()
	m.editorC.AddChild(m.editor)

	m.footer = NewFooter(env, m.footerState)
	m.footerC.AddChild(m.footer)

	for _, c := range []tui.Component{&m.header, &m.chat, &m.pending, &m.status, &m.editorC, &m.footerC} {
		ui.AddChild(c)
	}
	return m
}

// Observer receives the agent's events; ReplyObserver its replies as they
// arrive. Both hand what they receive to the interface's loop.
func (m *Mode) Observer() events.Observer {
	return events.ObserverFunc(func(e events.Event) { m.ui.Do(func() { m.handleEvent(e) }) })
}

func (m *Mode) ReplyObserver() func(ai.StreamEvent) {
	return func(e ai.StreamEvent) { m.ui.Do(func() { m.handleReply(e) }) }
}

// Run shows the conversation so far and takes input until the user leaves.
func (m *Mode) Run() error {
	m.ui.Do(func() {
		m.buildHeader()
		m.renderHistory()
		m.ui.SetFocus(m.editor)
		m.nextInitial()
	})
	return m.ui.Run()
}

// --- layout pieces ---

func (m *Mode) buildHeader() {
	t := m.env.Theme
	if m.opts.QuietStartup {
		m.header.AddChild(tui.NewText("", 0, 0, nil))
	} else {
		logo := Bold(t.Fg("accent", "pi-go")) + t.Fg("dim", " v"+m.opts.Version)
		hint := m.env.KeyHint
		raw := func(key, description string) string {
			return t.Fg("dim", m.env.formatKeyText(key, false)) + t.Fg("muted", " "+description)
		}
		compact := strings.Join([]string{
			hint("app.interrupt", "interrupt"),
			raw(m.env.KeyText("app.clear")+"/"+m.env.KeyText("app.exit"), "clear/exit"),
			raw("/", "commands"),
			raw("!", "bash"),
			hint("app.tools.expand", "more"),
		}, t.Fg("muted", " · "))
		expandedText := strings.Join([]string{
			hint("app.interrupt", "to interrupt"),
			hint("app.clear", "to clear"),
			raw(m.env.KeyText("app.clear")+" twice", "to exit"),
			hint("app.exit", "to exit (empty)"),
			hint("tui.editor.deleteToLineEnd", "to delete to end"),
			hint("app.tools.expand", "to expand tools"),
			hint("app.thinking.toggle", "to expand thinking"),
			raw("/", "for commands"),
			raw("!", "to run bash"),
			raw("!!", "to run bash (no context)"),
			hint("app.message.followUp", "to queue follow-up"),
		}, "\n")
		onboarding := t.Fg("dim", "Press "+m.env.KeyText("app.tools.expand")+" to show full startup help.")
		m.headerText = &expandable{
			collapsed: logo + "\n" + compact + "\n" + onboarding,
			expanded:  logo + "\n" + expandedText,
		}
		m.header.AddChild(&tui.Spacer{Lines: 1})
		m.header.AddChild(m.headerText)
		m.header.AddChild(&tui.Spacer{Lines: 1})
	}
	for _, n := range m.opts.Notices {
		m.header.AddChild(tui.NewText(t.Fg("dim", n), 1, 0, nil))
	}
}

// expandable is text with a short and a long form.
type expandable struct {
	collapsed, expanded string
	open                bool
}

func (e *expandable) Invalidate() {}
func (e *expandable) Render(width int) []string {
	text := e.collapsed
	if e.open {
		text = e.expanded
	}
	return tui.NewText(text, 1, 0, nil).Render(width)
}

func (m *Mode) footerState() FooterState {
	s := m.host.Footer()
	s.ContextTokens, s.ContextKnown = m.contextTokens, m.contextKnown
	if !m.contextKnown && !m.compacted {
		// Nothing reported yet: the conversation's estimated size. After a
		// compaction the size is unknown until the next reply reports it.
		s.ContextTokens, s.ContextKnown = compaction.EstimateConversation(m.host.Conversation()), true
	}
	return s
}

// --- history ---

// renderHistory draws the conversation so far: each message, each tool
// call with its result, and a compaction's summary where one stands in for
// earlier history.
func (m *Mode) renderHistory() {
	m.chat.Clear()
	t := m.env
	pending := map[string]*ToolExecution{}
	for _, msg := range m.host.Conversation() {
		if summary, ok := session.SummaryOf(msg); ok {
			m.chat.AddChild(&tui.Spacer{Lines: 1})
			c := NewCompactionSummary(t, m.beforeCompact, summary)
			c.SetExpanded(m.expanded)
			m.chat.AddChild(c)
			continue
		}
		switch msg.Role {
		case ai.RoleUser:
			if msg.BashExecution != nil {
				m.addBashExecution(msg.BashExecution)
				continue
			}
			if msg.Content == "" {
				continue
			}
			m.addUserMessage(msg.Content)
			m.editor.AddToHistory(msg.Content)
		case ai.RoleAssistant:
			reply := &ai.AssistantMessage{StopReason: ai.StopEnd}
			if msg.Reasoning != "" {
				reply.Blocks = append(reply.Blocks, ai.Block{Kind: ai.BlockThinking, Text: msg.Reasoning})
			}
			if msg.Content != "" {
				reply.Blocks = append(reply.Blocks, ai.Block{Kind: ai.BlockText, Text: msg.Content})
			}
			for _, call := range msg.ToolCalls {
				reply.Blocks = append(reply.Blocks, ai.Block{Kind: ai.BlockToolCall, Call: call})
			}
			m.chat.AddChild(NewAssistantMessage(t, reply, m.hideThinking, 1))
			for _, call := range msg.ToolCalls {
				x := NewToolExecution(t, call.Name, call.Args)
				x.SetExpanded(m.expanded)
				x.SetArgsComplete()
				m.chat.AddChild(x)
				pending[call.ID] = x
			}
		case ai.RoleTool:
			if x, ok := pending[msg.ToolCallID]; ok {
				x.UpdateResult(ToolResult{Text: msg.Content, IsError: strings.HasPrefix(msg.Content, "error: ")}, false)
				delete(pending, msg.ToolCallID)
			}
		}
	}
	m.ui.RequestRender()
}

func (m *Mode) addUserMessage(text string) {
	if len(m.chat.Children()) > 0 {
		m.chat.AddChild(&tui.Spacer{Lines: 1})
	}
	m.chat.AddChild(NewUserMessage(m.env, text, 1))
}

// addBashExecution shows a shell command the user ran, with its output.
func (m *Mode) addBashExecution(b *ai.BashExecution) {
	c := NewBashExecution(m.env, b.Command, b.ExcludeFromContext)
	c.SetExpanded(m.expanded)
	if b.Output != "" {
		c.AppendOutput(b.Output)
	}
	c.SetComplete(tools.BashResult{ExitCode: b.ExitCode, Cancelled: b.Cancelled, Truncated: b.Truncated, FullOutputPath: b.FullOutputPath})
	m.chat.AddChild(c)
}

// --- messages to the user ---

func (m *Mode) showError(text string) {
	m.chat.AddChild(&tui.Spacer{Lines: 1})
	m.chat.AddChild(tui.NewText(m.env.Theme.Fg("error", "Error: "+text), 1, 0, nil))
	m.ui.RequestRender()
}

func (m *Mode) showStatus(text string) {
	m.chat.AddChild(&tui.Spacer{Lines: 1})
	m.chat.AddChild(tui.NewText(m.env.Theme.Fg("dim", text), 1, 0, nil))
	m.ui.RequestRender()
}

func (m *Mode) showOutput(text string) {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return
	}
	m.chat.AddChild(&tui.Spacer{Lines: 1})
	m.chat.AddChild(tui.NewText(text, 1, 0, nil))
	m.ui.RequestRender()
}

func (m *Mode) setStatus(s *statusIndicator) {
	if m.activeStatus != nil {
		m.activeStatus.dispose()
	}
	m.activeStatus = s
	m.status.Clear()
	if s != nil {
		m.status.AddChild(s)
	}
}

func (m *Mode) clearStatus(kind string) {
	if m.activeStatus == nil || (kind != "" && m.activeStatus.kind != kind) {
		return
	}
	m.setStatus(nil)
}

// --- keys ---

// appEditor is the editor with the mode's own keys in front of it — Pi's
// CustomEditor: an action bound to a key runs instead of the editor seeing
// it, escape only when no completion list is open, and exit only when the
// editor is empty.
type appEditor struct {
	*tui.Editor
	keys     *tui.Keybindings
	actions  map[string]func()
	onEscape func()
	onExit   func()
}

func (e *appEditor) HandleKey(k tui.Key) {
	switch {
	case e.keys.Matches(k, "app.interrupt"):
		if !e.IsShowingAutocomplete() && e.onEscape != nil {
			e.onEscape()
			return
		}
	case e.keys.Matches(k, "app.exit") && e.Text() == "":
		if e.onExit != nil {
			e.onExit()
		}
		return
	case e.keys.Matches(k, "tui.editor.historyPrevious"), e.keys.Matches(k, "tui.editor.historyNext"):
		e.Editor.HandleKey(k)
		return
	}
	for action, fn := range e.actions {
		if e.keys.Matches(k, action) {
			fn()
			return
		}
	}
	e.Editor.HandleKey(k)
}

func (m *Mode) bindKeys() {
	m.editor.onEscape = m.interrupt
	m.editor.onExit = m.shutdown
	m.editor.actions["app.clear"] = m.ctrlC
	m.editor.actions["app.tools.expand"] = func() { m.setExpanded(!m.expanded) }
	m.editor.actions["app.thinking.toggle"] = m.toggleThinking
	m.editor.actions["app.message.followUp"] = m.followUp
}

// ctrlC clears the editor; twice within half a second, it exits.
func (m *Mode) ctrlC() {
	now := time.Now()
	if now.Sub(m.lastCtrlC) < 500*time.Millisecond {
		m.shutdown()
		return
	}
	m.editor.SetText("")
	m.lastCtrlC = now
}

// interrupt stops the run in flight, putting messages still queued for it
// back in the editor.
func (m *Mode) interrupt() {
	if !m.running {
		if m.bashCancel != nil {
			m.bashCancel()
		}
		return
	}
	queued := append(append([]string(nil), m.steering...), m.followUps...)
	if len(queued) > 0 {
		current := m.editor.Text()
		parts := []string{strings.Join(queued, "\n\n")}
		if strings.TrimSpace(current) != "" {
			parts = append(parts, current)
		}
		m.editor.SetText(strings.Join(parts, "\n\n"))
		m.steering, m.followUps = nil, nil
		m.updatePending()
	}
	if m.cancel != nil {
		m.cancel()
	}
}

func (m *Mode) setExpanded(expanded bool) {
	m.expanded = expanded
	if m.headerText != nil {
		m.headerText.open = expanded
	}
	for _, c := range m.chat.Children() {
		switch c := c.(type) {
		case *ToolExecution:
			c.SetExpanded(expanded)
		case *CompactionSummary:
			c.SetExpanded(expanded)
		case *BashExecution:
			c.SetExpanded(expanded)
		}
	}
	m.ui.RequestRender()
}

func (m *Mode) toggleThinking() {
	m.hideThinking = !m.hideThinking
	for _, c := range m.chat.Children() {
		if a, ok := c.(*AssistantMessage); ok {
			a.SetHideThinking(m.hideThinking)
		}
	}
	m.ui.RequestRender()
}

func (m *Mode) shutdown() {
	if m.quitting {
		return
	}
	m.quitting = true
	if m.cancel != nil {
		m.cancel()
	}
	m.setStatus(nil)
	m.ui.Stop()
}

// --- input ---

// submit takes what the user entered: an answer to a question a command
// asked, a command, a message steering the run in flight, or a new prompt.
func (m *Mode) submit(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if m.confirming != nil {
		answer := m.confirming
		m.confirming = nil
		m.showOutput(m.env.Theme.Fg("dim", "> "+text))
		answer <- text
		return
	}
	if m.commandBusy {
		m.editor.SetText(text)
		m.showStatus("A command is still running")
		return
	}
	if strings.HasPrefix(text, "!") {
		exclude := strings.HasPrefix(text, "!!")
		command := strings.TrimSpace(strings.TrimLeft(text, "!"))
		if command != "" {
			if m.bashCancel != nil {
				m.editor.SetText(text)
				m.showWarning("A bash command is already running. Press " + m.env.KeyText("app.interrupt") + " to cancel it first.")
				return
			}
			m.editor.AddToHistory(text)
			m.runBash(command, exclude)
			return
		}
	}
	if strings.HasPrefix(text, "/") {
		if m.running {
			// A command acts on the conversation the run is writing.
			m.editor.SetText(text)
			m.showStatus("Wait for the agent to finish, or press " + m.env.KeyText("app.interrupt") + " to stop it")
			return
		}
		m.editor.AddToHistory(text)
		m.runCommand(text)
		return
	}
	m.editor.AddToHistory(text)
	if m.running {
		m.queue(text, false)
		return
	}
	m.startRun(text)
}

// followUp queues a message for when the run is done; with nothing
// running, it is an ordinary submit.
func (m *Mode) followUp() {
	text := strings.TrimSpace(m.editor.ExpandedText())
	if text == "" {
		return
	}
	m.editor.SetText("")
	if !m.running {
		m.submit(text)
		return
	}
	m.editor.AddToHistory(text)
	m.queue(text, true)
}

func (m *Mode) queue(text string, follow bool) {
	if follow {
		m.followUps = append(m.followUps, text)
	} else {
		m.steering = append(m.steering, text)
	}
	m.updatePending()
	if m.run == nil {
		m.unsent = append(m.unsent, queuedMessage{text, follow})
		return
	}
	m.send(m.run, text, follow)
}

// send hands a queued message to the run, off the loop: steering waits
// until the run has registered it.
func (m *Mode) send(run Run, text string, follow bool) {
	go func() {
		var err error
		if follow {
			err = run.Follow(text)
		} else {
			err = run.Steer(text)
		}
		if err != nil {
			m.ui.Do(func() { m.showError(err.Error()) })
		}
	}()
}

// updatePending shows the messages waiting for the run.
func (m *Mode) updatePending() {
	t := m.env.Theme
	m.pending.Clear()
	if len(m.steering) == 0 && len(m.followUps) == 0 {
		m.ui.RequestRender()
		return
	}
	m.pending.AddChild(&tui.Spacer{Lines: 1})
	for _, s := range m.steering {
		m.pending.AddChild(&tui.TruncatedText{Text: t.Fg("dim", "Steering: "+s), PaddingX: 1})
	}
	for _, f := range m.followUps {
		m.pending.AddChild(&tui.TruncatedText{Text: t.Fg("dim", "Follow-up: "+f), PaddingX: 1})
	}
	m.ui.RequestRender()
}

// deliverQueued moves queued messages the run has taken into the
// conversation, as the next request goes out with them.
func (m *Mode) deliverQueued() {
	if len(m.steering) == 0 && len(m.followUps) == 0 {
		return
	}
	conversation := m.host.Conversation()
	recent := map[string]int{}
	for i := len(conversation) - 1; i >= 0 && i >= len(conversation)-8; i-- {
		if conversation[i].Role == ai.RoleUser {
			recent[conversation[i].Content]++
		}
	}
	take := func(queue []string) []string {
		var kept []string
		for _, q := range queue {
			if recent[q] > 0 {
				recent[q]--
				m.addUserMessage(q)
				continue
			}
			kept = append(kept, q)
		}
		return kept
	}
	m.steering, m.followUps = take(m.steering), take(m.followUps)
	m.updatePending()
}

// nextInitial sends the next message given on the command line.
func (m *Mode) nextInitial() {
	if len(m.opts.InitialMessages) == 0 || m.running || m.quitting {
		return
	}
	next := m.opts.InitialMessages[0]
	m.opts.InitialMessages = m.opts.InitialMessages[1:]
	m.editor.AddToHistory(next)
	m.startRun(next)
}

func (m *Mode) startRun(prompt string) {
	m.addUserMessage(prompt)
	m.replyFailed = false
	ctx, cancel := context.WithCancel(context.Background())
	m.running, m.cancel = true, cancel
	m.setStatus(workingStatus(m.env, "Working..."))
	m.ui.RequestRender()

	go func() {
		run, err := m.host.Start(ctx, prompt)
		if err == nil {
			m.ui.Do(func() {
				m.run = run
				for _, q := range m.unsent {
					m.send(run, q.text, q.follow)
				}
				m.unsent = nil
			})
			err = run.Wait()
		}
		m.ui.Do(func() { m.runFinished(ctx, err) })
	}()
}

func (m *Mode) runFinished(ctx context.Context, err error) {
	cancelled := ctx.Err() != nil
	if m.cancel != nil {
		m.cancel()
	}
	m.running, m.cancel, m.run, m.unsent = false, nil, nil, nil
	m.clearStatus("working")
	m.flushPendingBash()
	m.streaming = nil
	if err != nil && !cancelled && !errors.Is(err, context.Canceled) && !m.replyFailed {
		m.showError(err.Error())
	}
	if len(m.steering) > 0 || len(m.followUps) > 0 {
		// Never delivered: back to the editor rather than lost.
		queued := append(append([]string(nil), m.steering...), m.followUps...)
		m.steering, m.followUps = nil, nil
		m.updatePending()
		m.editor.SetText(strings.Join(queued, "\n\n"))
	}
	if err == nil || !cancelled {
		m.nextInitial()
	}
	m.ui.RequestRender()
}

func (m *Mode) showWarning(text string) {
	m.chat.AddChild(&tui.Spacer{Lines: 1})
	m.chat.AddChild(tui.NewText(m.env.Theme.Fg("warning", "Warning: "+text), 1, 0, nil))
	m.ui.RequestRender()
}

// runBash runs a shell command off the loop, its output drawn as it
// arrives. During an agent run it waits with the queued messages, so the
// conversation above reads in the order things were said.
func (m *Mode) runBash(command string, exclude bool) {
	b := NewBashExecution(m.env, command, exclude)
	b.SetExpanded(m.expanded)
	if m.running {
		m.pendingBash = append(m.pendingBash, b)
		m.pending.AddChild(b)
	} else {
		m.chat.AddChild(b)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.bashCancel = cancel
	m.ui.RequestRender()
	go func() {
		result, err := m.host.Bash(ctx, command, exclude, func(chunk string) {
			m.ui.Do(func() { b.AppendOutput(chunk) })
		})
		m.ui.Do(func() {
			cancel()
			m.bashCancel = nil
			if err != nil {
				b.SetComplete(tools.BashResult{})
				m.showError("Bash command failed: " + err.Error())
				return
			}
			b.SetComplete(result)
		})
	}()
}

// flushPendingBash moves shell commands run during the agent's run into the
// conversation.
func (m *Mode) flushPendingBash() {
	for _, b := range m.pendingBash {
		m.pending.RemoveChild(b)
		m.chat.AddChild(b)
	}
	m.pendingBash = nil
}

// runCommand runs a command off the loop, showing what it says when done.
// A question it asks is answered by the next thing the user submits.
func (m *Mode) runCommand(text string) {
	m.commandBusy = true
	confirm := func(question string) bool {
		answer := make(chan string, 1)
		m.ui.Do(func() {
			m.showOutput(question)
			m.confirming = answer
		})
		a := strings.ToLower(strings.TrimSpace(<-answer))
		return a == "y" || a == "yes"
	}
	before := fingerprint(m.host.Conversation())
	go func() {
		var out bytes.Buffer
		handled, quit := m.host.Command(context.Background(), text, &out, confirm)
		m.ui.Do(func() {
			m.commandBusy = false
			if !handled {
				if m.running {
					m.queue(text, false)
				} else {
					m.startRun(text)
				}
				return
			}
			if quit {
				m.shutdown()
				return
			}
			// A command that replaced the conversation — a new session, a
			// resumed one, a compaction — has it drawn again.
			if fingerprint(m.host.Conversation()) != before {
				m.compacted, m.contextTokens, m.contextKnown = false, 0, false
				m.renderHistory()
			}
			m.showOutput(out.String())
		})
	}()
}

// fingerprint identifies a conversation's content, to tell when a command
// replaced it.
func fingerprint(conversation []ai.Message) string {
	var b strings.Builder
	b.WriteString(itoa(len(conversation)))
	for _, msg := range conversation {
		b.WriteString("\x00")
		b.WriteString(string(msg.Role))
		b.WriteString(msg.Content)
	}
	return b.String()
}

// --- events ---

func (m *Mode) toolFor(id, name, args string) *ToolExecution {
	if x, ok := m.tools[id]; ok {
		return x
	}
	x := NewToolExecution(m.env, name, args)
	x.SetExpanded(m.expanded)
	m.chat.AddChild(x)
	m.tools[id] = x
	return x
}

func (m *Mode) handleEvent(e events.Event) {
	switch e.Kind {
	case events.KindAgentStart:
		if m.activeStatus == nil {
			m.setStatus(workingStatus(m.env, "Working..."))
		}
	case events.KindModelRequest:
		m.deliverQueued()
	case events.KindToolStart:
		m.toolFor(e.ToolCallID, e.ToolName, e.Detail.Args).MarkExecutionStarted()
	case events.KindToolEnd:
		if e.Detail.Err != "" {
			m.toolErrors[e.ToolCallID] = e.Detail.Err
		}
	case events.KindToolResult:
		x := m.toolFor(e.ToolCallID, e.ToolName, e.Detail.Args)
		_, failed := m.toolErrors[e.ToolCallID]
		x.UpdateResult(ToolResult{Text: e.Detail.Result, Details: e.Detail.ToolDetails, IsError: failed}, false)
		delete(m.tools, e.ToolCallID)
		delete(m.toolErrors, e.ToolCallID)
	case events.KindCompactionStart:
		m.beforeCompact = m.contextTokens
		m.setStatus(compactionStatus(m.env, e.Detail.Reason))
	case events.KindCompactionEnd:
		m.clearStatus("compaction")
		if e.Detail.Err != "" {
			m.chat.AddChild(&tui.Spacer{Lines: 1})
			m.chat.AddChild(tui.NewText(m.env.Theme.Fg("error", e.Detail.Err), 1, 0, nil))
		} else {
			m.contextTokens, m.contextKnown, m.compacted = 0, false, true
			m.renderHistory()
		}
		if m.running {
			m.setStatus(workingStatus(m.env, "Working..."))
		}
	case events.KindRetryStart:
		m.setStatus(retryStatus(m.env, e.Detail.Attempt, e.Detail.MaxAttempts, time.Duration(e.Detail.DelayMs)*time.Millisecond))
	case events.KindRetryEnd:
		m.clearStatus("retry")
		if e.Detail.Succeeded == nil || !*e.Detail.Succeeded {
			reason := e.Detail.Err
			if reason == "" {
				reason = "Unknown error"
			}
			m.showError("Retry failed after " + itoa(e.Detail.Attempt) + " attempts: " + reason)
		}
		if m.running {
			m.setStatus(workingStatus(m.env, "Working..."))
		}
	case events.KindAgentEnd:
		m.clearStatus("working")
	}
	m.ui.RequestRender()
}

func (m *Mode) handleReply(e ai.StreamEvent) {
	if e.Terminal() {
		final := e.Final
		if final == nil {
			return
		}
		if m.streaming == nil {
			m.streaming = NewAssistantMessage(m.env, nil, m.hideThinking, 1)
			m.chat.AddChild(m.streaming)
		}
		if final.StopReason == ai.StopAborted {
			copied := *final
			copied.ErrorMessage = "Operation aborted"
			final = &copied
		}
		m.streaming.Update(final)
		m.syncToolCalls(final)
		m.replyFailed = final.StopReason == ai.StopError || final.StopReason == ai.StopAborted
		for id, x := range m.tools {
			if final.StopReason == ai.StopAborted || final.StopReason == ai.StopError {
				reason := final.ErrorMessage
				if reason == "" {
					reason = "Error"
				}
				x.UpdateResult(ToolResult{Text: reason, IsError: true}, false)
				delete(m.tools, id)
			} else {
				x.SetArgsComplete()
			}
		}
		if final.Usage.Reported {
			m.contextTokens, m.contextKnown = final.Usage.InputTokens+final.Usage.OutputTokens, true
		}
		m.streaming = nil
		m.ui.RequestRender()
		return
	}
	if e.Partial == nil {
		return
	}
	if m.streaming == nil {
		m.streaming = NewAssistantMessage(m.env, nil, m.hideThinking, 1)
		m.chat.AddChild(m.streaming)
	}
	m.streaming.Update(e.Partial)
	m.syncToolCalls(e.Partial)
	m.ui.RequestRender()
}

// syncToolCalls draws each tool call of the reply as soon as it appears,
// updating its arguments as they arrive.
func (m *Mode) syncToolCalls(reply *ai.AssistantMessage) {
	for _, b := range reply.Blocks {
		if b.Kind != ai.BlockToolCall || b.Call.ID == "" {
			continue
		}
		if x, ok := m.tools[b.Call.ID]; ok {
			if x.rawArg != b.Call.Args {
				x.UpdateArgs(b.Call.Args)
			}
			continue
		}
		m.toolFor(b.Call.ID, b.Call.Name, b.Call.Args)
	}
}
