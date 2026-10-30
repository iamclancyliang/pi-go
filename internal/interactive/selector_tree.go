package interactive

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tui"
)

// Tree filter modes — Pi's FilterMode.
const (
	TreeFilterDefault = "default"
	TreeFilterNoTools = "no-tools"
	TreeFilterUser    = "user-only"
	TreeFilterLabeled = "labeled-only"
	TreeFilterAll     = "all"
)

var treeFilterModes = []string{TreeFilterDefault, TreeFilterNoTools, TreeFilterUser, TreeFilterLabeled, TreeFilterAll}

const (
	treeGutterWidth           = 2
	minVisibleAnchorContent   = 4
	maxVisibleAnchorContent   = 20
	minAnchorContext          = 2
	maxAnchorContext          = 12
	treeContentCutoff         = 200
	treeToolArgsCutoff        = 40
	treeCommandCutoff         = 50
	treeErrorCutoff           = 80
	treeHelpIndent            = "  "
	treeHelpSeparator         = " · "
	treeLabelInputIndentWidth = 2
)

// treeNode is an entry with the entries that follow it.
type treeNode struct {
	session.Node
	children []*treeNode
}

// gutterInfo says where a branch's vertical line runs, and whether it still does.
type gutterInfo struct {
	position int
	show     bool
}

// flatTreeNode is an entry as a row of the list.
type flatTreeNode struct {
	node               *treeNode
	indent             int
	showConnector      bool
	isLast             bool
	gutters            []gutterInfo
	isVirtualRootChild bool
}

// buildTree turns the flat list a store gives into a tree, each entry's children
// oldest first, and anything whose parent is gone a root — Pi's getTree.
func buildTree(nodes []session.Node) []*treeNode {
	byID := make(map[string]*treeNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = &treeNode{Node: n}
	}
	var roots []*treeNode
	for _, n := range nodes {
		node := byID[n.ID]
		parent, found := byID[n.ParentID]
		if n.ParentID == "" || n.ParentID == n.ID || !found {
			roots = append(roots, node)
			continue
		}
		parent.children = append(parent.children, node)
	}
	for _, node := range byID {
		slices.SortStableFunc(node.children, func(a, b *treeNode) int { return a.At.Compare(b.At) })
	}
	return roots
}

// treeList is the rows, the cursor and the filters — Pi's TreeList.
type treeList struct {
	env           *Env
	flat          []*flatTreeNode
	filtered      []*flatTreeNode
	selected      int
	leaf          string
	maxVisible    int
	filterMode    string
	query         string
	toolCalls     map[string]ai.ToolCall
	multipleRoots bool
	showLabelTime bool
	activePath    map[string]bool
	visibleParent map[string]string
	visibleKids   map[string][]string
	lastSelected  string
	folded        map[string]bool
	now           func() time.Time

	onSelect    func(string)
	onCancel    func()
	onCopy      func(text string, ok bool)
	onLabelEdit func(id, current string)
}

func newTreeList(env *Env, roots []*treeNode, leaf string, maxVisible int, initial, filterMode string) *treeList {
	if filterMode == "" {
		filterMode = TreeFilterDefault
	}
	l := &treeList{env: env, leaf: leaf, maxVisible: maxVisible, filterMode: filterMode,
		toolCalls: map[string]ai.ToolCall{}, activePath: map[string]bool{},
		folded: map[string]bool{}, multipleRoots: len(roots) > 1, now: time.Now}
	l.flat = l.flatten(roots)
	l.buildActivePath()
	l.applyFilter()
	target := initial
	if target == "" {
		target = leaf
	}
	l.selected = l.nearestVisible(target)
	if l.selected < len(l.filtered) {
		l.lastSelected = l.filtered[l.selected].node.ID
	}
	return l
}

func (l *treeList) entryMap() map[string]*flatTreeNode {
	out := make(map[string]*flatTreeNode, len(l.flat))
	for _, f := range l.flat {
		out[f.node.ID] = f
	}
	return out
}

// nearestVisible is where the cursor belongs for an entry: the entry itself, or the
// closest ancestor that is showing, or failing that the last row.
func (l *treeList) nearestVisible(id string) int {
	if len(l.filtered) == 0 {
		return 0
	}
	entries := l.entryMap()
	visible := make(map[string]int, len(l.filtered))
	for i, f := range l.filtered {
		visible[f.node.ID] = i
	}
	for current := id; current != ""; {
		if at, ok := visible[current]; ok {
			return at
		}
		node, ok := entries[current]
		if !ok {
			break
		}
		current = node.node.ParentID
	}
	return len(l.filtered) - 1
}

func (l *treeList) buildActivePath() {
	l.activePath = map[string]bool{}
	if l.leaf == "" {
		return
	}
	entries := l.entryMap()
	for current := l.leaf; current != ""; {
		l.activePath[current] = true
		node, ok := entries[current]
		if !ok {
			break
		}
		current = node.node.ParentID
	}
}

// flatten walks the tree with the branch holding the current position first.
//
// Indentation stays flat along a chain and steps in only where the conversation
// branched, and once more just after, to group a subtree by eye.
func (l *treeList) flatten(roots []*treeNode) []*flatTreeNode {
	contains := map[*treeNode]bool{}
	var mark func(*treeNode) bool
	mark = func(n *treeNode) bool {
		has := l.leaf != "" && n.ID == l.leaf
		for _, c := range n.children {
			if mark(c) {
				has = true
			}
		}
		contains[n] = has
		return has
	}
	for _, r := range roots {
		mark(r)
	}
	prioritized := func(nodes []*treeNode) []*treeNode {
		out := make([]*treeNode, 0, len(nodes))
		for _, n := range nodes {
			if contains[n] {
				out = append(out, n)
			}
		}
		for _, n := range nodes {
			if !contains[n] {
				out = append(out, n)
			}
		}
		return out
	}

	type item struct {
		node          *treeNode
		indent        int
		justBranched  bool
		showConnector bool
		isLast        bool
		gutters       []gutterInfo
		virtual       bool
	}
	var result []*flatTreeNode
	var stack []item
	multiple := len(roots) > 1
	ordered := prioritized(roots)
	startIndent := 0
	if multiple {
		startIndent = 1
	}
	for i := len(ordered) - 1; i >= 0; i-- {
		stack = append(stack, item{ordered[i], startIndent, multiple, multiple, i == len(ordered)-1, nil, multiple})
	}
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if m := it.node.Entry.Message; m != nil && m.Role == ai.RoleAssistant {
			for _, call := range m.ToolCalls {
				l.toolCalls[call.ID] = call
			}
		}
		result = append(result, &flatTreeNode{node: it.node, indent: it.indent, showConnector: it.showConnector,
			isLast: it.isLast, gutters: it.gutters, isVirtualRootChild: it.virtual})

		children := prioritized(it.node.children)
		branching := len(children) > 1
		childIndent := it.indent
		if branching || (it.justBranched && it.indent > 0) {
			childIndent = it.indent + 1
		}
		childGutters := l.childGutters(it.gutters, it.indent, it.showConnector, it.isLast, it.virtual)
		for i := len(children) - 1; i >= 0; i-- {
			stack = append(stack, item{children[i], childIndent, branching, branching, i == len(children)-1, childGutters, false})
		}
	}
	return result
}

func (l *treeList) childGutters(gutters []gutterInfo, indent int, showConnector, isLast, virtual bool) []gutterInfo {
	if !showConnector || virtual {
		return gutters
	}
	display := indent
	if l.multipleRoots {
		display = max(0, indent-1)
	}
	return append(slices.Clip(gutters), gutterInfo{position: max(0, display-1), show: !isLast})
}

// isBookkeeping is what the default view leaves out. Pi's list of settings entries,
// and the records of tool calls and failures pi-go keeps beside the conversation.
func isBookkeeping(e session.Entry) bool {
	return e.Label != nil || e.Model != nil || e.Thinking != nil || e.Info != nil ||
		e.Overflow != nil || e.Intent != nil || e.Settlement != nil || e.Failure != nil
}

func (l *treeList) applyFilter() {
	if len(l.filtered) > 0 && l.selected < len(l.filtered) {
		l.lastSelected = l.filtered[l.selected].node.ID
	}
	tokens := strings.Fields(strings.ToLower(l.query))

	l.filtered = nil
	for _, f := range l.flat {
		entry := f.node.Entry
		// A reply that only called tools says nothing a person could pick; its
		// results stand for it. The position itself always shows.
		if m := entry.Message; m != nil && m.Role == ai.RoleAssistant && f.node.ID != l.leaf {
			if strings.TrimSpace(m.Content) == "" {
				continue
			}
		}
		keep := true
		switch l.filterMode {
		case TreeFilterUser:
			keep = entry.Message != nil && entry.Message.Role == ai.RoleUser && entry.Message.BashExecution == nil
		case TreeFilterNoTools:
			keep = !isBookkeeping(entry) && !(entry.Message != nil && entry.Message.Role == ai.RoleTool)
		case TreeFilterLabeled:
			keep = f.node.Label != ""
		case TreeFilterAll:
		default:
			keep = !isBookkeeping(entry)
		}
		if !keep {
			continue
		}
		if len(tokens) > 0 {
			text := strings.ToLower(searchableText(f.node))
			matched := true
			for _, token := range tokens {
				if !strings.Contains(text, token) {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
		}
		l.filtered = append(l.filtered, f)
	}

	if len(l.folded) > 0 {
		skip := map[string]bool{}
		for _, f := range l.flat {
			if parent := f.node.ParentID; parent != "" && (l.folded[parent] || skip[parent]) {
				skip[f.node.ID] = true
			}
		}
		l.filtered = slices.DeleteFunc(l.filtered, func(f *flatTreeNode) bool { return skip[f.node.ID] })
	}

	l.recalculate()

	switch {
	case l.lastSelected != "":
		l.selected = l.nearestVisible(l.lastSelected)
	case l.selected >= len(l.filtered):
		l.selected = max(0, len(l.filtered)-1)
	}
	if l.selected < len(l.filtered) {
		l.lastSelected = l.filtered[l.selected].node.ID
	}
}

// recalculate redoes the indentation for what is showing: hiding an entry attaches
// what follows it to the nearest visible ancestor, and a chain must not drift right
// because entries in it were hidden.
func (l *treeList) recalculate() {
	if len(l.filtered) == 0 {
		return
	}
	visible := make(map[string]*flatTreeNode, len(l.filtered))
	for _, f := range l.filtered {
		visible[f.node.ID] = f
	}
	entries := l.entryMap()
	ancestor := func(id string) string {
		for current := entries[id].node.ParentID; current != ""; {
			if _, ok := visible[current]; ok {
				return current
			}
			next, ok := entries[current]
			if !ok {
				return ""
			}
			current = next.node.ParentID
		}
		return ""
	}
	parents := make(map[string]string, len(l.filtered))
	kids := map[string][]string{"": nil}
	for _, f := range l.filtered {
		parent := ancestor(f.node.ID)
		parents[f.node.ID] = parent
		kids[parent] = append(kids[parent], f.node.ID)
	}
	roots := kids[""]
	l.multipleRoots = len(roots) > 1

	type item struct {
		id            string
		indent        int
		justBranched  bool
		showConnector bool
		isLast        bool
		gutters       []gutterInfo
		virtual       bool
	}
	var stack []item
	startIndent := 0
	if l.multipleRoots {
		startIndent = 1
	}
	for i := len(roots) - 1; i >= 0; i-- {
		stack = append(stack, item{roots[i], startIndent, l.multipleRoots, l.multipleRoots, i == len(roots)-1, nil, l.multipleRoots})
	}
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		f := visible[it.id]
		if f == nil {
			continue
		}
		f.indent, f.showConnector, f.isLast, f.gutters, f.isVirtualRootChild =
			it.indent, it.showConnector, it.isLast, it.gutters, it.virtual

		children := kids[it.id]
		branching := len(children) > 1
		childIndent := it.indent
		if branching || (it.justBranched && it.indent > 0) {
			childIndent = it.indent + 1
		}
		childGutters := l.childGutters(it.gutters, it.indent, it.showConnector, it.isLast, it.virtual)
		for i := len(children) - 1; i >= 0; i-- {
			stack = append(stack, item{children[i], childIndent, branching, branching, i == len(children)-1, childGutters, false})
		}
	}
	l.visibleParent, l.visibleKids = parents, kids
}

// searchableText is what a search looks through.
func searchableText(n *treeNode) string {
	var parts []string
	if n.Label != "" {
		parts = append(parts, n.Label)
	}
	e := n.Entry
	switch {
	case e.Message != nil:
		role := string(e.Message.Role)
		if e.Message.Role == ai.RoleTool {
			role = "toolResult"
		}
		if e.Message.BashExecution != nil {
			parts = append(parts, "bashExecution", e.Message.BashExecution.Command)
		} else {
			parts = append(parts, role, truncateUnits(e.Message.Content, treeContentCutoff))
		}
	case e.Checkpoint != nil:
		parts = append(parts, "compaction")
	case e.BranchSummary != nil:
		parts = append(parts, "branch summary", e.BranchSummary.Summary)
	case e.Info != nil:
		parts = append(parts, "title")
		if e.Info.Name != "" {
			parts = append(parts, e.Info.Name)
		}
	case e.Model != nil:
		parts = append(parts, "model", e.Model.Model)
	case e.Thinking != nil:
		parts = append(parts, "thinking", string(e.Thinking.Level))
	case e.Label != nil:
		parts = append(parts, "label", e.Label.Label)
	case e.Intent != nil:
		parts = append(parts, "tool", e.Intent.Tool)
	case e.Settlement != nil:
		parts = append(parts, "tool result")
	case e.Failure != nil:
		parts = append(parts, "failed", e.Failure.Code)
	case e.Overflow != nil:
		parts = append(parts, "overflow")
	}
	return strings.Join(parts, " ")
}

// truncateUnits cuts a string to at most n characters, not splitting one.
func truncateUnits(s string, n int) string {
	if len(s) <= n {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func (l *treeList) statusLabels() string {
	labels := ""
	switch l.filterMode {
	case TreeFilterNoTools:
		labels += " [no-tools]"
	case TreeFilterUser:
		labels += " [user]"
	case TreeFilterLabeled:
		labels += " [labeled]"
	case TreeFilterAll:
		labels += " [all]"
	}
	if l.showLabelTime {
		labels += " [+label time]"
	}
	return labels
}

func (l *treeList) Invalidate() {}

type treeViewportRow struct {
	gutter, body string
	anchorCol    int
	bodyWidth    int
	isSelected   bool
}

// renderTreeViewport pans the rows sideways when the selected one sits too far
// right to read, keeping the cursor's gutter in place — Pi's
// renderHorizontalViewport.
func renderTreeViewport(rows []treeViewportRow, width int) []string {
	viewport := max(0, width-treeGutterWidth)
	widest := 0
	var selected *treeViewportRow
	for i := range rows {
		widest = max(widest, rows[i].bodyWidth)
		if rows[i].isSelected {
			selected = &rows[i]
		}
	}
	maxScroll := max(0, widest-viewport)
	scroll := 0
	if selected != nil && maxScroll > 0 {
		visibleContent := min(maxVisibleAnchorContent, max(minVisibleAnchorContent, viewport/3))
		if selected.anchorCol > viewport-visibleContent {
			context := min(maxAnchorContext, max(minAnchorContext, viewport/4))
			scroll = min(maxScroll, selected.anchorCol-context)
		}
	}
	out := make([]string, len(rows))
	for i, row := range rows {
		line := row.gutter + row.body
		if scroll > 0 {
			line = row.gutter + tui.SliceByColumn(row.body, scroll, viewport, true) + "\x1b[0m"
		}
		out[i] = tui.TruncateToWidth(line, width, "", false)
	}
	return out
}

func (l *treeList) Render(width int) []string {
	t := l.env.Theme
	if len(l.filtered) == 0 {
		return []string{
			tui.TruncateToWidth(t.Fg("muted", "  No entries found"), width, "...", false),
			tui.TruncateToWidth(t.Fg("muted", "  (0/0)"+l.statusLabels()), width, "...", false),
		}
	}
	start, end := windowOf(l.selected, len(l.filtered), l.maxVisible)
	rows := make([]treeViewportRow, 0, end-start)
	for i := start; i < end; i++ {
		f := l.filtered[i]
		selected := i == l.selected
		cursor := "  "
		if selected {
			cursor = t.Fg("accent", "› ")
		}
		display := f.indent
		if l.multipleRoots {
			display = max(0, f.indent-1)
		}
		hasConnector := f.showConnector && !f.isVirtualRootChild
		connectorAt := -1
		if hasConnector {
			connectorAt = display - 1
		}
		folded := l.folded[f.node.ID]
		var prefix strings.Builder
		for c := 0; c < display*3; c++ {
			level, inLevel := c/3, c%3
			at := slices.IndexFunc(f.gutters, func(g gutterInfo) bool { return g.position == level })
			switch {
			case at >= 0:
				if inLevel == 0 && f.gutters[at].show {
					prefix.WriteString("│")
				} else {
					prefix.WriteString(" ")
				}
			case hasConnector && level == connectorAt:
				switch inLevel {
				case 0:
					if f.isLast {
						prefix.WriteString("└")
					} else {
						prefix.WriteString("├")
					}
				case 1:
					switch {
					case folded:
						prefix.WriteString("⊞")
					case l.isFoldable(f.node.ID):
						prefix.WriteString("⊟")
					default:
						prefix.WriteString("─")
					}
				default:
					prefix.WriteString(" ")
				}
			default:
				prefix.WriteString(" ")
			}
		}
		foldMarker := ""
		if folded && !hasConnector {
			foldMarker = t.Fg("accent", "⊞ ")
		}
		pathMarker := ""
		if l.activePath[f.node.ID] {
			pathMarker = t.Fg("accent", "• ")
		}
		label := ""
		if f.node.Label != "" {
			label = t.Fg("warning", "["+f.node.Label+"] ")
		}
		labelTime := ""
		if l.showLabelTime && f.node.Label != "" && !f.node.LabelAt.IsZero() {
			labelTime = t.Fg("muted", formatLabelTime(f.node.LabelAt, l.now())+" ")
		}
		content := l.displayText(f.node, selected)
		prefixPart := t.Fg("dim", prefix.String()) + foldMarker + pathMarker
		anchor := tui.VisibleWidth(prefixPart)
		gutter, body := cursor, prefixPart+label+labelTime+content
		if selected {
			gutter, body = t.Bg("selectedBg", gutter), t.Bg("selectedBg", body)
		}
		rows = append(rows, treeViewportRow{gutter: gutter, body: body, anchorCol: anchor,
			bodyWidth: tui.VisibleWidth(body), isSelected: selected})
	}
	lines := renderTreeViewport(rows, width)
	return append(lines, tui.TruncateToWidth(
		t.Fg("muted", "  ("+strconv.Itoa(l.selected+1)+"/"+strconv.Itoa(len(l.filtered))+")"+l.statusLabels()), width, "...", false))
}

func formatLabelTime(at, now time.Time) string {
	at = at.Local()
	now = now.Local()
	clock := fmt.Sprintf("%02d:%02d", at.Hour(), at.Minute())
	if at.Year() == now.Year() && at.YearDay() == now.YearDay() {
		return clock
	}
	if at.Year() == now.Year() {
		return fmt.Sprintf("%d/%d %s", int(at.Month()), at.Day(), clock)
	}
	return fmt.Sprintf("%02d/%d/%d %s", at.Year()%100, int(at.Month()), at.Day(), clock)
}

var treeNormalizer = regexp.MustCompile(`[\n\t]`)

func normalizeTreeText(s string) string {
	return strings.TrimSpace(treeNormalizer.ReplaceAllString(s, " "))
}

func (l *treeList) displayText(n *treeNode, selected bool) string {
	t := l.env.Theme
	e := n.Entry
	var result string
	switch {
	case e.Message != nil:
		m := e.Message
		switch {
		case m.BashExecution != nil:
			result = t.Fg("dim", "[bash]: "+normalizeTreeText(m.BashExecution.Command))
		case m.Role == ai.RoleUser:
			result = t.Fg("accent", "user: ") + normalizeTreeText(truncateUnits(m.Content, treeContentCutoff))
		case m.Role == ai.RoleAssistant:
			if text := normalizeTreeText(truncateUnits(m.Content, treeContentCutoff)); text != "" {
				result = t.Fg("success", "assistant: ") + text
			} else {
				result = t.Fg("success", "assistant: ") + t.Fg("muted", "(no content)")
			}
		case m.Role == ai.RoleTool:
			if call, ok := l.toolCalls[m.ToolCallID]; ok {
				result = t.Fg("muted", l.formatToolCall(call))
			} else {
				result = t.Fg("muted", "[tool]")
			}
		default:
			result = t.Fg("dim", "["+string(m.Role)+"]")
		}
	case e.Checkpoint != nil:
		if e.Checkpoint.TokensBefore > 0 {
			tokens := int(math.Round(float64(e.Checkpoint.TokensBefore) / 1000))
			result = t.Fg("borderAccent", "[compaction: "+strconv.Itoa(tokens)+"k tokens]")
		} else {
			result = t.Fg("borderAccent", "[compaction]")
		}
	case e.BranchSummary != nil:
		result = t.Fg("warning", "[branch summary]: ") + normalizeTreeText(e.BranchSummary.Summary)
	case e.Model != nil:
		result = t.Fg("dim", "[model: "+e.Model.Model+"]")
	case e.Thinking != nil:
		result = t.Fg("dim", "[thinking: "+string(e.Thinking.Level)+"]")
	case e.Label != nil:
		text := e.Label.Label
		if text == "" {
			text = "(cleared)"
		}
		result = t.Fg("dim", "[label: "+text+"]")
	case e.Info != nil:
		if e.Info.Name != "" {
			result = t.Fg("dim", "[title: ") + t.Fg("dim", e.Info.Name) + t.Fg("dim", "]")
		} else {
			result = t.Fg("dim", "[title: ") + Italic(t.Fg("dim", "empty")) + t.Fg("dim", "]")
		}
	case e.Intent != nil:
		result = t.Fg("dim", "[tool: "+e.Intent.Tool+"]")
	case e.Settlement != nil:
		result = t.Fg("dim", "[tool result]")
	case e.Failure != nil:
		result = t.Fg("dim", "[failed: "+e.Failure.Code+"]")
	case e.Overflow != nil:
		result = t.Fg("dim", "[overflow]")
	}
	if selected {
		return Bold(result)
	}
	return result
}

func (l *treeList) shortenPath(path string) string {
	if home := l.env.Home; home != "" && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

// formatToolCall is one line about what a tool call did — Pi's formatToolCall.
func (l *treeList) formatToolCall(call ai.ToolCall) string {
	var args map[string]any
	_ = json.Unmarshal([]byte(call.Args), &args)
	text := func(key string) string {
		if v, ok := args[key].(string); ok {
			return v
		}
		return ""
	}
	path := func() string {
		if p := text("path"); p != "" {
			return l.shortenPath(p)
		}
		return l.shortenPath(text("file_path"))
	}
	pathOrHere := func() string {
		if p := text("path"); p != "" {
			return l.shortenPath(p)
		}
		return "."
	}
	switch call.Name {
	case "read":
		display := path()
		offset, hasOffset := args["offset"].(float64)
		limit, hasLimit := args["limit"].(float64)
		if hasOffset || hasLimit {
			begin := 1.0
			if hasOffset {
				begin = offset
			}
			display += ":" + strconv.FormatFloat(begin, 'f', -1, 64)
			if hasLimit {
				display += "-" + strconv.FormatFloat(begin+limit-1, 'f', -1, 64)
			}
		}
		return "[read: " + display + "]"
	case "write":
		return "[write: " + path() + "]"
	case "edit":
		return "[edit: " + path() + "]"
	case "bash":
		raw := text("command")
		command := truncateUnits(normalizeTreeText(raw), treeCommandCutoff)
		suffix := ""
		if len([]rune(raw)) > treeCommandCutoff {
			suffix = "..."
		}
		return "[bash: " + command + suffix + "]"
	case "grep":
		return "[grep: /" + text("pattern") + "/ in " + pathOrHere() + "]"
	case "find":
		return "[find: " + text("pattern") + " in " + pathOrHere() + "]"
	case "ls":
		return "[ls: " + pathOrHere() + "]"
	default:
		encoded, _ := json.Marshal(args)
		suffix := ""
		if len([]rune(string(encoded))) > treeToolArgsCutoff {
			suffix = "..."
		}
		return "[" + call.Name + ": " + truncateUnits(string(encoded), treeToolArgsCutoff) + suffix + "]"
	}
}

// copyText is what the copy key takes from an entry.
func copyText(n *treeNode) (string, bool) {
	e := n.Entry
	text := ""
	switch {
	case e.Message != nil && e.Message.BashExecution != nil:
		text = e.Message.BashExecution.Command
	case e.Message != nil:
		text = e.Message.Content
	case e.Checkpoint != nil:
		text = e.Checkpoint.Summary
	case e.BranchSummary != nil:
		text = e.BranchSummary.Summary
	}
	return text, strings.TrimSpace(text) != ""
}

func (l *treeList) setFilter(mode string) {
	l.filterMode = mode
	l.folded = map[string]bool{}
	l.applyFilter()
}

func (l *treeList) toggleFilter(mode string) {
	if l.filterMode == mode {
		mode = TreeFilterDefault
	}
	l.setFilter(mode)
}

// HandleKey moves, folds, filters, searches or chooses.
func (l *treeList) HandleKey(k tui.Key) {
	kb := l.env.Keys
	count := len(l.filtered)
	current := ""
	if l.selected < count {
		current = l.filtered[l.selected].node.ID
	}
	cycle := func(step int) {
		at := slices.Index(treeFilterModes, l.filterMode)
		l.setFilter(treeFilterModes[(at+step+len(treeFilterModes))%len(treeFilterModes)])
	}
	switch {
	case kb.Matches(k, "tui.select.up"):
		if count > 0 {
			l.selected = wrapUp(l.selected, count)
		}
	case kb.Matches(k, "tui.select.down"):
		if count > 0 {
			l.selected = wrapDown(l.selected, count)
		}
	case kb.Matches(k, "app.tree.foldOrUp"):
		if current != "" && l.isFoldable(current) && !l.folded[current] {
			l.folded[current] = true
			l.applyFilter()
		} else {
			l.selected = l.branchSegmentStart(false)
		}
	case kb.Matches(k, "app.tree.unfoldOrDown"):
		if current != "" && l.folded[current] {
			delete(l.folded, current)
			l.applyFilter()
		} else {
			l.selected = l.branchSegmentStart(true)
		}
	case kb.Matches(k, "tui.editor.cursorLeft"), kb.Matches(k, "tui.select.pageUp"):
		l.selected = max(0, l.selected-l.maxVisible)
	case kb.Matches(k, "tui.editor.cursorRight"), kb.Matches(k, "tui.select.pageDown"):
		l.selected = max(0, min(count-1, l.selected+l.maxVisible))
	case kb.Matches(k, "tui.select.confirm"):
		if current != "" {
			l.onSelect(current)
		}
	case kb.Matches(k, "app.message.copy"):
		if current == "" {
			l.onCopy("", false)
		} else {
			l.onCopy(copyText(l.filtered[l.selected].node))
		}
	case kb.Matches(k, "tui.select.cancel"):
		if l.query != "" {
			l.query = ""
			l.folded = map[string]bool{}
			l.applyFilter()
		} else {
			l.onCancel()
		}
	case kb.Matches(k, "app.tree.filter.default"):
		l.setFilter(TreeFilterDefault)
	case kb.Matches(k, "app.tree.filter.noTools"):
		l.toggleFilter(TreeFilterNoTools)
	case kb.Matches(k, "app.tree.filter.userOnly"):
		l.toggleFilter(TreeFilterUser)
	case kb.Matches(k, "app.tree.filter.labeledOnly"):
		l.toggleFilter(TreeFilterLabeled)
	case kb.Matches(k, "app.tree.filter.all"):
		l.toggleFilter(TreeFilterAll)
	case kb.Matches(k, "app.tree.filter.cycleBackward"):
		cycle(-1)
	case kb.Matches(k, "app.tree.filter.cycleForward"):
		cycle(1)
	case kb.Matches(k, "tui.editor.deleteCharBackward"):
		if l.query != "" {
			runes := []rune(l.query)
			l.query = string(runes[:len(runes)-1])
			l.folded = map[string]bool{}
			l.applyFilter()
		}
	case kb.Matches(k, "app.tree.editLabel"):
		if current != "" {
			l.onLabelEdit(current, l.filtered[l.selected].node.Label)
		}
	case kb.Matches(k, "app.tree.toggleLabelTimestamp"):
		l.showLabelTime = !l.showLabelTime
	case k.IsText():
		l.query += string(k.Text)
		l.folded = map[string]bool{}
		l.applyFilter()
	}
}

// isFoldable is a node with something under it that starts a branch or a root.
func (l *treeList) isFoldable(id string) bool {
	if len(l.visibleKids[id]) == 0 {
		return false
	}
	parent := l.visibleParent[id]
	if parent == "" {
		return true
	}
	return len(l.visibleKids[parent]) > 1
}

// branchSegmentStart is where the branch keys land: up walks to the start of this
// branch, down follows first children to the next place the line divides.
func (l *treeList) branchSegmentStart(down bool) int {
	if l.selected >= len(l.filtered) {
		return l.selected
	}
	index := make(map[string]int, len(l.filtered))
	for i, f := range l.filtered {
		index[f.node.ID] = i
	}
	current := l.filtered[l.selected].node.ID
	if down {
		for {
			children := l.visibleKids[current]
			switch len(children) {
			case 0:
				return index[current]
			case 1:
				current = children[0]
			default:
				return index[children[0]]
			}
		}
	}
	for {
		parent := l.visibleParent[current]
		if parent == "" {
			return index[current]
		}
		if len(l.visibleKids[parent]) > 1 && index[current] < l.selected {
			return index[current]
		}
		current = parent
	}
}

// treeSearchLine shows what has been typed.
type treeSearchLine struct{ list *treeList }

func (s treeSearchLine) Invalidate() {}
func (s treeSearchLine) Render(width int) []string {
	t := s.list.env.Theme
	line := "  " + t.Fg("muted", "Type to search:")
	if s.list.query != "" {
		line += " " + t.Fg("accent", s.list.query)
	}
	return []string{tui.TruncateToWidth(line, width, "...", false)}
}

var treeHelpItems = []struct {
	keys       []string
	label      string
	labelFirst bool
}{
	{keys: []string{"tui.select.up", "tui.select.down"}, label: "move"},
	{keys: []string{"tui.editor.cursorLeft", "tui.editor.cursorRight"}, label: "page"},
	{keys: []string{"app.tree.foldOrUp", "app.tree.unfoldOrDown"}, label: "branch"},
	{keys: []string{"app.message.copy"}, label: "copy"},
	{keys: []string{"app.tree.editLabel"}, label: "label"},
	{keys: []string{"app.tree.toggleLabelTimestamp"}, label: "label time"},
	{keys: []string{"app.tree.filter.default", "app.tree.filter.noTools", "app.tree.filter.userOnly",
		"app.tree.filter.labeledOnly", "app.tree.filter.all"}, label: "filters", labelFirst: true},
	{keys: []string{"app.tree.filter.cycleForward", "app.tree.filter.cycleBackward"}, label: "cycle", labelFirst: true},
}

// treeHelp is the keys, wrapped between items rather than inside one.
type treeHelp struct{ env *Env }

func (h treeHelp) Invalidate() {}

func (h treeHelp) formatKeys(ids []string) string {
	var keys []string
	for _, id := range ids {
		if bound := h.env.Keys.Keys(id); len(bound) > 0 {
			keys = append(keys, bound[0])
		}
	}
	if len(keys) == 0 {
		return ""
	}
	text := h.env.formatKeyText(compactRawKeys(keys), false)
	for _, r := range []struct{ word, symbol string }{
		{"pageUp", "pgup"}, {"pageDown", "pgdn"}, {"up", "↑"}, {"down", "↓"}, {"left", "←"}, {"right", "→"},
	} {
		text = replaceWord(text, r.word, r.symbol)
	}
	return text
}

// replaceWord replaces word where it stands alone, as \bword\b does.
func replaceWord(text, word, with string) string {
	return regexp.MustCompile(`\b`+word+`\b`).ReplaceAllString(text, with)
}

// compactRawKeys writes keys with one modifier prefix once: ctrl+d/t rather than
// ctrl+d/ctrl+t.
func compactRawKeys(keys []string) string {
	if len(keys) == 1 {
		return keys[0]
	}
	split := func(key string) (string, string) {
		at := strings.LastIndex(key, "+")
		if at < 0 {
			return "", key
		}
		return key[:at+1], key[at+1:]
	}
	prefix, _ := split(keys[0])
	suffixes := make([]string, len(keys))
	same := prefix != ""
	for i, key := range keys {
		p, s := split(key)
		suffixes[i] = s
		same = same && p == prefix
	}
	if same {
		return prefix + strings.Join(suffixes, "/")
	}
	return strings.Join(keys, "/")
}

func (h treeHelp) Render(width int) []string {
	available := max(1, width)
	var lines []string
	current := ""
	indented := func(item string) string {
		if tui.VisibleWidth(treeHelpIndent+item) <= available {
			return treeHelpIndent + item
		}
		return item
	}
	for _, spec := range treeHelpItems {
		item := spec.label
		if text := h.formatKeys(spec.keys); text != "" {
			if spec.labelFirst {
				item = spec.label + " " + text
			} else {
				item = text + " " + spec.label
			}
		}
		candidate := indented(item)
		if current != "" {
			candidate = current + treeHelpSeparator + item
		}
		if current == "" || tui.VisibleWidth(candidate) <= available {
			current = candidate
			continue
		}
		lines = append(lines, tui.WrapText(strings.TrimRight(current, " "), available)...)
		current = indented(item)
	}
	if current != "" {
		lines = append(lines, tui.WrapText(strings.TrimRight(current, " "), available)...)
	}
	for i, line := range lines {
		lines[i] = h.env.Theme.Fg("muted", line)
	}
	return lines
}

// treeLabelInput asks for a bookmark's words.
type treeLabelInput struct {
	env      *Env
	input    tui.Input
	id       string
	onSubmit func(id, label string)
	onCancel func()
}

func (i *treeLabelInput) Invalidate() {}

func (i *treeLabelInput) Render(width int) []string {
	const indent = "  "
	lines := []string{tui.TruncateToWidth(indent+i.env.Theme.Fg("muted", "Label (empty to remove):"), width, "...", false)}
	for _, line := range i.input.Render(width - len(indent)) {
		lines = append(lines, tui.TruncateToWidth(indent+line, width, "...", false))
	}
	return append(lines, tui.TruncateToWidth(indent+i.env.KeyHint("tui.select.confirm", "save")+"  "+
		i.env.KeyHint("tui.select.cancel", "cancel"), width, "...", false))
}

func (i *treeLabelInput) HandleKey(k tui.Key) {
	kb := i.env.Keys
	switch {
	case kb.Matches(k, "tui.select.confirm"):
		i.onSubmit(i.id, strings.TrimSpace(i.input.Value()))
	case kb.Matches(k, "tui.select.cancel"):
		i.onCancel()
	default:
		i.input.HandleKey(k)
	}
}

// TreeSelector moves within a conversation — Pi's TreeSelectorComponent: the whole
// tree with the current line marked, filters, search, folding, bookmarks.
type TreeSelector struct {
	tui.Container
	env        *Env
	list       *treeList
	label      *treeLabelInput
	treeC      tui.Container
	labelC     tui.Container
	focused    bool
	onLabelSet func(id, label string)

	// OnCopy receives what the copy key took, ok false when the entry had nothing.
	OnCopy func(text string, ok bool)
}

// NewTreeSelector shows nodes as a tree. terminalHeight sizes the list; initial is
// the entry to start on, empty for the current one.
func NewTreeSelector(env *Env, nodes []session.Node, leaf string, terminalHeight int, initial, filterMode string,
	onSelect func(string), onCancel func(), onLabelSet func(id, label string)) *TreeSelector {

	s := &TreeSelector{env: env, onLabelSet: onLabelSet}
	s.list = newTreeList(env, buildTree(nodes), leaf, max(5, terminalHeight/2), initial, filterMode)
	s.list.onSelect = onSelect
	s.list.onCancel = onCancel
	s.list.onCopy = func(text string, ok bool) {
		if s.OnCopy != nil {
			s.OnCopy(text, ok)
		}
	}
	s.list.onLabelEdit = s.showLabelInput
	s.treeC.AddChild(s.list)

	s.AddChild(spacer())
	s.AddChild(newBorder(env))
	s.AddChild(tui.NewText(Bold("  Session Tree"), 1, 0, nil))
	s.AddChild(treeHelp{env})
	s.AddChild(treeSearchLine{s.list})
	s.AddChild(newBorder(env))
	s.AddChild(spacer())
	s.AddChild(&s.treeC)
	s.AddChild(&s.labelC)
	s.AddChild(spacer())
	s.AddChild(newBorder(env))
	return s
}

func (s *TreeSelector) showLabelInput(id, current string) {
	input := &treeLabelInput{env: s.env, id: id}
	input.input.Keybindings = s.env.Keys
	if current != "" {
		input.input.SetValue(current)
	}
	input.onSubmit = func(id, label string) {
		for _, f := range s.list.flat {
			if f.node.ID == id {
				f.node.Label = label
				f.node.LabelAt = time.Time{}
				if label != "" {
					f.node.LabelAt = time.Now()
				}
				break
			}
		}
		s.onLabelSet(id, label)
		s.hideLabelInput()
	}
	input.onCancel = s.hideLabelInput
	input.input.SetFocused(s.focused)
	s.label = input
	s.treeC.Clear()
	s.labelC.Clear()
	s.labelC.AddChild(input)
}

func (s *TreeSelector) hideLabelInput() {
	s.label = nil
	s.labelC.Clear()
	s.treeC.Clear()
	s.treeC.AddChild(s.list)
}

// SetFocused passes focus to the label input while one is open.
func (s *TreeSelector) SetFocused(focused bool) {
	s.focused = focused
	if s.label != nil {
		s.label.input.SetFocused(focused)
	}
}

// HandleKey goes to the label input while one is open, otherwise to the list.
func (s *TreeSelector) HandleKey(k tui.Key) {
	if s.label != nil {
		s.label.HandleKey(k)
		return
	}
	s.list.HandleKey(k)
}
