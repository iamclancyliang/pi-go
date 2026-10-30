package interactive

import (
	"cmp"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tui"
)

// SessionLoader lists sessions, saying how far it has got. It runs off the
// interface's loop.
type SessionLoader func(onProgress func(loaded, total int)) ([]session.Info, error)

// SessionSelectorOptions are what a SessionSelector acts through.
type SessionSelectorOptions struct {
	Current SessionLoader
	All     SessionLoader
	// CurrentPath is the session in use, which is marked and cannot be deleted.
	CurrentPath string
	OnSelect    func(path string)
	OnCancel    func()
	// Rename names a session; Delete removes one, saying whether it went to the
	// trash. Each runs off the loop.
	Rename func(path, name string) error
	Delete func(path string) (trashed bool, err error)
	// Now is the clock ages are measured by.
	Now func() time.Time
}

const (
	sortThreaded  = "threaded"
	sortRecent    = "recent"
	sortRelevance = "relevance"
)

// parsedQuery is a session search: fuzzy words and exact phrases, or a pattern.
type parsedQuery struct {
	regex   *regexp.Regexp
	isRegex bool
	tokens  []queryToken
	failed  bool
}

type queryToken struct {
	phrase bool
	value  string
}

func parseSessionQuery(query string) parsedQuery {
	trimmed := strings.TrimFunc(query, unicode.IsSpace)
	if trimmed == "" {
		return parsedQuery{}
	}
	if pattern, ok := strings.CutPrefix(trimmed, "re:"); ok {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			return parsedQuery{isRegex: true, failed: true}
		}
		compiled, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			return parsedQuery{isRegex: true, failed: true}
		}
		return parsedQuery{isRegex: true, regex: compiled}
	}

	var parsed parsedQuery
	var buf strings.Builder
	inQuote := false
	flush := func(phrase bool) {
		value := strings.TrimSpace(buf.String())
		buf.Reset()
		if value != "" {
			parsed.tokens = append(parsed.tokens, queryToken{phrase: phrase, value: value})
		}
	}
	for _, ch := range trimmed {
		switch {
		case ch == '"':
			flush(inQuote)
			inQuote = !inQuote
		case !inQuote && unicode.IsSpace(ch):
			flush(false)
		default:
			buf.WriteRune(ch)
		}
	}
	if inQuote {
		// An unclosed quote is no phrase: plain words, quote and all.
		parsed.tokens = nil
		for _, word := range strings.Fields(trimmed) {
			parsed.tokens = append(parsed.tokens, queryToken{value: word})
		}
		return parsed
	}
	flush(false)
	return parsed
}

func normalizeWhitespaceLower(text string) string {
	return strings.Join(strings.Fields(strings.ToLower(text)), " ")
}

// matchSession reports whether a session answers a search, and how well: lower is
// better — Pi's matchSession.
func matchSession(info session.Info, query parsedQuery) (bool, float64) {
	text := info.ID + " " + info.Name + " " + info.AllText + " " + info.Dir
	if query.isRegex {
		if query.regex == nil {
			return false, 0
		}
		loc := query.regex.FindStringIndex(text)
		if loc == nil {
			return false, 0
		}
		return true, float64(loc[0]) * 0.1
	}
	total := 0.0
	normalized := ""
	for _, token := range query.tokens {
		if token.phrase {
			if normalized == "" {
				normalized = normalizeWhitespaceLower(text)
			}
			phrase := normalizeWhitespaceLower(token.value)
			if phrase == "" {
				continue
			}
			at := strings.Index(normalized, phrase)
			if at < 0 {
				return false, 0
			}
			total += float64(at) * 0.1
			continue
		}
		ok, score := tui.FuzzyMatch(token.value, text)
		if !ok {
			return false, 0
		}
		total += score
	}
	return true, total
}

// filterSessions narrows and orders sessions for a search — Pi's
// filterAndSortSessions, with no name filter, which the list has applied already.
func filterSessions(sessions []session.Info, query, sortMode string) []session.Info {
	if strings.TrimSpace(query) == "" {
		return sessions
	}
	parsed := parseSessionQuery(query)
	if parsed.failed {
		return nil
	}
	if sortMode == sortRecent {
		var out []session.Info
		for _, s := range sessions {
			if ok, _ := matchSession(s, parsed); ok {
				out = append(out, s)
			}
		}
		return out
	}
	type scored struct {
		info  session.Info
		score float64
	}
	var hits []scored
	for _, s := range sessions {
		if ok, score := matchSession(s, parsed); ok {
			hits = append(hits, scored{s, score})
		}
	}
	slices.SortStableFunc(hits, func(a, b scored) int {
		if c := cmp.Compare(a.score, b.score); c != 0 {
			return c
		}
		return b.info.Activity.Compare(a.info.Activity)
	})
	out := make([]session.Info, len(hits))
	for i, h := range hits {
		out[i] = h.info
	}
	return out
}

// formatSessionAge is Pi's formatSessionDate.
func formatSessionAge(at, now time.Time) string {
	d := now.Sub(at)
	mins, hours, days := int(d/time.Minute), int(d/time.Hour), int(d/(24*time.Hour))
	switch {
	case mins < 1:
		return "now"
	case mins < 60:
		return strconv.Itoa(mins) + "m"
	case hours < 24:
		return strconv.Itoa(hours) + "h"
	case days < 7:
		return strconv.Itoa(days) + "d"
	case days < 30:
		return strconv.Itoa(days/7) + "w"
	case days < 365:
		return strconv.Itoa(days/30) + "mo"
	}
	return strconv.Itoa(days/365) + "y"
}

func canonicalPath(path string) string {
	if path == "" {
		return path
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// sessionNode is a session in the tree forks make.
type sessionNode struct {
	info     session.Info
	children []*sessionNode
	latest   time.Time
}

// flatSession is a session as a row of the list.
type flatSession struct {
	info      session.Info
	depth     int
	isLast    bool
	continues []bool
}

// threadSessions orders sessions as a tree of forks, the most recently active
// subtree first, and flattens it into rows — Pi's buildSessionTree and
// flattenSessionTree.
func threadSessions(sessions []session.Info) []flatSession {
	byPath := make(map[string]*sessionNode, len(sessions))
	for _, s := range sessions {
		byPath[canonicalPath(s.Path)] = &sessionNode{info: s, latest: s.Activity}
	}
	var roots []*sessionNode
	for _, s := range sessions {
		node := byPath[canonicalPath(s.Path)]
		parent := canonicalPath(s.ParentSession)
		if p, ok := byPath[parent]; ok && parent != "" {
			p.children = append(p.children, node)
		} else {
			roots = append(roots, node)
		}
	}
	var update func(*sessionNode) time.Time
	update = func(n *sessionNode) time.Time {
		n.latest = n.info.Activity
		for _, c := range n.children {
			if at := update(c); at.After(n.latest) {
				n.latest = at
			}
		}
		return n.latest
	}
	var order func([]*sessionNode)
	order = func(nodes []*sessionNode) {
		slices.SortStableFunc(nodes, func(a, b *sessionNode) int { return b.latest.Compare(a.latest) })
		for _, n := range nodes {
			order(n.children)
		}
	}
	for _, r := range roots {
		update(r)
	}
	order(roots)

	var rows []flatSession
	var walk func(n *sessionNode, depth int, continues []bool, isLast bool)
	walk = func(n *sessionNode, depth int, continues []bool, isLast bool) {
		rows = append(rows, flatSession{info: n.info, depth: depth, isLast: isLast, continues: continues})
		for i, c := range n.children {
			walk(c, depth+1, append(slices.Clone(continues), depth > 0 && !isLast), i == len(n.children)-1)
		}
	}
	for i, r := range roots {
		walk(r, 0, nil, i == len(roots)-1)
	}
	return rows
}

// SessionSelector picks a conversation to resume — Pi's SessionSelectorComponent:
// this directory's or everyone's, threaded by fork, searchable, with rename and
// delete.
type SessionSelector struct {
	tui.Container
	env  *Env
	opts SessionSelectorOptions

	// Header state.
	allScope  bool
	sortMode  string
	named     bool
	loading   bool
	progress  string
	showPath  bool
	confirm   string
	status    string
	statusErr bool
	statusSeq int

	// List state.
	search     tui.Input
	sessions   []session.Info
	rows       []flatSession
	selected   int
	maxVisible int
	showCwd    bool
	current    string

	// Loaded lists, nil until they have been.
	currentSessions []session.Info
	allSessions     []session.Info
	loadedCurrent   bool
	loadedAll       bool
	currentLoading  bool
	allLoading      bool
	allLoadSeq      int

	renaming     bool
	renameInput  tui.Input
	renameTarget string
	focused      bool
}

// NewSessionSelector opens on this directory's sessions, loading them in the
// background.
func NewSessionSelector(env *Env, opts SessionSelectorOptions) *SessionSelector {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := &SessionSelector{env: env, opts: opts, sortMode: sortThreaded, maxVisible: 10,
		current: canonicalPath(opts.CurrentPath)}
	s.search.Keybindings = env.Keys
	s.search.OnSubmit = func(string) { s.selectCurrent() }
	s.renameInput.Keybindings = env.Keys
	s.renameInput.OnSubmit = s.confirmRename
	s.buildLayout(false)
	s.applyFilter()
	s.load(false, true)
	return s
}

func (s *SessionSelector) buildLayout(renaming bool) {
	t := s.env.Theme
	accent := &border{color: t.FgStyle("accent")}
	s.Clear()
	s.AddChild(spacer())
	s.AddChild(accent)
	s.AddChild(spacer())
	if renaming {
		s.AddChild(tui.NewText(Bold("Rename Session"), 1, 0, nil))
		s.AddChild(spacer())
		s.AddChild(&s.renameInput)
		s.AddChild(spacer())
		s.AddChild(tui.NewText(t.Fg("muted", s.env.KeyText("tui.select.confirm")+" to save · "+
			s.env.KeyText("tui.select.cancel")+" to cancel"), 1, 0, nil))
	} else {
		s.AddChild(sessionHeader{s})
		s.AddChild(spacer())
		s.AddChild(sessionList{s})
	}
	s.AddChild(spacer())
	s.AddChild(accent)
}

// SetFocused passes focus to whichever input is showing.
func (s *SessionSelector) SetFocused(focused bool) {
	s.focused = focused
	s.search.SetFocused(focused)
	s.renameInput.SetFocused(focused)
}

// load reads one scope's sessions off the loop.
func (s *SessionSelector) load(all, initial bool) {
	if all {
		s.allLoading = true
	} else {
		s.currentLoading = true
	}
	seq := 0
	if all {
		s.allLoadSeq++
		seq = s.allLoadSeq
	}
	s.allScope = all
	s.loading, s.progress = true, ""
	s.env.requestRender()

	isCurrentLoad := func() bool { return s.allScope == all && (!all || seq == s.allLoadSeq) }
	loader := s.opts.Current
	if all {
		loader = s.opts.All
	}
	go func() {
		sessions, err := loader(func(loaded, total int) {
			s.env.do(func() {
				if isCurrentLoad() {
					s.progress = strconv.Itoa(loaded) + "/" + strconv.Itoa(total)
					s.env.requestRender()
				}
			})
		})
		s.env.do(func() {
			if all {
				s.allLoading = false
			} else {
				s.currentLoading = false
			}
			if err == nil {
				if all {
					s.allSessions, s.loadedAll = sessions, true
				} else {
					s.currentSessions, s.loadedCurrent = sessions, true
				}
			}
			if !isCurrentLoad() {
				return
			}
			s.loading, s.progress = false, ""
			if err != nil {
				s.setStatus("Failed to load sessions: "+err.Error(), true, 4*time.Second)
				if initial {
					s.setSessions(nil, all)
				}
			} else {
				s.setSessions(sessions, all)
			}
			s.env.requestRender()
		})
	}()
}

func (s *SessionSelector) setSessions(sessions []session.Info, showCwd bool) {
	s.sessions, s.showCwd = sessions, showCwd
	s.applyFilter()
}

func (s *SessionSelector) applyFilter() {
	query := s.search.Value()
	named := s.sessions
	if s.named {
		named = nil
		for _, info := range s.sessions {
			if strings.TrimSpace(info.Name) != "" {
				named = append(named, info)
			}
		}
	}
	if s.sortMode == sortThreaded && strings.TrimSpace(query) == "" {
		s.rows = threadSessions(named)
	} else {
		filtered := filterSessions(named, query, s.sortMode)
		s.rows = make([]flatSession, len(filtered))
		for i, info := range filtered {
			s.rows[i] = flatSession{info: info, isLast: true}
		}
	}
	s.selected = min(s.selected, max(0, len(s.rows)-1))
}

func (s *SessionSelector) setStatus(message string, isError bool, hideAfter time.Duration) {
	s.status, s.statusErr = message, isError
	s.statusSeq++
	if message == "" || hideAfter <= 0 {
		return
	}
	seq := s.statusSeq
	time.AfterFunc(hideAfter, func() {
		s.env.do(func() {
			if s.statusSeq == seq {
				s.status = ""
				s.env.requestRender()
			}
		})
	})
}

func (s *SessionSelector) selectCurrent() {
	if s.selected < len(s.rows) {
		s.setStatus("", false, 0)
		s.opts.OnSelect(s.rows[s.selected].info.Path)
	}
}

func (s *SessionSelector) isCurrent(path string) bool {
	return s.current != "" && canonicalPath(path) == s.current
}

func (s *SessionSelector) startDelete() {
	if s.selected >= len(s.rows) {
		return
	}
	path := s.rows[s.selected].info.Path
	if s.isCurrent(path) {
		s.setStatus("Cannot delete the currently active session", true, 3*time.Second)
		s.env.requestRender()
		return
	}
	s.confirm = path
	s.env.requestRender()
}

func (s *SessionSelector) deleteSession(path string) {
	go func() {
		trashed, err := s.opts.Delete(path)
		s.env.do(func() {
			if err != nil {
				s.setStatus("Failed to delete: "+err.Error(), true, 3*time.Second)
				s.env.requestRender()
				return
			}
			gone := func(info session.Info) bool { return info.Path == path }
			s.currentSessions = slices.DeleteFunc(s.currentSessions, gone)
			s.allSessions = slices.DeleteFunc(s.allSessions, gone)
			if s.allScope {
				s.setSessions(s.allSessions, true)
			} else {
				s.setSessions(s.currentSessions, false)
			}
			message := "Session deleted"
			if trashed {
				message = "Session moved to trash"
			}
			s.setStatus(message, false, 2*time.Second)
			s.load(s.allScope, false)
		})
	}()
}

func (s *SessionSelector) enterRename(path, name string) {
	s.renaming, s.renameTarget = true, path
	s.renameInput.SetValue(name)
	// Focused outright, as Pi's is: the rename is what the keys go to now.
	s.renameInput.SetFocused(true)
	s.buildLayout(true)
	s.env.requestRender()
}

func (s *SessionSelector) exitRename() {
	s.renaming, s.renameTarget = false, ""
	s.buildLayout(false)
	s.env.requestRender()
}

func (s *SessionSelector) confirmRename(value string) {
	next := strings.TrimSpace(value)
	if next == "" {
		return
	}
	target := s.renameTarget
	go func() {
		err := s.opts.Rename(target, next)
		s.env.do(func() {
			if err != nil {
				s.setStatus("Failed to rename: "+err.Error(), true, 3*time.Second)
			} else {
				s.load(s.allScope, false)
			}
			s.exitRename()
		})
	}()
}

func (s *SessionSelector) toggleScope() {
	if !s.allScope {
		s.allScope = true
		if s.loadedAll {
			s.loading = false
			s.setSessions(s.allSessions, true)
		} else if !s.allLoading {
			s.load(true, false)
		}
		return
	}
	s.allScope = false
	s.loading = s.currentLoading
	s.setSessions(s.currentSessions, false)
}

func (s *SessionSelector) toggleSort() {
	switch s.sortMode {
	case sortThreaded:
		s.sortMode = sortRecent
	case sortRecent:
		s.sortMode = sortRelevance
	default:
		s.sortMode = sortThreaded
	}
	s.applyFilter()
}

// HandleKey goes to the rename input while one is open, otherwise to the list.
func (s *SessionSelector) HandleKey(k tui.Key) {
	kb := s.env.Keys
	if s.renaming {
		if kb.Matches(k, "tui.select.cancel") {
			s.exitRename()
			return
		}
		s.renameInput.HandleKey(k)
		return
	}
	if s.confirm != "" {
		switch {
		case kb.Matches(k, "tui.select.confirm"):
			path := s.confirm
			s.confirm = ""
			s.deleteSession(path)
		case kb.Matches(k, "tui.select.cancel"):
			s.confirm = ""
		}
		return
	}
	switch {
	case kb.Matches(k, "tui.input.tab"):
		s.toggleScope()
	case kb.Matches(k, "app.session.toggleSort"):
		s.toggleSort()
	case kb.Matches(k, "app.session.toggleNamedFilter"):
		s.named = !s.named
		s.applyFilter()
	case kb.Matches(k, "app.session.togglePath"):
		s.showPath = !s.showPath
	case kb.Matches(k, "app.session.delete"):
		s.startDelete()
	case kb.Matches(k, "app.session.rename"):
		if s.selected < len(s.rows) && !s.loadingScope() {
			s.enterRename(s.rows[s.selected].info.Path, s.rows[s.selected].info.Name)
		}
	case kb.Matches(k, "app.session.deleteNoninvasive"):
		// With a search typed, this is the ordinary key for deleting a word of it.
		if s.search.Value() != "" {
			s.search.HandleKey(k)
			s.applyFilter()
			return
		}
		s.startDelete()
	case kb.Matches(k, "tui.select.up"):
		s.selected = max(0, s.selected-1)
	case kb.Matches(k, "tui.select.down"):
		s.selected = min(len(s.rows)-1, s.selected+1)
		s.selected = max(0, s.selected)
	case kb.Matches(k, "tui.select.pageUp"):
		s.selected = max(0, s.selected-s.maxVisible)
	case kb.Matches(k, "tui.select.pageDown"):
		s.selected = max(0, min(len(s.rows)-1, s.selected+s.maxVisible))
	case kb.Matches(k, "tui.select.confirm"):
		s.selectCurrent()
	case kb.Matches(k, "tui.select.cancel"):
		s.setStatus("", false, 0)
		s.opts.OnCancel()
	default:
		s.search.HandleKey(k)
		s.applyFilter()
	}
}

func (s *SessionSelector) loadingScope() bool {
	if s.allScope {
		return s.allLoading
	}
	return s.currentLoading
}

// sessionHeader is the title, what is being shown, and the keys.
type sessionHeader struct{ s *SessionSelector }

func (h sessionHeader) Invalidate() {}

func (h sessionHeader) Render(width int) []string {
	s, env, t := h.s, h.s.env, h.s.env.Theme
	title := "Resume Session (Current Folder)"
	if s.allScope {
		title = "Resume Session (All)"
	}
	sortLabel := "Fuzzy"
	switch s.sortMode {
	case sortThreaded:
		sortLabel = "Threaded"
	case sortRecent:
		sortLabel = "Recent"
	}
	sortText := t.Fg("muted", "Sort: ") + t.Fg("accent", sortLabel)
	nameLabel := "All"
	if s.named {
		nameLabel = "Named"
	}
	nameText := t.Fg("muted", "Name: ") + t.Fg("accent", nameLabel)

	var scope string
	switch {
	case s.loading:
		progress := s.progress
		if progress == "" {
			progress = "..."
		}
		scope = t.Fg("muted", "○ Current Folder | ") + t.Fg("accent", "Loading "+progress)
	case !s.allScope:
		scope = t.Fg("accent", "◉ Current Folder") + t.Fg("muted", " | ○ All")
	default:
		scope = t.Fg("muted", "○ Current Folder | ") + t.Fg("accent", "◉ All")
	}

	right := tui.TruncateToWidth(scope+"  "+nameText+"  "+sortText, width, "", false)
	left := tui.TruncateToWidth(Bold(title), max(0, width-tui.VisibleWidth(right)-1), "", false)
	spacing := max(0, width-tui.VisibleWidth(left)-tui.VisibleWidth(right))

	var line1, line2 string
	switch {
	case s.confirm != "":
		hint := "Delete session? " + env.KeyHint("tui.select.confirm", "confirm") + " · " + env.KeyHint("tui.select.cancel", "cancel")
		line1 = t.Fg("error", tui.TruncateToWidth(hint, width, "…", false))
	case s.status != "":
		color := "accent"
		if s.statusErr {
			color = "error"
		}
		line1 = t.Fg(color, tui.TruncateToWidth(s.status, width, "…", false))
	default:
		pathState := "(off)"
		if s.showPath {
			pathState = "(on)"
		}
		sep := t.Fg("muted", " · ")
		hint1 := env.KeyHint("tui.input.tab", "scope") + sep + t.Fg("muted", `re:<pattern> regex · "phrase" exact`)
		hint2 := strings.Join([]string{
			env.KeyHint("app.session.toggleSort", "sort"),
			env.KeyHint("app.session.toggleNamedFilter", "named"),
			env.KeyHint("app.session.delete", "delete"),
			env.KeyHint("app.session.togglePath", "path "+pathState),
			env.KeyHint("app.session.rename", "rename"),
		}, sep)
		line1 = tui.TruncateToWidth(hint1, width, "…", false)
		line2 = tui.TruncateToWidth(hint2, width, "…", false)
	}
	return []string{left + strings.Repeat(" ", spacing) + right, line1, line2}
}

// sessionList is the search and the rows.
type sessionList struct{ s *SessionSelector }

func (l sessionList) Invalidate() {}

func (l sessionList) Render(width int) []string {
	s, t := l.s, l.s.env.Theme
	lines := append(s.search.Render(width), "")
	if len(s.rows) == 0 {
		var empty string
		switch {
		case s.named && s.showCwd:
			empty = "  No named sessions found. Press " + s.env.KeyText("app.session.toggleNamedFilter") + " to show all."
		case s.named:
			empty = "  No named sessions in current folder. Press " + s.env.KeyText("app.session.toggleNamedFilter") + " to show all, or Tab to view all."
		case s.showCwd:
			empty = "  No sessions found"
		default:
			empty = "  No sessions in current folder. Press Tab to view all."
		}
		return append(lines, t.Fg("muted", tui.TruncateToWidth(empty, width, "…", false)))
	}

	start, end := windowOf(s.selected, len(s.rows), s.maxVisible)
	now := s.opts.Now()
	for i := start; i < end; i++ {
		row := s.rows[i]
		info := row.info
		selected := i == s.selected
		deleting := info.Path == s.confirm

		prefix := ""
		if row.depth > 0 {
			var b strings.Builder
			for _, continues := range row.continues {
				if continues {
					b.WriteString("│  ")
				} else {
					b.WriteString("   ")
				}
			}
			if row.isLast {
				b.WriteString("└─ ")
			} else {
				b.WriteString("├─ ")
			}
			prefix = b.String()
		}

		display := info.Name
		if display == "" {
			display = info.Opening
			if display == "" {
				display = "(no messages)"
			}
		}
		message := strings.TrimSpace(strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return ' '
			}
			return r
		}, display))

		right := strconv.Itoa(info.MessageCount) + " " + formatSessionAge(info.Activity, now)
		if s.showCwd && info.Dir != "" {
			right = s.shorten(info.Dir) + " " + right
		}
		if s.showPath {
			right = s.shorten(info.Path) + " " + right
		}

		cursor := "  "
		if selected {
			cursor = t.Fg("accent", "› ")
		}
		available := width - 2 - tui.VisibleWidth(prefix) - (tui.VisibleWidth(right) + 2)
		truncated := tui.TruncateToWidth(message, max(10, available), "…", false)

		styled := truncated
		switch {
		case deleting:
			styled = t.Fg("error", truncated)
		case s.isCurrent(info.Path):
			styled = t.Fg("accent", truncated)
		case info.Name != "":
			styled = t.Fg("warning", truncated)
		}
		if selected {
			styled = Bold(styled)
		}

		left := cursor + t.Fg("dim", prefix) + styled
		spacing := max(1, width-tui.VisibleWidth(left)-tui.VisibleWidth(right))
		rightColor := "dim"
		if deleting {
			rightColor = "error"
		}
		line := left + strings.Repeat(" ", spacing) + t.Fg(rightColor, right)
		if selected {
			line = t.Bg("selectedBg", line)
		}
		lines = append(lines, tui.TruncateToWidth(line, width, "...", false))
	}
	if start > 0 || end < len(s.rows) {
		lines = append(lines, t.Fg("muted", tui.TruncateToWidth(position(s.selected, len(s.rows)), width, "", false)))
	}
	return lines
}

func (s *SessionSelector) shorten(path string) string {
	if home := s.env.Home; home != "" && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}
