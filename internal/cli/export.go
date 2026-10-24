package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/export"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// exportSource is everything an export describes besides the conversation.
type exportSource struct {
	conversation *Conversation
	session      *session.Session
	provider     string
	model        string
	thinking     ai.ThinkingLevel
	tools        *tools.Registry
	workingDir   string
}

func (s exportSource) document() export.Document {
	snapshot := s.session.Snapshot()
	doc := export.Document{
		SessionID: s.conversation.ID, Name: s.session.Name(),
		Provider: s.provider, Model: s.model, Thinking: s.thinking,
		Exported: time.Now(), SystemPrompt: snapshot.System, Messages: snapshot.Messages,
	}
	if s.tools != nil {
		for _, t := range s.tools.All() {
			doc.Tools = append(doc.Tools, export.Tool{Name: t.Name(), Description: t.Description()})
		}
	}
	return doc
}

// exportTo writes the conversation to path in the format its extension
// names, as Pi's /export does: a .jsonl path gets the current branch as a
// session file, anything else the HTML page, and no path a page named after
// the session in the working directory. A .md path gets the Markdown this
// build wrote before it could render HTML, which some readers still prefer.
func exportTo(s exportSource, path string) (string, error) {
	if path == "" {
		path = defaultExportName(s.conversation)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.workingDir, path)
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}

	switch strings.ToLower(filepath.Ext(path)) {
	case ".jsonl":
		return path, exportBranch(s, path)
	case ".md":
		return path, os.WriteFile(path, []byte(renderConversation(s.conversation.ID, s.session.Snapshot().Messages)), 0o600)
	default:
		page, err := export.HTML(s.document())
		if err != nil {
			return "", err
		}
		// Owner-only, like the session file it came from: a conversation
		// carries source and command output nobody chose to publish.
		return path, os.WriteFile(path, []byte(page), 0o600)
	}
}

// exportBranch copies the conversation's current branch into a new session
// file, linear from its first entry to where it stands, as Pi's exportToJsonl
// does. Only a recorded conversation has a branch to copy.
func exportBranch(s exportSource, path string) error {
	store := s.conversation.Store
	if store == nil {
		return fmt.Errorf("this conversation is not recorded, so there is no session to export as .jsonl")
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	copied, err := store.BranchInto(context.Background(), path, s.workingDir,
		session.NewSessionID(time.Now()), store.Leaf())
	if err != nil {
		return err
	}
	return copied.Close()
}

// defaultExportName is Pi's: the session's own name with -session- in front,
// in the working directory.
func defaultExportName(c *Conversation) string {
	base := strings.TrimSuffix(filepath.Base(c.Path), ".jsonl")
	if c.Path == "" {
		base = c.ID
	}
	if base == "" {
		base = time.Now().UTC().Format("2006-01-02T15-04-05Z")
	}
	return "pi-go-session-" + base + ".html"
}

// pathArgument reads the path a command was given the way Pi's
// getPathCommandArgument does: a quoted path may hold spaces, an unquoted
// one ends at the first, and an unclosed quote is no path at all.
func pathArgument(arg string) (string, bool) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", true
	}
	if quote := arg[0]; quote == '"' || quote == '\'' {
		end := strings.IndexByte(arg[1:], quote)
		if end < 0 {
			return "", false
		}
		return arg[1 : end+1], true
	}
	if i := strings.IndexAny(arg, " \t"); i >= 0 {
		return arg[:i], true
	}
	return arg, true
}
