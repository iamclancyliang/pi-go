package interactive

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/tui"
)

// FooterState is what the footer shows, read each time it is drawn.
type FooterState struct {
	Cwd         string
	SessionName string
	// Input, Output and CacheRead are the conversation's token totals.
	Input, Output, CacheRead int
	// ContextTokens is what the context now holds; ContextKnown is false
	// right after a compaction, until the next reply says.
	ContextTokens int
	ContextKnown  bool
	ContextWindow int
	AutoCompact   bool
	// Provider and Model name the model; Thinking is its thinking level when
	// it reasons at all, and empty when it does not.
	Provider, Model, Thinking string
	// ShowProvider prefixes the model with its provider, as Pi does when
	// more than one provider is available.
	ShowProvider bool
}

// Footer is the two lines under the editor — Pi's FooterComponent: the
// working directory with its git branch and the session's name, then token
// totals and how full the context is on the left and the model on the
// right.
type Footer struct {
	env    *Env
	state  func() FooterState
	branch func() string
}

// NewFooter draws state; the git branch is read from the working directory.
func NewFooter(env *Env, state func() FooterState) *Footer {
	f := &Footer{env: env, state: state}
	f.branch = cachedGitBranch(env.Cwd)
	return f
}

func (f *Footer) Invalidate() {}

// formatTokens is a count in the footer's short form: 950, 1.2k, 45k, 1.3M.
func formatTokens(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 10000:
		return strconv.FormatFloat(float64(n)/1000, 'f', 1, 64) + "k"
	case n < 1000000:
		return strconv.Itoa(int(float64(n)/1000+0.5)) + "k"
	case n < 10000000:
		return strconv.FormatFloat(float64(n)/1000000, 'f', 1, 64) + "M"
	}
	return strconv.Itoa(int(float64(n)/1000000+0.5)) + "M"
}

// formatCwd shows a directory inside home as "~/...".
func formatCwd(cwd, home string) string {
	if home == "" {
		return cwd
	}
	rel, err := filepath.Rel(filepath.Clean(home), filepath.Clean(cwd))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return cwd
	}
	if rel == "." {
		return "~"
	}
	return "~" + string(filepath.Separator) + rel
}

func (f *Footer) Render(width int) []string {
	t := f.env.Theme
	s := f.state()

	pwd := formatCwd(s.Cwd, f.env.Home)
	if branch := f.branch(); branch != "" {
		pwd += " (" + branch + ")"
	}
	if s.SessionName != "" {
		pwd += " • " + s.SessionName
	}

	var stats []string
	if s.Input > 0 {
		stats = append(stats, "↑"+formatTokens(s.Input))
	}
	if s.Output > 0 {
		stats = append(stats, "↓"+formatTokens(s.Output))
	}
	if s.CacheRead > 0 {
		stats = append(stats, "R"+formatTokens(s.CacheRead))
	}
	percent := 0.0
	if s.ContextKnown && s.ContextWindow > 0 {
		percent = float64(s.ContextTokens) / float64(s.ContextWindow) * 100
	}
	auto := ""
	if s.AutoCompact {
		auto = " (auto)"
	}
	contextText := "?/" + formatTokens(s.ContextWindow) + auto
	if s.ContextKnown {
		contextText = strconv.FormatFloat(percent, 'f', 1, 64) + "%/" + formatTokens(s.ContextWindow) + auto
	}
	switch {
	case percent > 90:
		contextText = t.Fg("error", contextText)
	case percent > 70:
		contextText = t.Fg("warning", contextText)
	}
	stats = append(stats, contextText)
	left := strings.Join(stats, " ")
	leftWidth := tui.VisibleWidth(left)
	if leftWidth > width {
		left = tui.TruncateToWidth(left, width, "...", false)
		leftWidth = tui.VisibleWidth(left)
	}

	model := s.Model
	if model == "" {
		model = "no-model"
	}
	right := model
	if s.Thinking != "" {
		if s.Thinking == "off" {
			right = model + " • thinking off"
		} else {
			right = model + " • " + s.Thinking
		}
	}
	const minPadding = 2
	if s.ShowProvider && s.Provider != "" {
		withProvider := "(" + s.Provider + ") " + right
		if leftWidth+minPadding+tui.VisibleWidth(withProvider) <= width {
			right = withProvider
		}
	}
	var line string
	if rightWidth := tui.VisibleWidth(right); leftWidth+minPadding+rightWidth <= width {
		line = left + strings.Repeat(" ", width-leftWidth-rightWidth) + right
	} else if room := width - leftWidth - minPadding; room > 0 {
		cut := tui.TruncateToWidth(right, room, "", false)
		line = left + strings.Repeat(" ", max(0, width-leftWidth-tui.VisibleWidth(cut))) + cut
	} else {
		line = left
	}

	// The left part may carry its own colour, whose reset would end a dim
	// wrapped around the whole line, so the two parts are dimmed apart.
	statsLine := t.Fg("dim", left) + t.Fg("dim", line[len(left):])
	return []string{tui.TruncateToWidth(t.Fg("dim", pwd), width, t.Fg("dim", "..."), false), statsLine}
}

// cachedGitBranch returns a function naming the branch checked out in the
// repository dir is in: the branch name, "detached" for a detached head, or
// "" outside a repository. It is read once.
func cachedGitBranch(dir string) func() string {
	read := false
	branch := ""
	return func() string {
		if !read {
			read, branch = true, gitBranch(dir)
		}
		return branch
	}
}

func gitBranch(dir string) string {
	for d := dir; ; {
		gitPath := filepath.Join(d, ".git")
		if info, err := os.Stat(gitPath); err == nil {
			head := filepath.Join(gitPath, "HEAD")
			if !info.IsDir() {
				// A worktree or submodule: .git names the real directory.
				b, err := os.ReadFile(gitPath)
				if err != nil {
					return ""
				}
				target := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
				if !filepath.IsAbs(target) {
					target = filepath.Join(d, target)
				}
				head = filepath.Join(target, "HEAD")
			}
			b, err := os.ReadFile(head)
			if err != nil {
				return ""
			}
			content := strings.TrimSpace(string(b))
			if ref, ok := strings.CutPrefix(content, "ref: refs/heads/"); ok {
				return ref
			}
			return "detached"
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}
