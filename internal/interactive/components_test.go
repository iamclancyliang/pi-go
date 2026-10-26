package interactive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/tools"
	"github.com/iamclancyliang/pi-go/internal/tui"
)

// componentDivergences are golden cases whose difference from Pi is
// accepted, with why.
var componentDivergences = map[string]string{
	"editPreviewError": "an edit that cannot apply shows pi-go's edit tool's own error wording",
	"editMissingFile":  "an edit that cannot apply shows pi-go's edit tool's own error wording",
}

type goldenTruncation struct {
	Truncated             bool   `json:"truncated"`
	TruncatedBy           string `json:"truncatedBy"`
	TotalLines            int    `json:"totalLines"`
	TotalBytes            int    `json:"totalBytes"`
	OutputLines           int    `json:"outputLines"`
	OutputBytes           int    `json:"outputBytes"`
	LastLinePartial       bool   `json:"lastLinePartial"`
	FirstLineExceedsLimit bool   `json:"firstLineExceedsLimit"`
	MaxLines              int    `json:"maxLines"`
	MaxBytes              int    `json:"maxBytes"`
}

func (g *goldenTruncation) toTools() *tools.Truncation {
	if g == nil {
		return nil
	}
	return &tools.Truncation{
		Truncated: g.Truncated, By: tools.TruncatedBy(g.TruncatedBy), TotalLines: g.TotalLines, TotalBytes: g.TotalBytes,
		OutputLines: g.OutputLines, OutputBytes: g.OutputBytes, LastLinePartial: g.LastLinePartial,
		FirstLineExceedsLimit: g.FirstLineExceedsLimit, MaxLines: g.MaxLines, MaxBytes: g.MaxBytes,
	}
}

type goldenDetails struct {
	Truncation         *goldenTruncation `json:"truncation"`
	FullOutputPath     string            `json:"fullOutputPath"`
	MatchLimitReached  int               `json:"matchLimitReached"`
	LinesTruncated     bool              `json:"linesTruncated"`
	ResultLimitReached int               `json:"resultLimitReached"`
	EntryLimitReached  int               `json:"entryLimitReached"`
	Diff               string            `json:"diff"`
}

// toolDetails turns Pi's details for a tool into pi-go's.
func toolDetails(tool string, d *goldenDetails) any {
	if d == nil {
		return nil
	}
	switch tool {
	case "read":
		return tools.ReadDetails{Truncation: d.Truncation.toTools()}
	case "bash":
		return tools.BashDetails{Truncation: d.Truncation.toTools(), FullOutputPath: d.FullOutputPath}
	case "grep":
		return tools.GrepDetails{Truncation: d.Truncation.toTools(), MatchLimitReached: d.MatchLimitReached, LinesTruncated: d.LinesTruncated}
	case "find":
		return tools.FindDetails{Truncation: d.Truncation.toTools(), ResultLimitReached: d.ResultLimitReached}
	case "ls":
		return tools.LsDetails{Truncation: d.Truncation.toTools(), EntryLimitReached: d.EntryLimitReached}
	case "edit":
		return tools.EditDetails{Diff: d.Diff}
	}
	return nil
}

type goldenComponent struct {
	Kind  string   `json:"kind"`
	Name  string   `json:"name"`
	Width int      `json:"width"`
	Lines []string `json:"lines"`

	Text string `json:"text"`

	Message struct {
		Content []struct {
			Type      string         `json:"type"`
			Text      string         `json:"text"`
			Thinking  string         `json:"thinking"`
			ID        string         `json:"id"`
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"content"`
		StopReason   string `json:"stopReason"`
		ErrorMessage string `json:"errorMessage"`
	} `json:"message"`
	HideThinking bool `json:"hideThinking"`

	TokensBefore int    `json:"tokensBefore"`
	Summary      string `json:"summary"`
	Expanded     bool   `json:"expanded"`

	Bash struct {
		Command  string   `json:"command"`
		Exclude  bool     `json:"exclude"`
		Chunks   []string `json:"chunks"`
		Expanded bool     `json:"expanded"`
		Complete *struct {
			ExitCode       *int   `json:"exitCode"`
			Cancelled      bool   `json:"cancelled"`
			Truncated      bool   `json:"truncated"`
			FullOutputPath string `json:"fullOutputPath"`
		} `json:"complete"`
	} `json:"bash"`

	Tool  string          `json:"tool"`
	Args  json.RawMessage `json:"args"`
	Steps struct {
		ArgsComplete bool `json:"argsComplete"`
		Result       *struct {
			Text    string         `json:"text"`
			IsError bool           `json:"isError"`
			Details *goldenDetails `json:"details"`
		} `json:"result"`
		Partial  bool `json:"partial"`
		Expanded bool `json:"expanded"`
	} `json:"steps"`
}

func goldenEnv(t *testing.T) *Env {
	t.Helper()
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "notes.txt"), []byte("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Env{Theme: DarkTheme(true), Keys: NewKeybindings("darwin", nil), Cwd: cwd, Home: "/home/tester", GOOS: "darwin"}
}

func (c goldenComponent) render(env *Env) []string {
	switch c.Kind {
	case "user":
		return NewUserMessage(env, c.Text, 1).Render(c.Width)
	case "assistant":
		msg := &ai.AssistantMessage{StopReason: ai.StopReason(c.Message.StopReason), ErrorMessage: c.Message.ErrorMessage}
		if msg.StopReason == "" {
			msg.StopReason = ai.StopEnd
		}
		for _, b := range c.Message.Content {
			switch b.Type {
			case "text":
				msg.Blocks = append(msg.Blocks, ai.Block{Kind: ai.BlockText, Text: b.Text})
			case "thinking":
				msg.Blocks = append(msg.Blocks, ai.Block{Kind: ai.BlockThinking, Text: b.Thinking})
			case "toolCall":
				args, _ := json.Marshal(b.Arguments)
				msg.Blocks = append(msg.Blocks, ai.Block{Kind: ai.BlockToolCall, Call: ai.ToolCall{ID: b.ID, Name: b.Name, Args: string(args)}})
			}
		}
		return NewAssistantMessage(env, msg, c.HideThinking, 1).Render(c.Width)
	case "compaction":
		s := NewCompactionSummary(env, c.TokensBefore, c.Summary)
		s.SetExpanded(c.Expanded)
		return s.Render(c.Width)
	case "bashExecution":
		b := NewBashExecution(env, c.Bash.Command, c.Bash.Exclude)
		defer b.loader.Stop()
		if c.Bash.Expanded {
			b.SetExpanded(true)
		}
		for _, chunk := range c.Bash.Chunks {
			b.AppendOutput(chunk)
		}
		if done := c.Bash.Complete; done != nil {
			b.SetComplete(tools.BashResult{ExitCode: done.ExitCode, Cancelled: done.Cancelled,
				Truncated: done.Truncated, FullOutputPath: done.FullOutputPath})
		}
		return b.Render(c.Width)
	case "tool":
		x := NewToolExecution(env, c.Tool, string(c.Args))
		if c.Steps.Expanded {
			x.SetExpanded(true)
		}
		if c.Steps.ArgsComplete {
			x.SetArgsComplete()
		}
		if r := c.Steps.Result; r != nil {
			x.UpdateResult(ToolResult{Text: r.Text, IsError: r.IsError, Details: toolDetails(c.Tool, r.Details)}, c.Steps.Partial)
		}
		return x.Render(c.Width)
	}
	return nil
}

// TestComponentsMatchPi draws each component as Pi's own components drew it
// (testdata/components/generate.ts).
func TestComponentsMatchPi(t *testing.T) {
	data, err := os.ReadFile("testdata/components/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Cases []goldenComponent `json:"cases"`
	}
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	tui.SetCapabilities(tui.TerminalCapabilities{TrueColor: true})
	defer tui.SetCapabilities(tui.TerminalCapabilities{})
	env := goldenEnv(t)
	failures := 0
	for _, c := range golden.Cases {
		label := c.Kind + " " + c.Name
		if c.Kind == "user" {
			label += " " + strings.ReplaceAll(c.Text, "\n", "⏎")
		}
		if _, skip := componentDivergences[c.Name]; skip {
			continue
		}
		got := c.render(env)
		if strings.Join(got, "\n") != strings.Join(c.Lines, "\n") {
			failures++
			if failures <= 12 {
				t.Errorf("%s (hide %v)\n got:\n%s\nwant:\n%s", label, c.HideThinking, showLines(got), showLines(c.Lines))
			}
		}
	}
	if failures > 0 {
		t.Logf("%d of %d cases differ", failures, len(golden.Cases))
	}
}

func showLines(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("  |")
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(l, "\x1b", "␛"), "\x07", "⍾"))
		b.WriteString("|\n")
	}
	return b.String()
}
