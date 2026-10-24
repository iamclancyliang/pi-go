package tui

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// AutocompleteItem is one completion: Value replaces the prefix, Label is
// what the list shows, Description what it shows beside it.
type AutocompleteItem struct {
	Value       string
	Label       string
	Description string
}

// AutocompleteSuggestions are completions for the text before the cursor,
// and the Prefix of that text they would replace.
type AutocompleteSuggestions struct {
	Items  []AutocompleteItem
	Prefix string
}

// AutocompleteProvider supplies completions to the editor. Columns are byte
// offsets into the cursor's line.
//
// Suggestions runs off the interface's loop when the editor is in one, so it
// may block — on a directory walk, say — and must stop when ctx is done. It
// returns nil for nothing to suggest.
type AutocompleteProvider interface {
	Suggestions(ctx context.Context, lines []string, cursorLine, cursorCol int, force bool) *AutocompleteSuggestions
	ApplyCompletion(lines []string, cursorLine, cursorCol int, item AutocompleteItem, prefix string) (newLines []string, newLine, newCol int)
}

// TriggerCharacterProvider is a provider with characters of its own, beyond
// @ and #, that open completion when typed at the start of a word.
type TriggerCharacterProvider interface {
	TriggerCharacters() []string
}

// FileCompletionGate is a provider that decides whether tab should complete
// a path where the cursor is.
type FileCompletionGate interface {
	ShouldTriggerFileCompletion(lines []string, cursorLine, cursorCol int) bool
}

// SlashCommand is a command the editor completes after "/".
type SlashCommand struct {
	Name         string
	Description  string
	ArgumentHint string
	// ArgumentCompletions, if set, completes the command's argument from the
	// text typed after the command. It runs off the interface's loop.
	ArgumentCompletions func(ctx context.Context, prefix string) []AutocompleteItem
}

// PathMatch is a file or directory a PathFinder found, relative to the
// directory searched and slash-separated.
type PathMatch struct {
	Path  string
	IsDir bool
}

// PathFinder searches baseDir recursively for files and directories matching
// query, returning at most limit — what Pi asks of fd for @-mentions: hidden
// entries included, .git excluded, symlinks followed, the query a
// case-smart pattern on the name, or on the full path when it holds a slash.
type PathFinder func(ctx context.Context, baseDir, query string, limit int) []PathMatch

// CombinedAutocompleteProvider completes slash commands and their arguments,
// paths, and @-mentions of files anywhere below the working directory — Pi's
// CombinedAutocompleteProvider. With no PathFinder, @-mentions complete
// nothing, as in Pi when fd is unavailable.
type CombinedAutocompleteProvider struct {
	commands []SlashCommand
	basePath string
	find     PathFinder
}

// NewCombinedAutocompleteProvider completes the given commands, and paths
// relative to basePath.
func NewCombinedAutocompleteProvider(commands []SlashCommand, basePath string, find PathFinder) *CombinedAutocompleteProvider {
	return &CombinedAutocompleteProvider{commands: commands, basePath: basePath, find: find}
}

const pathDelimiters = " \t\"'="

func findLastDelimiter(text string) int { return strings.LastIndexAny(text, pathDelimiters) }

func isTokenStart(text string, index int) bool {
	return index == 0 || strings.IndexByte(pathDelimiters, text[index-1]) >= 0
}

func findUnclosedQuoteStart(text string) int {
	in, start := false, -1
	for i := 0; i < len(text); i++ {
		if text[i] == '"' {
			in = !in
			if in {
				start = i
			}
		}
	}
	if in {
		return start
	}
	return -1
}

// extractQuotedPrefix is the open quoted token the cursor is in, @ and all.
func extractQuotedPrefix(text string) (string, bool) {
	start := findUnclosedQuoteStart(text)
	if start < 0 {
		return "", false
	}
	if start > 0 && text[start-1] == '@' {
		if !isTokenStart(text, start-1) {
			return "", false
		}
		return text[start-1:], true
	}
	if !isTokenStart(text, start) {
		return "", false
	}
	return text[start:], true
}

func parsePathPrefix(prefix string) (raw string, isAt, isQuoted bool) {
	switch {
	case strings.HasPrefix(prefix, `@"`):
		return prefix[2:], true, true
	case strings.HasPrefix(prefix, `"`):
		return prefix[1:], false, true
	case strings.HasPrefix(prefix, "@"):
		return prefix[1:], true, false
	}
	return prefix, false, false
}

// buildCompletionValue quotes a path that has a space in it, or was being
// typed inside quotes.
func buildCompletionValue(p string, isAt, isQuoted bool) string {
	at := ""
	if isAt {
		at = "@"
	}
	if !isQuoted && !strings.Contains(p, " ") {
		return at + p
	}
	return at + `"` + p + `"`
}

func toDisplayPath(p string) string { return strings.ReplaceAll(p, `\`, "/") }

// Suggestions completes an @-mention, a slash command or its argument, or a
// path, in that order of precedence.
func (p *CombinedAutocompleteProvider) Suggestions(ctx context.Context, lines []string, cursorLine, cursorCol int, force bool) *AutocompleteSuggestions {
	before := lines[cursorLine][:cursorCol]

	if at, ok := p.extractAtPrefix(before); ok {
		raw, _, quoted := parsePathPrefix(at)
		items := p.fuzzyFileSuggestions(ctx, raw, quoted)
		if len(items) == 0 {
			return nil
		}
		return &AutocompleteSuggestions{Items: items, Prefix: at}
	}

	if !force && strings.HasPrefix(before, "/") {
		space := strings.IndexByte(before, ' ')
		if space < 0 {
			type entry struct{ name, description string }
			var entries []entry
			for _, c := range p.commands {
				description := c.Description
				if c.ArgumentHint != "" {
					if description != "" {
						description = c.ArgumentHint + " — " + description
					} else {
						description = c.ArgumentHint
					}
				}
				entries = append(entries, entry{c.Name, description})
			}
			var items []AutocompleteItem
			for _, e := range FuzzyFilter(entries, before[1:], func(e entry) string { return e.name }) {
				items = append(items, AutocompleteItem{Value: e.name, Label: e.name, Description: e.description})
			}
			if len(items) == 0 {
				return nil
			}
			return &AutocompleteSuggestions{Items: items, Prefix: before}
		}
		name, argument := before[1:space], before[space+1:]
		for _, c := range p.commands {
			if c.Name != name {
				continue
			}
			if c.ArgumentCompletions == nil {
				return nil
			}
			items := c.ArgumentCompletions(ctx, argument)
			if len(items) == 0 {
				return nil
			}
			return &AutocompleteSuggestions{Items: items, Prefix: argument}
		}
		return nil
	}

	prefix, ok := p.extractPathPrefix(before, force)
	if !ok {
		return nil
	}
	items := p.fileSuggestions(prefix)
	if len(items) == 0 {
		return nil
	}
	return &AutocompleteSuggestions{Items: items, Prefix: prefix}
}

// ApplyCompletion replaces the prefix with the item. A command name is
// followed by a space; so is an @-mentioned file, but not a directory, so
// completion can go on into it.
func (p *CombinedAutocompleteProvider) ApplyCompletion(lines []string, cursorLine, cursorCol int, item AutocompleteItem, prefix string) ([]string, int, int) {
	line := lines[cursorLine]
	beforePrefix := line[:cursorCol-len(prefix)]
	after := line[cursorCol:]
	quoted := strings.HasPrefix(prefix, `"`) || strings.HasPrefix(prefix, `@"`)
	if quoted && strings.HasSuffix(item.Value, `"`) && strings.HasPrefix(after, `"`) {
		// The closing quote is already there.
		after = after[1:]
	}
	out := append([]string(nil), lines...)
	isDir := strings.HasSuffix(item.Label, "/")
	offset := len(item.Value)
	if isDir && strings.HasSuffix(item.Value, `"`) {
		// Inside the quotes, so the path can be continued.
		offset--
	}

	if strings.HasPrefix(prefix, "/") && strings.TrimFunc(beforePrefix, jsSpace) == "" && !strings.Contains(prefix[1:], "/") {
		out[cursorLine] = beforePrefix + "/" + item.Value + " " + after
		return out, cursorLine, len(beforePrefix) + len(item.Value) + 2
	}
	if strings.HasPrefix(prefix, "@") {
		suffix := " "
		if isDir {
			suffix = ""
		}
		out[cursorLine] = beforePrefix + item.Value + suffix + after
		return out, cursorLine, len(beforePrefix) + offset + len(suffix)
	}
	out[cursorLine] = beforePrefix + item.Value + after
	return out, cursorLine, len(beforePrefix) + offset
}

// ShouldTriggerFileCompletion: tab completes a path anywhere but in a slash
// command's name.
func (p *CombinedAutocompleteProvider) ShouldTriggerFileCompletion(lines []string, cursorLine, cursorCol int) bool {
	before := strings.TrimFunc(lines[cursorLine][:cursorCol], jsSpace)
	return !(strings.HasPrefix(before, "/") && !strings.Contains(before, " "))
}

func (p *CombinedAutocompleteProvider) extractAtPrefix(text string) (string, bool) {
	if quoted, ok := extractQuotedPrefix(text); ok && strings.HasPrefix(quoted, `@"`) {
		return quoted, true
	}
	start := findLastDelimiter(text) + 1
	if start < len(text) && text[start] == '@' {
		return text[start:], true
	}
	return "", false
}

// extractPathPrefix is the path being typed before the cursor: anything when
// tab forced it, otherwise only what looks like a path.
func (p *CombinedAutocompleteProvider) extractPathPrefix(text string, force bool) (string, bool) {
	if quoted, ok := extractQuotedPrefix(text); ok {
		return quoted, true
	}
	prefix := text[findLastDelimiter(text)+1:]
	if force {
		return prefix, true
	}
	if strings.Contains(prefix, "/") || strings.HasPrefix(prefix, ".") || strings.HasPrefix(prefix, "~/") {
		return prefix, true
	}
	if prefix == "" && strings.HasSuffix(text, " ") {
		return prefix, true
	}
	return "", false
}

func expandHomePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if strings.HasPrefix(p, "~/") {
		expanded := filepath.Join(home, p[2:])
		if strings.HasSuffix(p, "/") && !strings.HasSuffix(expanded, "/") {
			expanded += "/"
		}
		return expanded
	}
	if p == "~" {
		return home
	}
	return p
}

// fileSuggestions lists the directory the path prefix is in, keeping the
// entries that start with what follows the last slash, directories first.
func (p *CombinedAutocompleteProvider) fileSuggestions(prefix string) []AutocompleteItem {
	raw, isAt, isQuoted := parsePathPrefix(prefix)
	expanded := raw
	if strings.HasPrefix(expanded, "~") {
		expanded = expandHomePath(expanded)
	}
	absolute := strings.HasPrefix(raw, "~") || strings.HasPrefix(expanded, "/")
	isRoot := raw == "" || raw == "./" || raw == "../" || raw == "~" || raw == "~/" || raw == "/"

	var dir, filePrefix string
	if isRoot || strings.HasSuffix(raw, "/") {
		dir = expanded
	} else {
		dir, filePrefix = path.Dir(expanded), path.Base(expanded)
	}
	if !absolute {
		dir = filepath.Join(p.basePath, dir)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var items []AutocompleteItem
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(filePrefix)) {
			continue
		}
		isDir := entry.IsDir()
		if !isDir && entry.Type()&fs.ModeSymlink != 0 {
			if info, err := os.Stat(filepath.Join(dir, name)); err == nil {
				isDir = info.IsDir()
			}
		}

		var rel string
		switch {
		case strings.HasSuffix(raw, "/"):
			rel = raw + name
		case strings.ContainsAny(raw, `/\`):
			switch {
			case strings.HasPrefix(raw, "~/"):
				if d := path.Dir(raw[2:]); d == "." {
					rel = "~/" + name
				} else {
					rel = "~/" + path.Join(d, name)
				}
			case strings.HasPrefix(raw, "/"):
				if d := path.Dir(raw); d == "/" {
					rel = "/" + name
				} else {
					rel = d + "/" + name
				}
			default:
				rel = path.Join(path.Dir(raw), name)
				if strings.HasPrefix(raw, "./") && !strings.HasPrefix(rel, "./") {
					rel = "./" + rel
				}
			}
		case strings.HasPrefix(raw, "~"):
			rel = "~/" + name
		default:
			rel = name
		}
		rel = toDisplayPath(rel)
		label := name
		if isDir {
			rel += "/"
			label += "/"
		}
		items = append(items, AutocompleteItem{Value: buildCompletionValue(rel, isAt, isQuoted), Label: label})
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := strings.HasSuffix(items[i].Value, "/"), strings.HasSuffix(items[j].Value, "/")
		if a != b {
			return a
		}
		return localeLess(items[i].Label, items[j].Label)
	})
	return items
}

// localeLess orders names roughly as JavaScript's localeCompare does by
// default: by letter regardless of case, and lower case first between two
// that differ only in case.
func localeLess(a, b string) bool {
	if la, lb := strings.ToLower(a), strings.ToLower(b); la != lb {
		return la < lb
	}
	return a > b
}

// scoreEntry ranks a found path against the query: exact name, name prefix,
// name substring, path substring; directories a little higher.
func scoreEntry(filePath, query string, isDir bool) int {
	name := strings.ToLower(path.Base(filePath))
	q := strings.ToLower(query)
	score := 0
	switch {
	case name == q:
		score = 100
	case strings.HasPrefix(name, q):
		score = 80
	case strings.Contains(name, q):
		score = 50
	case strings.Contains(strings.ToLower(filePath), q):
		score = 30
	}
	if isDir && score > 0 {
		score += 10
	}
	return score
}

// fuzzyFileSuggestions searches for an @-mention: below the working
// directory, or below the directory the query names up to its last slash
// when that exists.
func (p *CombinedAutocompleteProvider) fuzzyFileSuggestions(ctx context.Context, query string, quoted bool) []AutocompleteItem {
	if p.find == nil || ctx.Err() != nil {
		return nil
	}
	base, q, displayBase, scoped := p.resolveScopedQuery(query)
	if !scoped {
		base, q = p.basePath, query
	}
	found := p.find(ctx, base, q, 100)
	if ctx.Err() != nil {
		return nil
	}

	type scored struct {
		match PathMatch
		score int
	}
	var entries []scored
	for _, m := range found {
		score := 1
		if q != "" {
			withSlash := m.Path
			if m.IsDir {
				withSlash += "/"
			}
			score = scoreEntry(withSlash, q, m.IsDir)
		}
		if score > 0 {
			entries = append(entries, scored{m, score})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].score > entries[j].score })
	if len(entries) > 20 {
		entries = entries[:20]
	}

	var items []AutocompleteItem
	for _, e := range entries {
		display := e.match.Path
		if scoped {
			if displayBase == "/" {
				display = "/" + toDisplayPath(e.match.Path)
			} else {
				display = toDisplayPath(displayBase) + toDisplayPath(e.match.Path)
			}
		}
		label := path.Base(e.match.Path)
		completion := display
		if e.match.IsDir {
			label += "/"
			completion += "/"
		}
		items = append(items, AutocompleteItem{
			Value:       buildCompletionValue(completion, true, quoted),
			Label:       label,
			Description: display,
		})
	}
	return items
}

func (p *CombinedAutocompleteProvider) resolveScopedQuery(raw string) (baseDir, query, displayBase string, ok bool) {
	normalized := toDisplayPath(raw)
	slash := strings.LastIndexByte(normalized, '/')
	if slash < 0 {
		return "", "", "", false
	}
	displayBase, query = normalized[:slash+1], normalized[slash+1:]
	switch {
	case strings.HasPrefix(displayBase, "~/"):
		baseDir = expandHomePath(displayBase)
	case strings.HasPrefix(displayBase, "/"):
		baseDir = displayBase
	default:
		baseDir = filepath.Join(p.basePath, displayBase)
	}
	if info, err := os.Stat(baseDir); err != nil || !info.IsDir() {
		return "", "", "", false
	}
	return baseDir, query, displayBase, true
}
