package interactive

import (
	"bytes"
	"encoding/json"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iamclancyliang/pi-go/internal/tools"
	"github.com/iamclancyliang/pi-go/internal/tui"
)

// ToolResult is what a tool call produced, as the interface draws it.
type ToolResult struct {
	Text    string
	Details any
	IsError bool
}

// ToolExecution is one tool call, from its arguments arriving to its result
// — Pi's ToolExecutionComponent. A built-in tool is drawn by its own
// renderer in a box coloured by its state: pending, succeeded, failed. A
// tool with no renderer shows its name, its arguments as JSON and its
// output.
type ToolExecution struct {
	env    *Env
	name   string
	rawArg string
	args   map[string]any

	expanded         bool
	isPartial        bool
	argsComplete     bool
	executionStarted bool
	result           *ToolResult

	renderer toolRenderer
	children tui.Container
	box      *tui.Box
	text     *tui.Text
	self     tui.Container
	hidden   bool
}

// toolRenderer draws one tool's call and result. self reports a renderer
// that frames itself rather than sitting in the shared box.
type toolRenderer interface {
	self() bool
	call(x *ToolExecution) tui.Component
	result(x *ToolExecution) tui.Component
}

func rendererFor(name string) toolRenderer {
	switch name {
	case "read":
		return &readRenderer{}
	case "write":
		return &writeRenderer{}
	case "edit":
		return &editRenderer{}
	case "bash":
		return &bashRenderer{}
	case "grep":
		return &textRenderer{callText: formatGrepCall, resultText: formatGrepResult}
	case "find":
		return &textRenderer{callText: formatFindCall, resultText: formatFindResult}
	case "ls":
		return &textRenderer{callText: formatLsCall, resultText: formatLsResult}
	}
	return nil
}

// NewToolExecution draws a call to the named tool with its arguments as
// JSON.
func NewToolExecution(env *Env, name, args string) *ToolExecution {
	x := &ToolExecution{env: env, name: name, isPartial: true, renderer: rendererFor(name)}
	x.setArgs(args)
	x.children.AddChild(&tui.Spacer{Lines: 1})
	pending := env.Theme.BgStyle("toolPendingBg")
	switch {
	case x.renderer == nil:
		x.text = tui.NewText("", 1, 1, pending)
		x.children.AddChild(x.text)
	case !x.renderer.self():
		x.box = tui.NewBox(1, 1, pending)
		x.children.AddChild(x.box)
	}
	x.update()
	return x
}

func (x *ToolExecution) setArgs(args string) {
	x.rawArg = args
	x.args = map[string]any{}
	_ = json.Unmarshal([]byte(args), &x.args)
}

// UpdateArgs replaces the arguments, as they stream in.
func (x *ToolExecution) UpdateArgs(args string) {
	x.setArgs(args)
	x.update()
}

// SetArgsComplete marks the arguments final, which lets an edit work out
// its diff.
func (x *ToolExecution) SetArgsComplete() {
	x.argsComplete = true
	x.update()
	x.env.requestRender()
}

// MarkExecutionStarted marks the call running, which starts a command's
// clock.
func (x *ToolExecution) MarkExecutionStarted() {
	x.executionStarted = true
	x.update()
	x.env.requestRender()
}

// UpdateResult shows a result; partial is output still arriving.
func (x *ToolExecution) UpdateResult(result ToolResult, partial bool) {
	x.result = &result
	x.isPartial = partial
	x.update()
}

// SetExpanded shows a result in full or folded.
func (x *ToolExecution) SetExpanded(expanded bool) {
	x.expanded = expanded
	x.update()
}

func (x *ToolExecution) Invalidate() {
	x.children.Invalidate()
	x.update()
}

func (x *ToolExecution) Render(width int) []string {
	if x.hidden {
		return nil
	}
	if x.renderer != nil && x.renderer.self() {
		lines := x.self.Render(width)
		if len(lines) == 0 {
			return nil
		}
		return append([]string{""}, lines...)
	}
	return x.children.Render(width)
}

func (x *ToolExecution) isError() bool { return x.result != nil && x.result.IsError }

func (x *ToolExecution) background() tui.Style {
	switch {
	case x.isPartial:
		return x.env.Theme.BgStyle("toolPendingBg")
	case x.isError():
		return x.env.Theme.BgStyle("toolErrorBg")
	}
	return x.env.Theme.BgStyle("toolSuccessBg")
}

func (x *ToolExecution) update() {
	x.hidden = false
	if x.renderer == nil {
		x.text.SetBackground(x.background())
		x.text.SetText(x.formatFallback())
		return
	}
	target := &x.self
	if x.box != nil {
		x.box.SetBackground(x.background())
		target = &x.box.Container
	}
	target.Clear()
	target.AddChild(x.renderer.call(x))
	if x.result != nil {
		if c := x.renderer.result(x); c != nil {
			target.AddChild(c)
		}
	}
}

// formatFallback draws a tool with no renderer: its name, its arguments
// and its output.
func (x *ToolExecution) formatFallback() string {
	t := x.env.Theme
	text := t.Fg("toolTitle", Bold(x.name))
	if args := indentJSON(x.rawArg); args != "" {
		text += "\n\n" + args
	}
	if out := x.textOutput(); out != "" {
		text += "\n" + out
	}
	return text
}

// indentJSON lays arguments out with two-space indents, keys in their
// order, as JSON.stringify does.
func indentJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "{}"
	}
	var b bytes.Buffer
	if err := json.Indent(&b, []byte(raw), "", "  "); err != nil {
		return raw
	}
	return b.String()
}

// textOutput is the result's text with escape sequences, carriage returns
// and other control characters taken out.
func (x *ToolExecution) textOutput() string {
	if x.result == nil {
		return ""
	}
	return sanitizeOutput(x.result.Text)
}

func sanitizeOutput(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n':
			return r
		case r == '\r', r <= 0x1f, r >= 0xfff9 && r <= 0xfffb:
			return -1
		}
		return r
	}, tui.StripSequences(s))
}

// --- shared formatting ---

// argString reads an argument the way Pi's renderers do: a string is
// itself, a missing one is empty, and anything else is invalid.
func (x *ToolExecution) argString(names ...string) (string, bool) {
	for _, name := range names {
		v, ok := x.args[name]
		if !ok || v == nil {
			continue
		}
		s, isString := v.(string)
		return s, isString
	}
	return "", true
}

func (x *ToolExecution) argPresent(name string) bool {
	v, ok := x.args[name]
	return ok && v != nil
}

// jsValue writes an argument as JavaScript's String() would.
func jsValue(v any) string {
	switch v := v.(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return "null"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func jsNumber(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

func invalidArg(t *Theme) string { return t.Fg("error", "[invalid arg]") }

func (e *Env) shortenPath(p string) string {
	if e.Home != "" && strings.HasPrefix(p, e.Home) {
		return "~" + p[len(e.Home):]
	}
	return p
}

// resolvePath resolves a path argument against the working directory, with
// "~" for the home directory and an "@" mention marker dropped.
func (e *Env) resolvePath(p string) string {
	p = strings.TrimPrefix(p, "@")
	switch {
	case p == "~":
		p = e.Home
	case strings.HasPrefix(p, "~/"):
		p = filepath.Join(e.Home, p[2:])
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(e.Cwd, p)
}

// relativeOrAbsolute is a path relative to the working directory when it
// is inside it, and absolute otherwise.
func (e *Env) relativeOrAbsolute(p string) string {
	abs := e.resolvePath(p)
	if rel, err := filepath.Rel(e.Cwd, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel) {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(abs)
}

// linkPath makes styled text a link to the file, where the terminal draws
// links.
func (e *Env) linkPath(styled, raw string) string {
	if !tui.Capabilities().Hyperlinks {
		return styled
	}
	return tui.Hyperlink(styled, (&url.URL{Scheme: "file", Path: e.resolvePath(raw)}).String())
}

func (x *ToolExecution) renderToolPath(raw string, valid bool, emptyFallback string) string {
	t := x.env.Theme
	if !valid {
		return invalidArg(t)
	}
	value := raw
	if value == "" {
		value = emptyFallback
	}
	if value == "" {
		return t.Fg("toolOutput", "...")
	}
	return x.env.linkPath(t.Fg("accent", x.env.shortenPath(value)), value)
}

func replaceTabs(s string) string { return strings.ReplaceAll(s, "\t", "   ") }

func trimTrailingEmptyLines(lines []string) []string {
	end := len(lines)
	for end > 0 && lines[end-1] == "" {
		end--
	}
	return lines[:end]
}

// previewLines shows the first max lines of output, or all of them
// expanded, with a hint naming how many more there are.
func (x *ToolExecution) previewLines(lines []string, max int, style func(string) string, more func(remaining int) string) string {
	t := x.env.Theme
	shown := lines
	if !x.expanded && len(lines) > max {
		shown = lines[:max]
	}
	styled := make([]string, len(shown))
	for i, l := range shown {
		styled[i] = style(l)
	}
	text := "\n" + strings.Join(styled, "\n")
	if remaining := len(lines) - len(shown); remaining > 0 {
		text += t.Fg("muted", "\n... ("+more(remaining)+",") + " " + x.env.KeyHint("app.tools.expand", "to expand") + t.Fg("muted", ")")
	}
	return text
}

func itoa(n int) string { return strconv.Itoa(n) }

func truncationOf(details any) *tools.Truncation {
	switch d := details.(type) {
	case tools.ReadDetails:
		return d.Truncation
	case tools.BashDetails:
		return d.Truncation
	case tools.GrepDetails:
		return d.Truncation
	case tools.FindDetails:
		return d.Truncation
	case tools.LsDetails:
		return d.Truncation
	}
	return nil
}

func maxBytesOf(cut *tools.Truncation) int {
	if cut.MaxBytes > 0 {
		return cut.MaxBytes
	}
	return tools.DefaultMaxBytes
}

// textRenderer is a tool drawn as two texts.
type textRenderer struct {
	callText   func(x *ToolExecution) string
	resultText func(x *ToolExecution) string
}

func (r *textRenderer) self() bool { return false }
func (r *textRenderer) call(x *ToolExecution) tui.Component {
	return tui.NewText(r.callText(x), 0, 0, nil)
}
func (r *textRenderer) result(x *ToolExecution) tui.Component {
	return tui.NewText(r.resultText(x), 0, 0, nil)
}

// --- read ---

type readRenderer struct{}

func (readRenderer) self() bool { return false }

var compactResourceNames = map[string]bool{
	"AGENTS.override.md": true, "AGENTS.md": true, "AGENTS.MD": true, "CLAUDE.md": true, "CLAUDE.MD": true,
}

func (x *ToolExecution) readLineRange() string {
	offset, hasOffset := jsNumber(x.args["offset"])
	limit, hasLimit := jsNumber(x.args["limit"])
	if !x.argPresent("offset") && !x.argPresent("limit") {
		return ""
	}
	start := 1.0
	if hasOffset {
		start = offset
	}
	text := ":" + jsValue(start)
	if hasLimit && start+limit-1 != 0 {
		text += "-" + jsValue(start+limit-1)
	}
	return x.env.Theme.Fg("warning", text)
}

func (r readRenderer) call(x *ToolExecution) tui.Component {
	t := x.env.Theme
	raw, valid := x.argString("file_path", "path")
	if !x.expanded && valid && raw != "" {
		abs := x.env.resolvePath(raw)
		name := filepath.Base(abs)
		hint := t.Fg("dim", " ("+x.env.KeyText("app.tools.expand")+" to expand)")
		switch {
		case name == "SKILL.md":
			label := filepath.Base(filepath.Dir(abs))
			if label == "" || label == "." || label == "/" {
				label = name
			}
			return tui.NewText(t.Fg("customMessageLabel", "\x1b[1m[skill]\x1b[22m ")+t.Fg("customMessageText", label)+
				x.readLineRange()+hint, 0, 0, nil)
		case compactResourceNames[name]:
			return tui.NewText(t.Fg("toolTitle", Bold("read resource"))+" "+t.Fg("accent", x.env.relativeOrAbsolute(raw))+
				x.readLineRange()+hint, 0, 0, nil)
		}
	}
	return tui.NewText(t.Fg("toolTitle", Bold("read"))+" "+x.renderToolPath(raw, valid, "")+x.readLineRange(), 0, 0, nil)
}

func (r readRenderer) result(x *ToolExecution) tui.Component {
	t := x.env.Theme
	if !x.expanded && !x.isError() {
		return tui.NewText("", 0, 0, nil)
	}
	raw, _ := x.argString("file_path", "path")
	style := func(l string) string { return t.Fg("toolOutput", replaceTabs(l)) }
	output := x.textOutput()
	if !x.isError() && knownLanguage(languageFromPath(raw)) {
		// Pi highlights a file in a language it knows; see highlightCode.
		output, style = replaceTabs(output), replaceTabs
	}
	lines := trimTrailingEmptyLines(strings.Split(output, "\n"))
	text := x.previewLines(lines, 10, style, func(n int) string { return itoa(n) + " more lines" })
	if cut := truncationOf(x.result.Details); cut != nil && cut.Truncated {
		switch {
		case cut.FirstLineExceedsLimit:
			text += "\n" + t.Fg("warning", "[First line exceeds "+tools.FormatSize(maxBytesOf(cut))+" limit]")
		case cut.By == tools.TruncatedByLines:
			maxLines := cut.MaxLines
			if maxLines == 0 {
				maxLines = tools.DefaultMaxLines
			}
			text += "\n" + t.Fg("warning", "[Truncated: showing "+itoa(cut.OutputLines)+" of "+itoa(cut.TotalLines)+
				" lines ("+itoa(maxLines)+" line limit)]")
		default:
			text += "\n" + t.Fg("warning", "[Truncated: "+itoa(cut.OutputLines)+" lines shown ("+
				tools.FormatSize(maxBytesOf(cut))+" limit)]")
		}
	}
	return tui.NewText(text, 0, 0, nil)
}

// --- write ---

type writeRenderer struct{}

func (writeRenderer) self() bool { return false }

func (writeRenderer) call(x *ToolExecution) tui.Component {
	t := x.env.Theme
	raw, valid := x.argString("file_path", "path")
	text := t.Fg("toolTitle", Bold("write")) + " " + x.renderToolPath(raw, valid, "")
	content, contentValid := x.argString("content")
	switch {
	case !contentValid:
		text += "\n\n" + t.Fg("error", "[invalid content arg - expected string]")
	case content != "":
		content = strings.ReplaceAll(content, "\r", "")
		style := func(l string) string { return t.Fg("toolOutput", replaceTabs(l)) }
		if knownLanguage(languageFromPath(raw)) {
			// Pi highlights a file in a language it knows; see highlightCode.
			content, style = replaceTabs(content), func(l string) string { return l }
		}
		lines := trimTrailingEmptyLines(strings.Split(content, "\n"))
		total := len(lines)
		text += "\n" + x.previewLines(lines, 10, style,
			func(n int) string { return itoa(n) + " more lines, " + itoa(total) + " total" })
	}
	return tui.NewText(text, 0, 0, nil)
}

func (writeRenderer) result(x *ToolExecution) tui.Component {
	if !x.isError() || x.result.Text == "" {
		return &tui.Container{}
	}
	return tui.NewText("\n"+x.env.Theme.Fg("error", x.result.Text), 0, 0, nil)
}

// --- grep, find, ls ---

func formatGrepCall(x *ToolExecution) string {
	t := x.env.Theme
	pattern, patternValid := x.argString("pattern")
	raw, pathValid := x.argString("path")
	text := t.Fg("toolTitle", Bold("grep")) + " "
	if patternValid {
		text += t.Fg("accent", "/"+pattern+"/")
	} else {
		text += invalidArg(t)
	}
	path := invalidArg(t)
	if pathValid {
		if raw == "" {
			raw = "."
		}
		path = x.env.shortenPath(raw)
	}
	text += t.Fg("toolOutput", " in "+path)
	if glob, _ := x.argString("glob"); glob != "" {
		text += t.Fg("toolOutput", " ("+glob+")")
	}
	if x.argPresent("limit") {
		text += t.Fg("toolOutput", " limit "+jsValue(x.args["limit"]))
	}
	return text
}

func formatFindCall(x *ToolExecution) string {
	t := x.env.Theme
	pattern, patternValid := x.argString("pattern")
	raw, pathValid := x.argString("path")
	text := t.Fg("toolTitle", Bold("find")) + " "
	if patternValid {
		text += t.Fg("accent", pattern)
	} else {
		text += invalidArg(t)
	}
	path := invalidArg(t)
	if pathValid {
		if raw == "" {
			raw = "."
		}
		path = x.env.shortenPath(raw)
	}
	text += t.Fg("toolOutput", " in "+path)
	if x.argPresent("limit") {
		text += t.Fg("toolOutput", " (limit "+jsValue(x.args["limit"])+")")
	}
	return text
}

func formatLsCall(x *ToolExecution) string {
	t := x.env.Theme
	raw, valid := x.argString("path")
	text := t.Fg("toolTitle", Bold("ls")) + " " + x.renderToolPath(raw, valid, ".")
	if x.argPresent("limit") {
		text += t.Fg("toolOutput", " (limit "+jsValue(x.args["limit"])+")")
	}
	return text
}

// listResult is grep, find and ls's result: the first lines of output and
// a warning naming whatever limited it.
func (x *ToolExecution) listResult(max int, warnings []string) string {
	t := x.env.Theme
	text := ""
	if out := strings.TrimSpace(x.textOutput()); out != "" {
		text += x.previewLines(strings.Split(out, "\n"), max, func(l string) string { return t.Fg("toolOutput", l) },
			func(n int) string { return itoa(n) + " more lines" })
	}
	if len(warnings) > 0 {
		text += "\n" + t.Fg("warning", "[Truncated: "+strings.Join(warnings, ", ")+"]")
	}
	return text
}

func formatGrepResult(x *ToolExecution) string {
	var warnings []string
	if d, ok := x.result.Details.(tools.GrepDetails); ok {
		if d.MatchLimitReached > 0 {
			warnings = append(warnings, itoa(d.MatchLimitReached)+" matches limit")
		}
		if d.Truncation != nil && d.Truncation.Truncated {
			warnings = append(warnings, tools.FormatSize(maxBytesOf(d.Truncation))+" limit")
		}
		if d.LinesTruncated {
			warnings = append(warnings, "some lines truncated")
		}
	}
	return x.listResult(15, warnings)
}

func formatFindResult(x *ToolExecution) string {
	var warnings []string
	if d, ok := x.result.Details.(tools.FindDetails); ok {
		if d.ResultLimitReached > 0 {
			warnings = append(warnings, itoa(d.ResultLimitReached)+" results limit")
		}
		if d.Truncation != nil && d.Truncation.Truncated {
			warnings = append(warnings, tools.FormatSize(maxBytesOf(d.Truncation))+" limit")
		}
	}
	return x.listResult(20, warnings)
}

func formatLsResult(x *ToolExecution) string {
	var warnings []string
	if d, ok := x.result.Details.(tools.LsDetails); ok {
		if d.EntryLimitReached > 0 {
			warnings = append(warnings, itoa(d.EntryLimitReached)+" entries limit")
		}
		if d.Truncation != nil && d.Truncation.Truncated {
			warnings = append(warnings, tools.FormatSize(maxBytesOf(d.Truncation))+" limit")
		}
	}
	return x.listResult(20, warnings)
}

// --- bash ---

// bashPreviewLines is how many lines of a command's output show folded.
const bashPreviewLines = 5

type bashRenderer struct {
	startedAt, endedAt time.Time
	ticker             *time.Ticker
}

func (*bashRenderer) self() bool { return false }

func (r *bashRenderer) call(x *ToolExecution) tui.Component {
	t := x.env.Theme
	if x.executionStarted && r.startedAt.IsZero() {
		r.startedAt = time.Now()
		r.endedAt = time.Time{}
	}
	command, valid := x.argString("command")
	display := command
	switch {
	case !valid:
		display = invalidArg(t)
	case command == "":
		display = t.Fg("toolOutput", "...")
	}
	suffix := ""
	if timeout, ok := jsNumber(x.args["timeout"]); ok && timeout != 0 {
		suffix = t.Fg("muted", " (timeout "+jsValue(timeout)+"s)")
	}
	return tui.NewText(t.Fg("toolTitle", Bold("$ "+display))+suffix, 0, 0, nil)
}

func (r *bashRenderer) result(x *ToolExecution) tui.Component {
	t := x.env.Theme
	if !r.startedAt.IsZero() && x.isPartial && r.ticker == nil {
		// The elapsed time moves while the command runs.
		r.ticker = time.NewTicker(time.Second)
		ticker := r.ticker
		go func() {
			for range ticker.C {
				x.env.requestRender()
			}
		}()
	}
	if !x.isPartial || x.isError() {
		if r.endedAt.IsZero() {
			r.endedAt = time.Now()
		}
		if r.ticker != nil {
			r.ticker.Stop()
			r.ticker = nil
		}
	}

	c := &tui.Container{}
	output := strings.TrimSpace(x.textOutput())
	details, _ := x.result.Details.(tools.BashDetails)
	cut := details.Truncation
	// The note naming the full output's file is drawn as a warning below,
	// so it is dropped from the output.
	if !x.isPartial && cut != nil && cut.Truncated && details.FullOutputPath != "" && strings.HasSuffix(output, "]") {
		if start := strings.LastIndex(output, "\n\n["); start >= 0 && strings.Contains(output[start:], details.FullOutputPath) {
			output = strings.TrimRight(output[:start], " \t\n")
		}
	}
	if output != "" {
		lines := strings.Split(output, "\n")
		for i, l := range lines {
			lines[i] = t.Fg("toolOutput", l)
		}
		styled := strings.Join(lines, "\n")
		if x.expanded {
			c.AddChild(tui.NewText("\n"+styled, 0, 0, nil))
		} else {
			c.AddChild(&tailPreview{x: x, text: styled})
		}
	}
	if (cut != nil && cut.Truncated) || details.FullOutputPath != "" {
		var warnings []string
		if details.FullOutputPath != "" {
			warnings = append(warnings, "Full output: "+details.FullOutputPath)
		}
		if cut != nil && cut.Truncated {
			if cut.By == tools.TruncatedByLines {
				warnings = append(warnings, "Truncated: showing "+itoa(cut.OutputLines)+" of "+itoa(cut.TotalLines)+" lines")
			} else {
				warnings = append(warnings, "Truncated: "+itoa(cut.OutputLines)+" lines shown ("+tools.FormatSize(maxBytesOf(cut))+" limit)")
			}
		}
		c.AddChild(tui.NewText("\n"+t.Fg("warning", "["+strings.Join(warnings, ". ")+"]"), 0, 0, nil))
	}
	if !r.startedAt.IsZero() {
		c.AddChild(&elapsed{r: r, x: x})
	}
	return c
}

// tailPreview is a command's output folded to its last lines at the width
// it is drawn at, with a hint naming how many earlier lines are hidden.
type tailPreview struct {
	x    *ToolExecution
	text string
}

func (p *tailPreview) Invalidate() {}

func (p *tailPreview) Render(width int) []string {
	t := p.x.env.Theme
	all := tui.NewText(p.text, 0, 0, nil).Render(width)
	shown, skipped := all, 0
	if len(all) > bashPreviewLines {
		shown, skipped = all[len(all)-bashPreviewLines:], len(all)-bashPreviewLines
	}
	out := []string{""}
	if skipped > 0 {
		hint := t.Fg("muted", "... ("+itoa(skipped)+" earlier lines,") + " " + p.x.env.KeyHint("app.tools.expand", "to expand") + t.Fg("muted", ")")
		out = append(out, tui.TruncateToWidth(hint, width, "...", false))
	}
	return append(out, shown...)
}

// elapsed is how long a command has run, or ran.
type elapsed struct {
	r *bashRenderer
	x *ToolExecution
}

func (e *elapsed) Invalidate() {}

func (e *elapsed) Render(width int) []string {
	label := "Took"
	if e.x.isPartial {
		label = "Elapsed"
	}
	end := e.r.endedAt
	if end.IsZero() {
		end = time.Now()
	}
	seconds := strconv.FormatFloat(end.Sub(e.r.startedAt).Seconds(), 'f', 1, 64)
	return tui.NewText("\n"+e.x.env.Theme.Fg("muted", label+" "+seconds+"s"), 0, 0, nil).Render(width)
}

// --- edit ---

// editRenderer draws an edit in its own box: the path, then the diff — the
// preview worked out before the call runs, replaced by the edit's own diff
// if that differs — coloured by whether the edit can or did apply.
type editRenderer struct {
	box          *tui.Box
	preview      *editPreview
	previewKey   string
	pending      bool
	settledError bool
}

type editPreview struct {
	diff  string
	first int
	err   string
}

func (*editRenderer) self() bool { return true }

func (r *editRenderer) headerBackground(t *Theme) tui.Style {
	switch {
	case r.preview != nil && r.preview.err != "":
		return t.BgStyle("toolErrorBg")
	case r.preview != nil:
		return t.BgStyle("toolSuccessBg")
	case r.settledError:
		return t.BgStyle("toolErrorBg")
	}
	return t.BgStyle("toolPendingBg")
}

func (r *editRenderer) build(x *ToolExecution) {
	t := x.env.Theme
	raw, valid := x.argString("file_path", "path")
	r.box.SetBackground(r.headerBackground(t))
	r.box.Clear()
	r.box.AddChild(tui.NewText(t.Fg("toolTitle", Bold("edit"))+" "+x.renderToolPath(raw, valid, ""), 0, 0, nil))
	if r.preview == nil {
		return
	}
	body := t.Fg("error", r.preview.err)
	if r.preview.err == "" {
		body = renderDiff(t, r.preview.diff)
	}
	r.box.AddChild(&tui.Spacer{Lines: 1})
	r.box.AddChild(tui.NewText(body, 0, 0, nil))
}

// previewKey identifies the arguments a preview was worked out for; empty
// when they are not yet enough to work one out.
func (x *ToolExecution) editPreviewKey() string {
	path, _ := x.argString("path", "file_path")
	edits, ok := x.args["edits"].([]any)
	if path == "" || !ok || len(edits) == 0 {
		return ""
	}
	for _, e := range edits {
		m, ok := e.(map[string]any)
		if !ok {
			return ""
		}
		if _, ok := m["oldText"].(string); !ok {
			return ""
		}
		if _, ok := m["newText"].(string); !ok {
			return ""
		}
	}
	return x.rawArg
}

func (r *editRenderer) call(x *ToolExecution) tui.Component {
	if r.box == nil {
		r.box = tui.NewBox(1, 1, nil)
	}
	key := x.editPreviewKey()
	if r.previewKey != key {
		r.preview, r.previewKey, r.pending, r.settledError = nil, key, false, false
	}
	if x.argsComplete && key != "" && r.preview == nil && !r.pending {
		r.pending = true
		cwd, args := x.env.Cwd, x.rawArg
		apply := func(p *editPreview) {
			if r.previewKey == key {
				r.preview, r.pending = p, false
				r.build(x)
				x.env.requestRender()
			}
		}
		compute := func() *editPreview {
			d, err := tools.PreviewEdit(cwd, args)
			if err != nil {
				return &editPreview{err: err.Error()}
			}
			return &editPreview{diff: d.Diff, first: d.FirstChangedLine}
		}
		if x.env.Do != nil {
			go func() {
				p := compute()
				x.env.Do(func() { apply(p) })
			}()
		} else {
			apply(compute())
		}
	}
	r.build(x)
	return r.box
}

func (r *editRenderer) result(x *ToolExecution) tui.Component {
	t := x.env.Theme
	details, _ := x.result.Details.(tools.EditDetails)
	if !x.isError() && details.Diff != "" {
		r.preview = &editPreview{diff: details.Diff, first: details.FirstChangedLine}
		r.previewKey = x.editPreviewKey()
	}
	r.settledError = x.isError()
	r.build(x)

	c := &tui.Container{}
	output := ""
	switch {
	case x.isError():
		if text := x.result.Text; text != "" && (r.preview == nil || text != r.preview.err) {
			output = t.Fg("error", text)
		}
	}
	if output != "" {
		c.AddChild(&tui.Spacer{Lines: 1})
		c.AddChild(tui.NewText(output, 1, 0, nil))
	}
	return c
}
