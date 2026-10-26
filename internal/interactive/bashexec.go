package interactive

import (
	"strings"

	"github.com/iamclancyliang/pi-go/internal/tools"
	"github.com/iamclancyliang/pi-go/internal/tui"
)

// bashPreview is how many lines of a shell command's output show folded.
const bashPreview = 20

// BashExecution is a shell command the user ran with "!" — Pi's
// BashExecutionComponent: the command between two rules, its output as it
// arrives, a spinner while it runs, then how it ended.
type BashExecution struct {
	env      *Env
	command  string
	children tui.Container
	content  tui.Container
	loader   *tui.Loader
	lines    []string
	expanded bool

	running        bool
	exitCode       *int
	cancelled      bool
	truncated      bool
	fullOutputPath string
}

// NewBashExecution draws command, running. exclude marks a "!!" command,
// kept out of the model's context, whose rules are dim.
func NewBashExecution(env *Env, command string, exclude bool) *BashExecution {
	t := env.Theme
	color := "bashMode"
	if exclude {
		color = "dim"
	}
	b := &BashExecution{env: env, command: command, running: true}
	b.loader = tui.NewLoader(renderFunc(env.requestRender), t.FgStyle(color), t.FgStyle("muted"),
		"Running... ("+env.KeyText("tui.select.cancel")+" to cancel)", nil, 0)
	border := &rule{style: t.FgStyle(color)}
	b.children.AddChild(&tui.Spacer{Lines: 1})
	b.children.AddChild(border)
	b.children.AddChild(&b.content)
	b.children.AddChild(border)
	b.update()
	return b
}

// rule is a line across the width.
type rule struct{ style tui.Style }

func (r *rule) Invalidate() {}
func (r *rule) Render(width int) []string {
	return []string{r.style(strings.Repeat("─", max(1, width)))}
}

// AppendOutput adds output as it arrives; a line may arrive in pieces.
func (b *BashExecution) AppendOutput(chunk string) {
	clean := strings.ReplaceAll(tui.StripSequences(chunk), "\r\n", "\n")
	clean = strings.ReplaceAll(clean, "\r", "\n")
	parts := strings.Split(clean, "\n")
	if len(b.lines) > 0 {
		b.lines[len(b.lines)-1] += parts[0]
		b.lines = append(b.lines, parts[1:]...)
	} else {
		b.lines = append(b.lines, parts...)
	}
	b.update()
}

// SetComplete records how the command ended.
func (b *BashExecution) SetComplete(result tools.BashResult) {
	b.running = false
	b.exitCode, b.cancelled = result.ExitCode, result.Cancelled
	b.truncated, b.fullOutputPath = result.Truncated, result.FullOutputPath
	b.loader.Stop()
	b.update()
}

// SetExpanded shows the output in full or folded to its last lines.
func (b *BashExecution) SetExpanded(expanded bool) {
	b.expanded = expanded
	b.update()
}

func (b *BashExecution) Invalidate()               { b.update() }
func (b *BashExecution) Render(width int) []string { return b.children.Render(width) }

func (b *BashExecution) update() {
	t := b.env.Theme
	// Held to what the model would be given of it, as the bash tool is.
	cut := tools.TruncateTail(strings.Join(b.lines, "\n"), tools.Limits{})
	var available []string
	if cut.Content != "" {
		available = strings.Split(cut.Content, "\n")
	}
	preview := available
	if len(preview) > bashPreview {
		preview = preview[len(preview)-bashPreview:]
	}
	hidden := len(available) - len(preview)

	b.content.Clear()
	b.content.AddChild(tui.NewText(t.Fg("bashMode", Bold("$ "+b.command)), 1, 0, nil))
	if len(available) > 0 {
		style := func(lines []string) string {
			styled := make([]string, len(lines))
			for i, l := range lines {
				styled[i] = t.Fg("muted", l)
			}
			return "\n" + strings.Join(styled, "\n")
		}
		if b.expanded {
			b.content.AddChild(tui.NewText(style(available), 1, 0, nil))
		} else {
			b.content.AddChild(&lastLines{text: style(preview), max: bashPreview})
		}
	}

	if b.running {
		b.content.AddChild(b.loader)
		return
	}
	var status []string
	if hidden > 0 {
		if b.expanded {
			status = append(status, t.Fg("muted", "(")+b.env.KeyHint("app.tools.expand", "to collapse")+t.Fg("muted", ")"))
		} else {
			status = append(status, t.Fg("muted", "... "+itoa(hidden)+" more lines (")+b.env.KeyHint("app.tools.expand", "to expand")+t.Fg("muted", ")"))
		}
	}
	switch {
	case b.cancelled:
		status = append(status, t.Fg("warning", "(cancelled)"))
	case b.exitCode != nil && *b.exitCode != 0:
		status = append(status, t.Fg("error", "(exit "+itoa(*b.exitCode)+")"))
	}
	if (b.truncated || cut.Truncated) && b.fullOutputPath != "" {
		status = append(status, t.Fg("warning", "Output truncated. Full output: "+b.fullOutputPath))
	}
	if len(status) > 0 {
		b.content.AddChild(tui.NewText("\n"+strings.Join(status, "\n"), 1, 0, nil))
	}
}

// lastLines is text kept to its last max lines at the width it is drawn
// at.
type lastLines struct {
	text string
	max  int
}

func (l *lastLines) Invalidate() {}
func (l *lastLines) Render(width int) []string {
	all := tui.NewText(l.text, 1, 0, nil).Render(width)
	if len(all) > l.max {
		return all[len(all)-l.max:]
	}
	return all
}
