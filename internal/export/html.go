// Package export renders a conversation for a person to read outside pi-go.
//
// Pi's export is a page whose script renders the session file in the
// browser, with its terminal theme's colours. pi-go's session format is its
// own (ADR-0006), so the page is rendered here instead: one self-contained
// file, readable with scripts off, that a browser opens and a gist can carry.
package export

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// Document is what an export shows.
type Document struct {
	SessionID string
	Name      string
	Provider  string
	Model     string
	Thinking  ai.ThinkingLevel
	Exported  time.Time

	SystemPrompt string
	Tools        []Tool

	// Messages is the conversation on its current branch, oldest first.
	Messages []ai.Message
}

// Tool is a tool the conversation offered, as the model was told of it.
type Tool struct {
	Name        string
	Description string
}

// markdown renders what a model or a person wrote. Raw HTML in the source is
// left out rather than passed through, and dangerous link targets are
// neutralised — goldmark's default, kept on purpose: a conversation carries
// text from files, commands and models, and none of it may become markup
// that runs in the reader's browser.
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// HTML renders a document as one self-contained page.
func HTML(d Document) (string, error) {
	page := pageData{Document: d, Title: d.Name}
	if page.Title == "" {
		page.Title = "pi-go session " + d.SessionID
	}

	// Results are shown with the call they answer, so they are looked up by
	// call id rather than placed where they fall in the record.
	results := map[string]ai.Message{}
	names := map[string]string{}
	for _, m := range d.Messages {
		if m.Role == ai.RoleTool && m.ToolCallID != "" {
			results[m.ToolCallID] = m
		}
		for _, call := range m.ToolCalls {
			names[call.ID] = call.Name
		}
	}
	shown := map[string]bool{}

	for _, m := range d.Messages {
		switch {
		case m.BashExecution != nil:
			page.Items = append(page.Items, shellItem(*m.BashExecution))
		case m.Role == ai.RoleTool:
			if shown[m.ToolCallID] {
				continue
			}
			// A result whose call is not on this branch is still shown, on its
			// own: dropping it would hide something the conversation contains.
			page.Items = append(page.Items, item{Kind: "tool", Label: "result of " + orUnknown(names[m.ToolCallID]),
				Result: m.Content, Collapsed: longText(m.Content)})
		default:
			it, err := messageItem(m)
			if err != nil {
				return "", err
			}
			for _, call := range m.ToolCalls {
				c := callItem{Name: call.Name, Args: prettyJSON(call.Args)}
				if r, ok := results[call.ID]; ok {
					c.Result, c.HasResult, c.Collapsed = r.Content, true, longText(r.Content)
					shown[call.ID] = true
				}
				it.Calls = append(it.Calls, c)
			}
			if it.Body == "" && it.Reasoning == "" && len(it.Calls) == 0 {
				continue
			}
			page.Items = append(page.Items, it)
		}
	}

	var out bytes.Buffer
	if err := pageTemplate.Execute(&out, page); err != nil {
		return "", fmt.Errorf("export: rendering the page: %w", err)
	}
	return out.String(), nil
}

type pageData struct {
	Document
	Title string
	Items []item
}

type item struct {
	Kind      string // user, assistant, system, tool, shell
	Label     string
	Body      template.HTML
	Reasoning string
	Calls     []callItem

	// Result and Collapsed are for a tool result shown on its own, and for
	// a shell command's output.
	Result    string
	Collapsed bool
	Command   string
	Status    string
}

type callItem struct {
	Name      string
	Args      string
	Result    string
	HasResult bool
	Collapsed bool
}

func messageItem(m ai.Message) (item, error) {
	it := item{Kind: string(m.Role), Label: string(m.Role), Reasoning: strings.TrimSpace(m.Reasoning)}
	if strings.TrimSpace(m.Content) != "" {
		var rendered bytes.Buffer
		if err := markdown.Convert([]byte(m.Content), &rendered); err != nil {
			return item{}, fmt.Errorf("export: rendering a %s message: %w", m.Role, err)
		}
		// Safe to mark as HTML: goldmark escaped the source and left raw HTML
		// out, so this is markup goldmark wrote, not markup the message did.
		it.Body = template.HTML(rendered.String())
	}
	return it, nil
}

func shellItem(b ai.BashExecution) item {
	status := "finished"
	switch {
	case b.Cancelled:
		status = "cancelled"
	case b.ExitCode != nil:
		status = fmt.Sprintf("exit %d", *b.ExitCode)
	}
	if b.Truncated {
		status += ", output truncated"
	}
	if b.ExcludeFromContext {
		status += ", not shown to the model"
	}
	output := b.Output
	if output == "" {
		output = "(no output)"
	}
	return item{Kind: "shell", Label: "shell", Command: b.Command, Result: output,
		Status: status, Collapsed: longText(output)}
}

// longText is output worth folding away, so the conversation around it stays
// readable; it opens with a click.
func longText(s string) bool { return strings.Count(s, "\n") > 20 || len(s) > 2000 }

func prettyJSON(raw string) string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return raw
	}
	return string(pretty)
}

func orUnknown(name string) string {
	if name == "" {
		return "an unknown call"
	}
	return name
}

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
:root { --bg:#f6f6f4; --card:#ffffff; --fg:#1d1d1f; --muted:#6b6b70; --line:#e2e2de;
  --user:#eef3fb; --tool:#f4f2ec; --code:#f1f1ee; --accent:#3b6fd8; }
@media (prefers-color-scheme: dark) {
  :root { --bg:#17181b; --card:#1f2024; --fg:#e6e6e3; --muted:#9a9aa0; --line:#2e3036;
    --user:#1d2633; --tool:#24231f; --code:#26272b; --accent:#7aa2f7; }
}
* { box-sizing: border-box; }
body { margin:0; background:var(--bg); color:var(--fg);
  font:15px/1.55 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
main { max-width: 860px; margin: 0 auto; padding: 32px 16px 64px; }
header h1 { font-size: 22px; margin: 0 0 4px; }
.meta { color: var(--muted); font-size: 13px; }
.card { background: var(--card); border: 1px solid var(--line); border-radius: 10px;
  padding: 12px 16px; margin: 12px 0; overflow-wrap: anywhere; }
.card.user { background: var(--user); }
.card.tool, .card.shell { background: var(--tool); }
.role { font-size: 12px; font-weight: 600; text-transform: uppercase; letter-spacing: .04em;
  color: var(--muted); margin-bottom: 4px; }
pre, code { font: 13px/1.45 ui-monospace, SFMono-Regular, Menlo, monospace; }
pre { background: var(--code); padding: 10px 12px; border-radius: 6px; overflow-x: auto;
  white-space: pre-wrap; margin: 6px 0; }
:not(pre) > code { background: var(--code); padding: 1px 4px; border-radius: 4px; }
details { margin: 6px 0; }
summary { cursor: pointer; color: var(--muted); font-size: 13px; }
.call { border-left: 3px solid var(--accent); padding-left: 10px; margin: 8px 0; }
.call-name { font-weight: 600; font-size: 13px; }
table { border-collapse: collapse; } td, th { border: 1px solid var(--line); padding: 4px 8px; }
a { color: var(--accent); }
</style>
</head>
<body>
<main>
<header>
  <h1>{{.Title}}</h1>
  <div class="meta">session {{.SessionID}}{{if .Model}} · {{.Provider}}/{{.Model}}{{end}}{{if .Thinking}} · thinking {{.Thinking}}{{end}} · exported {{.Exported.Format "2006-01-02 15:04 MST"}}</div>
</header>
{{if .SystemPrompt}}<details class="card"><summary>System prompt</summary><pre>{{.SystemPrompt}}</pre></details>{{end}}
{{if .Tools}}<details class="card"><summary>Tools offered ({{len .Tools}})</summary>{{range .Tools}}<p><code>{{.Name}}</code> — {{.Description}}</p>{{end}}</details>{{end}}
{{range .Items}}<section class="card {{.Kind}}">
  <div class="role">{{.Label}}</div>
  {{- if eq .Kind "shell"}}
  <pre>$ {{.Command}}</pre>
  {{if .Collapsed}}<details><summary>output ({{.Status}})</summary><pre>{{.Result}}</pre></details>{{else}}<pre>{{.Result}}</pre><div class="meta">{{.Status}}</div>{{end}}
  {{- else if eq .Kind "tool"}}
  {{if .Collapsed}}<details><summary>show output</summary><pre>{{.Result}}</pre></details>{{else}}<pre>{{.Result}}</pre>{{end}}
  {{- else}}
  {{if .Reasoning}}<details><summary>Thinking</summary><pre>{{.Reasoning}}</pre></details>{{end}}
  {{.Body}}
  {{range .Calls}}<div class="call">
    <div class="call-name">{{.Name}}</div>
    <pre>{{.Args}}</pre>
    {{if .HasResult}}<details{{if not .Collapsed}} open{{end}}><summary>result of {{.Name}}</summary><pre>{{.Result}}</pre></details>{{else}}<div class="meta">no result recorded</div>{{end}}
  </div>{{end}}
  {{- end}}
</section>
{{end}}
</main>
</body>
</html>
`))
