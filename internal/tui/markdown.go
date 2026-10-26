package tui

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// MarkdownTheme styles the parts of rendered Markdown.
type MarkdownTheme struct {
	Heading, Link, LinkURL, Code, CodeBlock, CodeBlockBorder Style
	Quote, QuoteBorder, HR, ListBullet                       Style
	Bold, Italic, Strikethrough, Underline                   Style
	// HighlightCode, if set, colours a code block's lines.
	HighlightCode func(code, lang string) []string
	// CodeBlockIndent prefixes each line of a code block; nil is two spaces.
	CodeBlockIndent *string
}

// DefaultTextStyle styles all text a Markdown component draws, beneath
// whatever the Markdown itself asks for.
type DefaultTextStyle struct {
	Color, BgColor                         Style
	Bold, Italic, Strikethrough, Underline bool
}

// MarkdownOptions adjust how Markdown is read.
type MarkdownOptions struct {
	// PreserveOrderedListMarkers keeps the source's list markers — "1)",
	// "*", "+" — instead of drawing every list as "1." or "-".
	PreserveOrderedListMarkers bool
	// PreserveBackslashEscapes keeps backslash escapes as typed.
	PreserveBackslashEscapes bool
	// Transform rewrites the source before it is parsed, given the width
	// the content has.
	Transform func(markdown string, width int) string
}

// Markdown is text rendered from Markdown to styled terminal lines — Pi's
// Markdown component: headings, emphasis, code blocks, lists, quotes,
// tables and links, wrapped to the width and padded to it.
//
// The rules are Pi's, checked against output Pi itself produced
// (testdata/markdown). Pi parses with marked and this with goldmark, so the
// two read Markdown's corner cases a little differently; where Pi's output
// depends on marked's tokens — the blank lines between blocks, a closing
// fence still arriving — the token structure is reconstructed from the
// source. LaTeX is drawn as written, as Pi draws it with math rendering off.
type Markdown struct {
	text               string
	paddingX, paddingY int
	theme              MarkdownTheme
	style              *DefaultTextStyle
	options            MarkdownOptions

	cachedText  string
	cachedWidth int
	cached      []string
	valid       bool
}

// NewMarkdown is a Markdown component. style may be nil.
func NewMarkdown(text string, paddingX, paddingY int, theme MarkdownTheme, style *DefaultTextStyle, options MarkdownOptions) *Markdown {
	return &Markdown{text: text, paddingX: paddingX, paddingY: paddingY, theme: theme, style: style, options: options}
}

// SetText replaces the Markdown, as a reply streams in.
func (m *Markdown) SetText(text string) {
	m.text = text
	m.Invalidate()
}

// Text is the Markdown source.
func (m *Markdown) Text() string { return m.text }

// Invalidate drops the cached rendering.
func (m *Markdown) Invalidate() { m.valid = false }

// marked links an address without asking that its domain have a dot, so an
// address still streaming in — "https://exa" — is a link already; goldmark's
// defaults wait for the dot.
var (
	laxURL = regexp.MustCompile(`^(?:http|https|ftp)://[-a-zA-Z0-9@:%._\+~#=]{1,256}(?::\d+)?(?:[/#?][-a-zA-Z0-9@:%_\+.~#$!?&/=\(\);,'">\^{}\[\]` + "`" + `]*)?`)
	laxWWW = regexp.MustCompile(`^www\.[-a-zA-Z0-9@:%._\+~#=]{1,256}(?::\d+)?(?:[/#?][-a-zA-Z0-9@:%_\+.~#$!?&/=\(\);,'">\^{}\[\]` + "`" + `]*)?`)
)

var markdownParser = goldmark.New(goldmark.WithExtensions(
	extension.Table, extension.Strikethrough, extension.TaskList,
	extension.NewLinkify(extension.WithLinkifyURLRegexp(laxURL), extension.WithLinkifyWWWRegexp(laxWWW)),
)).Parser()

// Render draws the Markdown at width.
func (m *Markdown) Render(width int) []string {
	if m.valid && m.cachedText == m.text && m.cachedWidth == width {
		return m.cached
	}
	result := m.render(width)
	m.cached, m.cachedText, m.cachedWidth, m.valid = result, m.text, width, true
	return result
}

func (m *Markdown) render(width int) []string {
	contentWidth := max(1, width-m.paddingX*2)
	source := m.text
	if m.options.Transform != nil {
		source = m.options.Transform(source, contentWidth)
	}
	if strings.TrimFunc(source, jsSpace) == "" {
		return []string{}
	}
	source = strings.ReplaceAll(source, "\t", "   ")
	r := &mdRenderer{m: m, src: []byte(source)}
	doc := markdownParser.Parse(text.NewReader(r.src))

	blocks := r.blocks(doc, false, true)
	r.trimPartialClosingFence(blocks)
	var rendered []string
	for i, b := range blocks {
		rendered = append(rendered, r.renderBlock(b, contentWidth, nextKind(blocks, i), nil)...)
	}

	var wrapped []string
	for _, line := range rendered {
		wrapped = append(wrapped, WrapText(line, contentWidth)...)
	}
	margin := strings.Repeat(" ", m.paddingX)
	var bg Style
	if m.style != nil {
		bg = m.style.BgColor
	}
	var content []string
	for _, line := range wrapped {
		line = margin + line + margin
		if bg != nil {
			content = append(content, withBackground(line, width, bg))
		} else {
			content = append(content, PadToWidth(line, width))
		}
	}
	var padding []string
	for i := 0; i < m.paddingY; i++ {
		empty := strings.Repeat(" ", width)
		if bg != nil {
			empty = withBackground(empty, width, bg)
		}
		padding = append(padding, empty)
	}
	result := append(append(append([]string{}, padding...), content...), padding...)
	if len(result) == 0 {
		return []string{""}
	}
	return result
}

// mdBlock is one block as marked would have tokenised it: a node, or the
// "space" token marked emits for blank lines between blocks.
type mdBlock struct {
	kind string
	node ast.Node
}

func nextKind(blocks []mdBlock, i int) string {
	if i+1 < len(blocks) {
		return blocks[i+1].kind
	}
	return ""
}

type mdRenderer struct {
	m   *Markdown
	src []byte
}

// inlineStyle is the styling plain text gets in one context, and the codes
// that restore it after a nested style closes.
type inlineStyle struct {
	apply  func(string) string
	prefix string
}

func blockKind(n ast.Node) string {
	switch n.(type) {
	case *ast.Heading:
		return "heading"
	case *ast.Paragraph:
		return "paragraph"
	case *ast.TextBlock:
		return "text"
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		return "code"
	case *ast.List:
		return "list"
	case *east.Table:
		return "table"
	case *ast.Blockquote:
		return "blockquote"
	case *ast.ThematicBreak:
		return "hr"
	case *ast.HTMLBlock:
		return "html"
	case *ast.LinkReferenceDefinition:
		return "def"
	}
	return "other"
}

// blocks lists a container's child blocks with marked's space tokens where
// the source has blank lines between them. top adds the leading and
// trailing space tokens marked makes of blank lines around the document.
func (r *mdRenderer) blocks(container ast.Node, inQuote, top bool) []mdBlock {
	var out []mdBlock
	var prev ast.Node
	for c := container.FirstChild(); c != nil; c = c.NextSibling() {
		if c.Type() != ast.TypeBlock {
			continue
		}
		start := r.lineStart(r.firstPos(c))
		if prev == nil {
			if top && start > 0 && r.blankBetween(0, start, inQuote, true) {
				out = append(out, mdBlock{kind: "space"})
			}
		} else if r.blankBetween(r.lineEnd(r.lastPos(prev)), start, inQuote, false) {
			out = append(out, mdBlock{kind: "space"})
		}
		out = append(out, mdBlock{kind: blockKind(c), node: c})
		prev = c
	}
	if top && prev != nil && r.trailingSpace(r.blockEnd(prev)) {
		out = append(out, mdBlock{kind: "space"})
	}
	return out
}

// blankBetween reports a blank line in src[from:to] — from the newline
// ending one block's last line to the start of the next block's first. In
// a quote, a line holding only ">" is blank, since marked reads the quote's
// content with the markers taken away. leading counts any whitespace before
// the first block, which marked tokenises even when it is one newline.
func (r *mdRenderer) blankBetween(from, to int, inQuote, leading bool) bool {
	if from >= to {
		return false
	}
	gap := string(r.src[from:to])
	cut := " \t"
	if inQuote {
		cut = " \t>"
	}
	if leading {
		return strings.Trim(gap, cut+"\n") == ""
	}
	lines := strings.Split(gap, "\n")
	// lines[0] is the rest of the earlier block's last line; the last piece
	// is the start of the next block's line. Only whole lines between count.
	for _, l := range lines[1 : len(lines)-1] {
		if strings.Trim(l, cut) == "" {
			return true
		}
	}
	return false
}

// trailingSpace: marked turns two or more trailing newlines — after a
// closing fence, if there is one — into a space token.
func (r *mdRenderer) trailingSpace(from int) bool {
	rest := string(r.src[from:])
	if i := strings.LastIndexFunc(rest, func(c rune) bool { return !jsSpace(c) }); i >= 0 {
		end := strings.IndexByte(rest[i:], '\n')
		if end < 0 {
			return false
		}
		rest = rest[i+end:]
	}
	return len(rest) >= 2
}

// blockEnd is where marked's token for a top-level block ends: the end of
// its last line, except for a list, whose last item marked trims of
// trailing whitespace — which is then read as blank lines after it.
func (r *mdRenderer) blockEnd(n ast.Node) int {
	if _, ok := n.(*ast.List); ok {
		return min(r.lastPos(n)+1, len(r.src))
	}
	return r.lineEnd(r.lastPos(n))
}

func (r *mdRenderer) lineStart(pos int) int {
	pos = max(0, min(pos, len(r.src)))
	for pos > 0 && r.src[pos-1] != '\n' {
		pos--
	}
	return pos
}

func (r *mdRenderer) lineEnd(pos int) int {
	pos = max(0, min(pos, len(r.src)))
	for pos < len(r.src) && r.src[pos] != '\n' {
		pos++
	}
	return pos
}

// firstPos and lastPos are the first and last source offsets a node and
// everything in it cover.
func (r *mdRenderer) firstPos(n ast.Node) int {
	first := len(r.src)
	r.eachPos(n, func(start, stop int) { first = min(first, start) })
	return first
}

func (r *mdRenderer) lastPos(n ast.Node) int {
	last := 0
	r.eachPos(n, func(start, stop int) { last = max(last, stop-1, start) })
	return last
}

func (r *mdRenderer) eachPos(n ast.Node, visit func(start, stop int)) {
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if p := c.Pos(); p >= 0 {
			visit(p, p+1)
		}
		if c.Type() == ast.TypeBlock {
			lines := c.Lines()
			for i := 0; i < lines.Len(); i++ {
				s := lines.At(i)
				visit(s.Start, max(s.Stop, s.Start+1))
			}
		}
		if t, ok := c.(*ast.Text); ok {
			visit(t.Segment.Start, max(t.Segment.Stop, t.Segment.Start+1))
		}
		return ast.WalkContinue, nil
	})
}

// trimPartialClosingFence drops a closing fence still being typed from the
// last code block, so a streamed block does not grow a line and lose it
// again when the fence completes.
func (r *mdRenderer) trimPartialClosingFence(blocks []mdBlock) {
	if len(blocks) == 0 {
		return
	}
	last := blocks[len(blocks)-1].node
	for last != nil {
		switch n := last.(type) {
		case *ast.List:
			item := n.LastChild()
			if item == nil {
				return
			}
			last = item.LastChild()
			continue
		case *ast.Blockquote:
			last = n.LastChild()
			continue
		case *ast.FencedCodeBlock:
			r.trimFence(n)
		}
		return
	}
}

func (r *mdRenderer) trimFence(n *ast.FencedCodeBlock) {
	lines := n.Lines()
	if lines.Len() == 0 {
		return
	}
	fence := r.fenceMarker(n)
	lastSeg := lines.At(lines.Len() - 1)
	lastLine := strings.TrimRight(string(lastSeg.Value(r.src)), "\n")
	if fence == "" || lastLine == "" || len(lastLine) >= len(fence) || lastLine != strings.Repeat(fence[:1], len(lastLine)) {
		return
	}
	// Only a fence still arriving: a last line that ends in a newline is
	// followed by something, and so is not a partial fence at the end.
	if lastSeg.Stop > lastSeg.Start && r.src[lastSeg.Stop-1] == '\n' {
		return
	}
	lines.SetSliced(0, lines.Len()-1)
	n.SetLines(lines)
}

// fenceMarker is the run of backticks or tildes opening a fenced block.
func (r *mdRenderer) fenceMarker(n *ast.FencedCodeBlock) string {
	line := strings.TrimLeft(string(r.src[r.lineStart(n.Pos()):r.lineEnd(n.Pos())]), " >")
	for _, c := range []byte{'`', '~'} {
		i := 0
		for i < len(line) && line[i] == c {
			i++
		}
		if i >= 3 {
			return line[:i]
		}
	}
	return ""
}

func (r *mdRenderer) theme() *MarkdownTheme { return &r.m.theme }

func (r *mdRenderer) applyDefault(s string) string {
	st := r.m.style
	if st == nil {
		return s
	}
	s = st.Color.apply(s)
	if st.Bold {
		s = r.theme().Bold.apply(s)
	}
	if st.Italic {
		s = r.theme().Italic.apply(s)
	}
	if st.Strikethrough {
		s = r.theme().Strikethrough.apply(s)
	}
	if st.Underline {
		s = r.theme().Underline.apply(s)
	}
	return s
}

// stylePrefix is the codes fn opens with.
func stylePrefix(fn func(string) string) string {
	styled := fn("\x00")
	if i := strings.IndexByte(styled, 0); i >= 0 {
		return styled[:i]
	}
	return ""
}

func (r *mdRenderer) defaultStyle() *inlineStyle {
	prefix := ""
	if r.m.style != nil {
		prefix = stylePrefix(r.applyDefault)
	}
	return &inlineStyle{apply: r.applyDefault, prefix: prefix}
}

func (r *mdRenderer) renderBlock(b mdBlock, width int, next string, ctx *inlineStyle) []string {
	t := r.theme()
	var lines []string
	spaceAfter := func() {
		if next != "" && next != "space" {
			lines = append(lines, "")
		}
	}
	switch n := b.node.(type) {
	case nil:
		if b.kind == "space" {
			lines = append(lines, "")
		}
	case *ast.Heading:
		style := func(s string) string { return t.Heading.apply(t.Bold.apply(s)) }
		if n.Level == 1 {
			style = func(s string) string { return t.Heading.apply(t.Bold.apply(t.Underline.apply(s))) }
		}
		heading := r.renderInline(n, &inlineStyle{apply: style, prefix: stylePrefix(style)})
		if n.Level >= 3 {
			heading = style(strings.Repeat("#", n.Level)+" ") + heading
		}
		lines = append(lines, heading)
		spaceAfter()
	case *ast.Paragraph:
		lines = append(lines, r.renderInlineKeepingTrail(n, ctx))
		if next != "" && next != "list" && next != "space" {
			lines = append(lines, "")
		}
	case *ast.TextBlock:
		lines = append(lines, r.renderInlineKeepingTrail(n, ctx))
	case *ast.FencedCodeBlock:
		lang := ""
		if n.Info != nil {
			lang, _, _ = strings.Cut(strings.TrimSpace(string(n.Info.Value(r.src))), " ")
		}
		lines = append(lines, r.codeBlock(r.codeText(n, true), lang)...)
		spaceAfter()
	case *ast.CodeBlock:
		lines = append(lines, r.codeBlock(r.codeText(n, false), "")...)
		spaceAfter()
	case *ast.List:
		lines = append(lines, r.renderList(n, 0, width, ctx)...)
	case *east.Table:
		lines = append(lines, r.renderTable(n, width, next, ctx)...)
	case *ast.Blockquote:
		lines = append(lines, r.renderQuote(n, width)...)
		spaceAfter()
	case *ast.ThematicBreak:
		lines = append(lines, t.HR.apply(strings.Repeat("─", min(width, 80))))
		spaceAfter()
	case *ast.LinkReferenceDefinition:
		// A definition draws nothing; the links that use it show its address.
	case *ast.HTMLBlock:
		raw := r.linesText(n.Lines())
		if n.HasClosure() {
			raw += string(n.ClosureLine.Value(r.src))
		}
		lines = append(lines, r.applyDefault(strings.TrimSpace(raw)))
	default:
		lines = append(lines, r.plainText(b.node))
	}
	return lines
}

func (r *mdRenderer) linesText(lines *text.Segments) string {
	var b strings.Builder
	for i := 0; i < lines.Len(); i++ {
		s := lines.At(i)
		b.Write(s.Value(r.src))
	}
	return b.String()
}

// codeText is a code block's content without the newline before its end.
// An indented block also loses its trailing blank lines, as marked trims
// them.
func (r *mdRenderer) codeText(n ast.Node, fenced bool) string {
	code := r.linesText(n.Lines())
	if fenced {
		return strings.TrimSuffix(code, "\n")
	}
	return strings.TrimRight(code, "\n")
}

func (r *mdRenderer) codeBlock(code, lang string) []string {
	t := r.theme()
	indent := "  "
	if t.CodeBlockIndent != nil {
		indent = *t.CodeBlockIndent
	}
	lines := []string{t.CodeBlockBorder.apply("```" + lang)}
	if t.HighlightCode != nil {
		for _, l := range t.HighlightCode(code, lang) {
			lines = append(lines, indent+l)
		}
	} else {
		for _, l := range strings.Split(code, "\n") {
			lines = append(lines, indent+t.CodeBlock.apply(l))
		}
	}
	return append(lines, t.CodeBlockBorder.apply("```"))
}

func (r *mdRenderer) renderQuote(n *ast.Blockquote, width int) []string {
	t := r.theme()
	style := func(s string) string { return t.Quote.apply(t.Italic.apply(s)) }
	prefix := stylePrefix(style)
	applyQuote := func(line string) string {
		if prefix == "" {
			return style(line)
		}
		return style(strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+prefix))
	}
	contentWidth := max(1, width-2)
	// The message's own text style does not reach into a quote.
	ctx := &inlineStyle{apply: func(s string) string { return s }, prefix: prefix}
	children := r.blocks(n, true, false)
	var inner []string
	for i, c := range children {
		inner = append(inner, r.renderBlock(c, contentWidth, nextKind(children, i), ctx)...)
	}
	for len(inner) > 0 && inner[len(inner)-1] == "" {
		inner = inner[:len(inner)-1]
	}
	var lines []string
	for _, l := range inner {
		for _, w := range WrapText(applyQuote(l), contentWidth) {
			lines = append(lines, t.QuoteBorder.apply("│ ")+w)
		}
	}
	return lines
}

var (
	orderedMarker   = regexp.MustCompile(`^(\d{1,9}[.)])[ \t]+`)
	unorderedMarker = regexp.MustCompile(`^([-+*])(?:[ \t]+|\r?\n|$)`)
)

func (r *mdRenderer) renderList(n *ast.List, depth, width int, ctx *inlineStyle) []string {
	t := r.theme()
	var lines []string
	indent := strings.Repeat("    ", depth)
	i := 0
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		last := item.NextSibling() == nil
		at := string(r.src[max(0, item.Pos()):r.lineEnd(max(0, item.Pos()))])
		var bullet string
		if n.IsOrdered() {
			bullet = strconv.Itoa(n.Start+i) + ". "
			if r.m.options.PreserveOrderedListMarkers {
				if m := orderedMarker.FindStringSubmatch(at); m != nil {
					bullet = m[1] + " "
				}
			}
		} else {
			bullet = "- "
			if r.m.options.PreserveOrderedListMarkers {
				if m := unorderedMarker.FindStringSubmatch(at); m != nil {
					bullet = m[1] + " "
				}
			}
		}
		marker := bullet
		if box := taskBox(item); box != nil {
			if box.IsChecked {
				marker += "[x] "
			} else {
				marker += "[ ] "
			}
		}
		first := indent + t.ListBullet.apply(marker)
		continuation := indent + strings.Repeat(" ", VisibleWidth(marker))
		itemWidth := max(1, width-VisibleWidth(first))
		rendered := false
		for _, c := range r.blocks(item, false, false) {
			if sub, ok := c.node.(*ast.List); ok {
				lines = append(lines, r.renderList(sub, depth+1, width, ctx)...)
				rendered = true
				continue
			}
			for _, l := range r.renderBlock(c, itemWidth, "", ctx) {
				for _, w := range WrapText(l, itemWidth) {
					prefix := first
					if rendered {
						prefix = continuation
					}
					lines = append(lines, prefix+w)
					rendered = true
				}
			}
		}
		if !rendered {
			lines = append(lines, first)
		}
		if !n.IsTight && !last {
			lines = append(lines, "")
		}
		i++
	}
	return lines
}

// taskBox is a list item's task checkbox, if it has one.
func taskBox(item ast.Node) *east.TaskCheckBox {
	block := item.FirstChild()
	if block == nil {
		return nil
	}
	box, _ := block.FirstChild().(*east.TaskCheckBox)
	return box
}

// trailingBlanks is the spaces and tabs from pos to the end of its line.
func (r *mdRenderer) trailingBlanks(pos int) string {
	end := pos
	for end < len(r.src) && (r.src[end] == ' ' || r.src[end] == '\t') {
		end++
	}
	if end < len(r.src) && r.src[end] != '\n' {
		return ""
	}
	return string(r.src[pos:end])
}

// renderInlineKeepingTrail draws a paragraph with the spaces that end its
// last line, which marked keeps as text — except in a list's last item,
// whose text marked trims.
func (r *mdRenderer) renderInlineKeepingTrail(n ast.Node, ctx *inlineStyle) string {
	if ctx == nil {
		ctx = r.defaultStyle()
	}
	w := &inlineWriter{r: r, ctx: ctx}
	w.children(n)
	if item, ok := n.Parent().(*ast.ListItem); !ok || item.NextSibling() != nil {
		w.text(r.trailingBlanks(r.lastPos(n) + 1))
	}
	w.flush()
	result := w.out.String()
	for ctx.prefix != "" && strings.HasSuffix(result, ctx.prefix) {
		result = strings.TrimSuffix(result, ctx.prefix)
	}
	return result
}

// renderInline draws a block's inline content. Adjacent plain text is
// styled as one run, as marked merges it into one token; a backslash escape
// is a token of its own.
func (r *mdRenderer) renderInline(parent ast.Node, ctx *inlineStyle) string {
	if ctx == nil {
		ctx = r.defaultStyle()
	}
	w := &inlineWriter{r: r, ctx: ctx}
	w.children(parent)
	w.flush()
	result := w.out.String()
	for ctx.prefix != "" && strings.HasSuffix(result, ctx.prefix) {
		result = strings.TrimSuffix(result, ctx.prefix)
	}
	return result
}

type inlineWriter struct {
	r       *mdRenderer
	ctx     *inlineStyle
	out     strings.Builder
	pending strings.Builder
	// afterBox drops the space between a task checkbox and the item's text.
	afterBox bool
	// inLink draws an address inside a link's text as text, not a link.
	inLink bool
	// inTable reads "\|" as a plain pipe: marked splits a row into cells,
	// unescaping pipes, before it reads the cells.
	inTable bool
}

func (w *inlineWriter) text(s string) {
	if w.afterBox {
		s = strings.TrimLeft(s, " \t")
		w.afterBox = s == ""
	}
	w.pending.WriteString(s)
}

// flush styles the pending run of plain text, line by line.
func (w *inlineWriter) flush() {
	if w.pending.Len() == 0 {
		return
	}
	parts := strings.Split(w.pending.String(), "\n")
	for i, p := range parts {
		parts[i] = w.ctx.apply(p)
	}
	w.out.WriteString(strings.Join(parts, "\n"))
	w.pending.Reset()
}

func (w *inlineWriter) styled(s string) {
	w.flush()
	w.out.WriteString(s)
}

func (w *inlineWriter) nested(n ast.Node) string {
	inner := &inlineWriter{r: w.r, ctx: w.ctx, inLink: w.inLink, inTable: w.inTable}
	inner.children(n)
	inner.flush()
	result := inner.out.String()
	for w.ctx.prefix != "" && strings.HasSuffix(result, w.ctx.prefix) {
		result = strings.TrimSuffix(result, w.ctx.prefix)
	}
	return result
}

func (w *inlineWriter) children(n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		w.node(c)
	}
}

func (w *inlineWriter) node(n ast.Node) {
	r, t := w.r, w.r.theme()
	switch n := n.(type) {
	case *east.TaskCheckBox:
		w.afterBox = true
	case *ast.Text:
		w.rawText(string(n.Segment.Value(r.src)))
		if n.SoftLineBreak() {
			// marked keeps a line's trailing spaces; goldmark drops them.
			w.text(r.trailingBlanks(n.Segment.Stop))
		}
		if n.SoftLineBreak() || n.HardLineBreak() {
			w.text("\n")
		}
	case *ast.String:
		w.text(string(n.Value))
	case *ast.Emphasis:
		inner := w.nested(n)
		if n.Level >= 2 {
			w.styled(t.Bold.apply(inner) + w.ctx.prefix)
		} else {
			w.styled(t.Italic.apply(inner) + w.ctx.prefix)
		}
	case *east.Strikethrough:
		if !r.strictStrikethrough(n) {
			// marked strikes only ~~text~~ with no space inside the tildes.
			w.text("~")
			w.children(n)
			w.text("~")
			return
		}
		w.styled(t.Strikethrough.apply(w.nested(n)) + w.ctx.prefix)
	case *ast.CodeSpan:
		var code strings.Builder
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			if tx, ok := c.(*ast.Text); ok {
				code.Write(tx.Segment.Value(r.src))
			} else if s, ok := c.(*ast.String); ok {
				code.Write(s.Value)
			}
		}
		w.styled(t.Code.apply(strings.ReplaceAll(code.String(), "\n", " ")) + w.ctx.prefix)
	case *ast.Link:
		w.inLink = true
		content := w.nested(n)
		w.inLink = false
		w.link(content, r.plainText(n), string(n.Destination))
	case *ast.AutoLink:
		label := string(n.Label(r.src))
		if w.inLink {
			w.text(label)
			return
		}
		href := string(n.URL(r.src))
		if n.AutoLinkType == ast.AutoLinkEmail && !strings.HasPrefix(strings.ToLower(href), "mailto:") {
			href = "mailto:" + href
		}
		w.link(w.ctx.apply(label), label, href)
	case *ast.Image:
		w.text(r.plainText(n))
	case *ast.RawHTML:
		var raw strings.Builder
		for i := 0; i < n.Segments.Len(); i++ {
			s := n.Segments.At(i)
			raw.Write(s.Value(r.src))
		}
		// An HTML tag is a token of its own in marked, styled apart from the
		// text around it.
		w.flush()
		w.text(raw.String())
		w.flush()
	default:
		w.children(n)
	}
}

// rawText adds a text segment, turning each backslash escape into a token
// of its own.
func (w *inlineWriter) rawText(s string) {
	for {
		i := strings.IndexByte(s, '\\')
		if i < 0 || i+1 >= len(s) || !isASCIIPunct(s[i+1]) {
			w.text(s)
			return
		}
		if w.inTable && s[i+1] == '|' {
			w.text(s[:i] + "|")
			s = s[i+2:]
			continue
		}
		w.text(s[:i])
		w.flush()
		escaped := s[i+1 : i+2]
		if w.r.m.options.PreserveBackslashEscapes {
			escaped = s[i : i+2]
		}
		w.text(escaped)
		w.flush()
		s = s[i+2:]
	}
}

func isASCIIPunct(c byte) bool {
	return strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", c) >= 0
}

// link draws a link: an OSC 8 hyperlink where the terminal has them, and
// otherwise the text with the address after it, unless the text is the
// address.
func (w *inlineWriter) link(content, plain, href string) {
	t := w.r.theme()
	styled := t.Link.apply(t.Underline.apply(content))
	if Capabilities().Hyperlinks {
		w.styled(Hyperlink(styled, href) + w.ctx.prefix)
		return
	}
	bare := href
	if strings.HasPrefix(href, "mailto:") {
		bare = href[len("mailto:"):]
	}
	if plain == href || plain == bare {
		w.styled(styled + w.ctx.prefix)
		return
	}
	w.styled(styled + t.LinkURL.apply(" ("+href+")") + w.ctx.prefix)
}

// strictStrikethrough reports a strikethrough written with exactly two
// tildes either side.
func (r *mdRenderer) strictStrikethrough(n ast.Node) bool {
	first, last := len(r.src), 0
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		r.eachPos(c, func(start, stop int) {
			first, last = min(first, start), max(last, stop)
		})
	}
	if first < 2 || last+2 > len(r.src) {
		return false
	}
	open := first >= 3 && r.src[first-3] == '~'
	close := last+2 < len(r.src) && r.src[last+2] == '~'
	return string(r.src[first-2:first]) == "~~" && string(r.src[last:last+2]) == "~~" && !open && !close
}

// plainText is the text of a node's inline content without styling.
func (r *mdRenderer) plainText(n ast.Node) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch c := c.(type) {
		case *ast.Text:
			b.Write(c.Segment.Value(r.src))
			if c.SoftLineBreak() || c.HardLineBreak() {
				b.WriteByte('\n')
			}
		case *ast.String:
			b.Write(c.Value)
		case *ast.AutoLink:
			b.Write(c.Label(r.src))
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

// --- tables ---

func (r *mdRenderer) cellText(cell ast.Node, ctx *inlineStyle) string {
	if ctx == nil {
		ctx = r.defaultStyle()
	}
	w := &inlineWriter{r: r, ctx: ctx, inTable: true}
	w.children(cell)
	w.flush()
	result := w.out.String()
	for ctx.prefix != "" && strings.HasSuffix(result, ctx.prefix) {
		result = strings.TrimSuffix(result, ctx.prefix)
	}
	return result
}

func longestWordWidth(s string, limit int) int {
	longest := 0
	for _, word := range strings.FieldsFunc(s, jsSpace) {
		longest = max(longest, VisibleWidth(word))
	}
	return min(longest, limit)
}

func (r *mdRenderer) renderTable(n *east.Table, width int, next string, ctx *inlineStyle) []string {
	var header []string
	var rows [][]string
	for row := n.FirstChild(); row != nil; row = row.NextSibling() {
		var cells []string
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			cells = append(cells, r.cellText(cell, ctx))
		}
		if _, ok := row.(*east.TableHeader); ok {
			header = cells
		} else {
			rows = append(rows, cells)
		}
	}
	cols := len(header)
	if cols == 0 {
		return nil
	}
	var lines []string
	overhead := 3*cols + 1
	available := width - overhead
	if available < cols {
		// Too narrow for a table that holds together: the source instead.
		raw := string(r.src[r.lineStart(r.firstPos(n)):r.lineEnd(r.lastPos(n))])
		lines = append(lines, WrapText(raw, width)...)
		if next != "" && next != "space" {
			lines = append(lines, "")
		}
		return lines
	}

	const maxUnbrokenWord = 30
	natural := make([]int, cols)
	minWord := make([]int, cols)
	for i, h := range header {
		natural[i] = VisibleWidth(h)
		minWord[i] = max(1, longestWordWidth(h, maxUnbrokenWord))
	}
	for _, row := range rows {
		for i, c := range row {
			if i >= cols {
				break
			}
			natural[i] = max(natural[i], VisibleWidth(c))
			minWord[i] = max(minWord[i], longestWordWidth(c, maxUnbrokenWord))
		}
	}
	sum := func(xs []int) int {
		total := 0
		for _, x := range xs {
			total += x
		}
		return total
	}
	minCols := append([]int(nil), minWord...)
	if sum(minCols) > available {
		minCols = make([]int, cols)
		for i := range minCols {
			minCols[i] = 1
		}
		if remaining := available - cols; remaining > 0 {
			weight := 0
			for _, w := range minWord {
				weight += max(0, w-1)
			}
			allocated := 0
			for i, w := range minWord {
				grow := 0
				if weight > 0 {
					grow = max(0, w-1) * remaining / weight
				}
				minCols[i] += grow
				allocated += grow
			}
			for i := 0; allocated < remaining && i < cols; i++ {
				minCols[i]++
				allocated++
			}
		}
	}
	minTotal := sum(minCols)

	var widths []int
	if sum(natural)+overhead <= width {
		widths = make([]int, cols)
		for i := range widths {
			widths[i] = max(natural[i], minCols[i])
		}
	} else {
		potential := 0
		for i := range natural {
			potential += max(0, natural[i]-minCols[i])
		}
		extra := max(0, available-minTotal)
		widths = make([]int, cols)
		for i := range widths {
			grow := 0
			if potential > 0 {
				grow = max(0, natural[i]-minCols[i]) * extra / potential
			}
			widths[i] = minCols[i] + grow
		}
		remaining := available - sum(widths)
		for remaining > 0 {
			grew := false
			for i := 0; i < cols && remaining > 0; i++ {
				if widths[i] < natural[i] {
					widths[i]++
					remaining--
					grew = true
				}
			}
			if !grew {
				break
			}
		}
	}

	border := func(left, mid, right string) string {
		parts := make([]string, cols)
		for i, w := range widths {
			parts[i] = strings.Repeat("─", w)
		}
		return left + "─" + strings.Join(parts, "─"+mid+"─") + "─" + right
	}
	cellLines := func(cells []string) ([][]string, int) {
		out := make([][]string, cols)
		height := 0
		for i := 0; i < cols; i++ {
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			out[i] = WrapText(c, max(1, widths[i]))
			height = max(height, len(out[i]))
		}
		return out, height
	}
	row := func(cells [][]string, line int, style Style) string {
		parts := make([]string, cols)
		for i, c := range cells {
			s := ""
			if line < len(c) {
				s = c[line]
			}
			parts[i] = style.apply(s + strings.Repeat(" ", max(0, widths[i]-VisibleWidth(s))))
		}
		return "│ " + strings.Join(parts, " │ ") + " │"
	}

	lines = append(lines, border("┌", "┬", "┐"))
	hc, hh := cellLines(header)
	for l := 0; l < hh; l++ {
		lines = append(lines, row(hc, l, r.theme().Bold))
	}
	separator := border("├", "┼", "┤")
	lines = append(lines, separator)
	for ri, cells := range rows {
		rc, rh := cellLines(cells)
		for l := 0; l < rh; l++ {
			lines = append(lines, row(rc, l, nil))
		}
		if ri < len(rows)-1 {
			lines = append(lines, separator)
		}
	}
	lines = append(lines, border("└", "┴", "┘"))
	if next != "" && next != "space" {
		lines = append(lines, "")
	}
	return lines
}
