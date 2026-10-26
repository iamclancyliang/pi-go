package interactive

import (
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/tui"
)

// OSC 133 marks a message as a prompt zone for terminals that support
// jumping between prompts: start, end of prompt, start of output.
const (
	osc133Start = "\x1b]133;A\x07"
	osc133End   = "\x1b]133;B\x07"
	osc133Final = "\x1b]133;C\x07"
)

func markZone(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	out := append([]string(nil), lines...)
	out[0] = osc133Start + out[0]
	out[len(out)-1] = osc133End + osc133Final + out[len(out)-1]
	return out
}

// UserMessage is a message the user sent, on its own background — Pi's
// UserMessageComponent. Its Markdown keeps list markers and backslash
// escapes as typed.
type UserMessage struct {
	box *tui.Box
}

// NewUserMessage draws text with outputPad blank columns either side.
func NewUserMessage(env *Env, text string, outputPad int) *UserMessage {
	box := tui.NewBox(outputPad, 1, env.Theme.BgStyle("userMessageBg"))
	box.AddChild(tui.NewMarkdown(text, 0, 0, env.Theme.MarkdownTheme(),
		&tui.DefaultTextStyle{Color: env.Theme.FgStyle("userMessageText")},
		tui.MarkdownOptions{PreserveOrderedListMarkers: true, PreserveBackslashEscapes: true}))
	return &UserMessage{box: box}
}

func (u *UserMessage) Invalidate()               { u.box.Invalidate() }
func (u *UserMessage) Render(width int) []string { return markZone(u.box.Render(width)) }

// AssistantMessage is a reply as it arrives and once complete — Pi's
// AssistantMessageComponent: its text as Markdown, its thinking in muted
// italics or folded to a label, and why it ended when that was a failure.
// Its tool calls are drawn by their own components.
type AssistantMessage struct {
	env           *Env
	content       tui.Container
	message       *ai.AssistantMessage
	hideThinking  bool
	thinkingLabel string
	outputPad     int
	hasToolCalls  bool
}

// NewAssistantMessage draws message, which may be nil until the reply
// begins.
func NewAssistantMessage(env *Env, message *ai.AssistantMessage, hideThinking bool, outputPad int) *AssistantMessage {
	a := &AssistantMessage{env: env, hideThinking: hideThinking, thinkingLabel: "Thinking...", outputPad: outputPad}
	if message != nil {
		a.Update(message)
	}
	return a
}

// SetHideThinking folds or unfolds the thinking.
func (a *AssistantMessage) SetHideThinking(hide bool) {
	a.hideThinking = hide
	if a.message != nil {
		a.Update(a.message)
	}
}

// SetThinkingLabel is what folded thinking shows.
func (a *AssistantMessage) SetThinkingLabel(label string) {
	a.thinkingLabel = label
	if a.message != nil {
		a.Update(a.message)
	}
}

func (a *AssistantMessage) Invalidate() {
	if a.message != nil {
		a.Update(a.message)
	}
}

func (a *AssistantMessage) Render(width int) []string {
	lines := a.content.Render(width)
	if a.hasToolCalls {
		return lines
	}
	return markZone(lines)
}

func visibleBlock(b ai.Block) bool {
	return (b.Kind == ai.BlockText || b.Kind == ai.BlockThinking) && strings.TrimSpace(b.Text) != ""
}

// Update redraws from message, the reply as it now stands.
func (a *AssistantMessage) Update(message *ai.AssistantMessage) {
	a.message = message
	a.content.Clear()
	t := a.env.Theme
	blocks := message.Blocks

	for _, b := range blocks {
		if visibleBlock(b) {
			a.content.AddChild(&tui.Spacer{Lines: 1})
			break
		}
	}

	for i := 0; i < len(blocks); i++ {
		b := blocks[i]
		switch {
		case b.Kind == ai.BlockText && strings.TrimSpace(b.Text) != "":
			a.content.AddChild(tui.NewMarkdown(strings.TrimSpace(b.Text), a.outputPad, 0, t.MarkdownTheme(), nil, tui.MarkdownOptions{}))
		case b.Kind == ai.BlockThinking:
			// A run of thinking blocks is drawn as one section.
			var run []string
			for ; i < len(blocks) && blocks[i].Kind == ai.BlockThinking; i++ {
				if text := strings.TrimSpace(blocks[i].Text); text != "" {
					run = append(run, text)
				}
			}
			i--
			if len(run) == 0 {
				continue
			}
			if a.hideThinking {
				a.content.AddChild(tui.NewText(Italic(t.Fg("thinkingText", a.thinkingLabel)), a.outputPad, 0, nil))
			} else {
				a.content.AddChild(tui.NewMarkdown(strings.Join(run, "\n\n"), a.outputPad, 0, t.MarkdownTheme(),
					&tui.DefaultTextStyle{Color: t.FgStyle("thinkingText"), Italic: true}, tui.MarkdownOptions{}))
			}
			// A gap only before more visible content: a tool call that
			// follows is drawn apart and brings its own.
			for _, after := range blocks[i+1:] {
				if visibleBlock(after) {
					a.content.AddChild(&tui.Spacer{Lines: 1})
					break
				}
			}
		}
	}

	a.hasToolCalls = false
	for _, b := range blocks {
		if b.Kind == ai.BlockToolCall {
			a.hasToolCalls = true
		}
	}
	errorLine := func(text string) {
		a.content.AddChild(&tui.Spacer{Lines: 1})
		a.content.AddChild(tui.NewText(t.Fg("error", text), a.outputPad, 0, nil))
	}
	switch {
	case message.StopReason == ai.StopLength:
		// A length stop can come before a tool call is complete, so it is
		// shown even with tool calls.
		errorLine("Response was truncated before completion.")
	case a.hasToolCalls:
		// A failed tool call shows its own error.
	case message.StopReason == ai.StopAborted:
		text := "Operation aborted"
		if message.ErrorMessage != "" && message.ErrorMessage != "Request was aborted" {
			text = message.ErrorMessage
		}
		errorLine(text)
	case message.StopReason == ai.StopError:
		text := message.ErrorMessage
		if text == "" {
			text = "Unknown error"
		}
		errorLine("Error: " + text)
	}
}

// CompactionSummary is where the conversation was compacted — Pi's
// CompactionSummaryMessageComponent: collapsed to a line naming the size it
// was compacted from, or expanded to the summary.
type CompactionSummary struct {
	env          *Env
	box          *tui.Box
	tokensBefore int
	summary      string
	expanded     bool
}

// NewCompactionSummary shows a compaction of tokensBefore tokens to summary.
func NewCompactionSummary(env *Env, tokensBefore int, summary string) *CompactionSummary {
	c := &CompactionSummary{env: env, tokensBefore: tokensBefore, summary: summary,
		box: tui.NewBox(1, 1, env.Theme.BgStyle("customMessageBg"))}
	c.update()
	return c
}

// SetExpanded shows or folds the summary.
func (c *CompactionSummary) SetExpanded(expanded bool) {
	c.expanded = expanded
	c.update()
}

func (c *CompactionSummary) Invalidate()               { c.update() }
func (c *CompactionSummary) Render(width int) []string { return c.box.Render(width) }

func (c *CompactionSummary) update() {
	t := c.env.Theme
	c.box.Clear()
	tokens := groupThousands(c.tokensBefore)
	c.box.AddChild(tui.NewText(t.Fg("customMessageLabel", "\x1b[1m[compaction]\x1b[22m"), 0, 0, nil))
	c.box.AddChild(&tui.Spacer{Lines: 1})
	if c.expanded {
		c.box.AddChild(tui.NewMarkdown("**Compacted from "+tokens+" tokens**\n\n"+c.summary, 0, 0, t.MarkdownTheme(),
			&tui.DefaultTextStyle{Color: t.FgStyle("customMessageText")}, tui.MarkdownOptions{}))
		return
	}
	c.box.AddChild(tui.NewText(
		t.Fg("customMessageText", "Compacted from "+tokens+" tokens (")+
			t.Fg("dim", c.env.KeyText("app.tools.expand"))+
			t.Fg("customMessageText", " to expand)"), 0, 0, nil))
}

// groupThousands writes n with commas between thousands, as
// toLocaleString does in an English locale.
func groupThousands(n int) string {
	s := strings.TrimPrefix(itoa(n), "-")
	var b strings.Builder
	if n < 0 {
		b.WriteByte('-')
	}
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
