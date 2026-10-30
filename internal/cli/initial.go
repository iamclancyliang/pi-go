package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tui"
)

// PrepareInitialMessage folds piped input and @file contents into the first
// message, in Pi's order: piped input, then the files, then what was typed, with
// nothing between them. The file wrapper carries its own line breaks.
//
// Whitespace-only input counts as none, so a closed or empty stdin does not turn
// into a message.
func PrepareInitialMessage(args Args, workingDir, piped string) (Args, error) {
	files, err := fileArguments(args.FileArgs, workingDir)
	if err != nil {
		return args, err
	}
	parts := []string{}
	if trimmed := strings.TrimSpace(piped); trimmed != "" {
		parts = append(parts, trimmed)
	}
	if files != "" {
		parts = append(parts, files)
	}
	if len(parts) == 0 {
		return args, nil
	}
	rest := args.Messages
	if len(rest) > 0 {
		parts = append(parts, rest[0])
		rest = rest[1:]
	}
	args.Messages = append([]string{strings.Join(parts, "")}, rest...)
	return args, nil
}

// fileArguments wraps each file for the model. An empty file is skipped. A missing
// one ends the run, because answering without a file the question was about would
// look like an answer about it.
func fileArguments(paths []string, workingDir string) (string, error) {
	var text strings.Builder
	for _, path := range paths {
		absolute := absoluteUnder(workingDir, path)
		content, err := os.ReadFile(absolute)
		if err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("file not found: %s", absolute)
			}
			return "", fmt.Errorf("could not read file %s: %w", absolute, err)
		}
		if len(content) == 0 {
			continue
		}
		// Refused, where Pi attaches it: messages here carry text only, and the
		// bytes of an image sent as text would cost a great deal to say nothing.
		if isImage(content) {
			return "", fmt.Errorf("%s is an image, and this build cannot send images to a model", absolute)
		}
		fmt.Fprintf(&text, "<file name=%q>\n%s\n</file>\n", absolute,
			strings.ToValidUTF8(string(content), "�"))
	}
	return text.String(), nil
}

// isImage recognises the formats Pi attaches, by their opening bytes rather than
// by a name that may not match them.
func isImage(content []byte) bool {
	switch {
	case bytes.HasPrefix(content, []byte("\x89PNG\r\n\x1a\n")),
		bytes.HasPrefix(content, []byte("\xff\xd8\xff")),
		bytes.HasPrefix(content, []byte("GIF87a")),
		bytes.HasPrefix(content, []byte("GIF89a")):
		return true
	}
	return len(content) >= 12 && string(content[:4]) == "RIFF" && string(content[8:12]) == "WEBP"
}

// ExportFile writes a session file out as a page, or whatever the output's
// extension names, without running anything. The output is the first message, as
// in Pi, and defaults to a name beside the working directory's other exports.
func ExportFile(input string, args Args, workingDir string) (string, error) {
	path := absoluteUnder(workingDir, input)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("file not found: %s", path)
	}
	store, err := session.OpenFileStore(path, workingDir, session.NewSessionID(time.Now()))
	if err != nil {
		return "", err
	}
	defer store.Close()
	sess, err := session.Restore(context.Background(), "", store)
	if err != nil {
		return "", err
	}
	source := exportSource{
		conversation: &Conversation{Session: sess, Path: path, ID: store.ID(), Store: store},
		session:      sess,
		workingDir:   workingDir,
	}
	source.provider, source.model, _ = sess.RecordedModel()
	source.thinking, _ = sess.RecordedThinking()

	output := ""
	if len(args.Messages) > 0 {
		output = args.Messages[0]
	}
	return exportTo(source, output)
}

// RunListModels prints the models this build can name, for the providers a
// credential is configured for. A named --provider is listed whether or not one
// is, since Ollama needs none.
//
// The rows are what is recorded plus what a provider reports when asked, which
// is a fraction of what any provider offers, and a name absent here can still be
// used (see ADR-0008). A fact nothing records shows as "-" rather than as a guess.
func RunListModels(ctx context.Context, args Args, streams Streams, transport http.RoundTripper) int {
	offline := args.Offline || truthyEnv(os.Getenv("PI_OFFLINE"))
	if args.Provider != "" {
		if _, err := SelectProvider(args); err != nil {
			fmt.Fprintf(streams.Err, "pi: %v\n", err)
			return 1
		}
	}

	var refs []ai.ModelRef
	for _, name := range ProviderNames() {
		if args.Provider != "" && name != args.Provider {
			continue
		}
		if args.Provider == "" && !hasCredential(args, name) {
			continue
		}
		provider := Providers[name]
		seen := map[string]bool{}
		add := func(id string) {
			if id != "" && !seen[id] {
				seen[id] = true
				refs = append(refs, ai.ModelRef{Provider: name, ID: id})
			}
		}
		add(provider.DefaultModel)
		for _, id := range ai.KnownModels(name) {
			add(id)
		}
		if offline {
			continue
		}
		scoped := args
		scoped.Provider = name
		port, _, _, err := Open(scoped, transport)
		if err != nil {
			fmt.Fprintf(streams.Err, "pi: warning: %s: %v\n", name, err)
			continue
		}
		listed, err := listAvailableModels(ctx, port, name)
		if err != nil && !errors.Is(err, ai.ErrModelListingUnsupported) {
			fmt.Fprintf(streams.Err, "pi: warning: %s: could not list models: %v\n", name, err)
		}
		for _, ref := range listed {
			add(ref.ID)
		}
	}

	if len(refs) == 0 {
		fmt.Fprintln(streams.Out, "No models available. Run /login, or set a provider's credential in the environment.")
		return 0
	}
	if args.ListModelsPattern != "" {
		refs = tui.FuzzyFilter(refs, args.ListModelsPattern, func(r ai.ModelRef) string {
			return r.Provider + " " + r.ID
		})
		if len(refs) == 0 {
			fmt.Fprintf(streams.Out, "No models matching %q\n", args.ListModelsPattern)
			return 0
		}
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].Provider != refs[j].Provider {
			return refs[i].Provider < refs[j].Provider
		}
		return refs[i].ID < refs[j].ID
	})

	rows := [][]string{{"provider", "model", "context", "max-out", "thinking"}}
	for _, ref := range refs {
		facts, _ := ai.Facts(ref.Provider, ref.ID)
		thinking := "-"
		if facts.ReasoningKnown {
			thinking = "no"
			if facts.Reasoning {
				thinking = "yes"
			}
		}
		rows = append(rows, []string{ref.Provider, ref.ID,
			tokenCount(facts.ContextWindow), tokenCount(facts.MaxOutputTokens), thinking})
	}
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for at, cell := range row {
			widths[at] = max(widths[at], len(cell))
		}
	}
	for _, row := range rows {
		cells := make([]string, len(row))
		for at, cell := range row {
			cells[at] = fmt.Sprintf("%-*s", widths[at], cell)
		}
		fmt.Fprintln(streams.Out, strings.Join(cells, "  "))
	}
	return 0
}

// hasCredential reports a credential for a provider, stored or in the environment.
func hasCredential(args Args, name string) bool {
	if storedKey(args, name) != "" {
		return true
	}
	for _, v := range Providers[name].EnvVars {
		if strings.TrimSpace(os.Getenv(v)) != "" {
			return true
		}
	}
	return false
}

// tokenCount is Pi's: 200000 is 200K and 1048576 is 1.0M. Zero means not recorded
// here, which is not a window of nothing.
func tokenCount(count int) string {
	switch {
	case count <= 0:
		return "-"
	case count >= 1_000_000:
		if count%1_000_000 == 0 {
			return fmt.Sprintf("%dM", count/1_000_000)
		}
		return fmt.Sprintf("%.1fM", float64(count)/1_000_000)
	case count >= 1_000:
		if count%1_000 == 0 {
			return fmt.Sprintf("%dK", count/1_000)
		}
		return fmt.Sprintf("%.1fK", float64(count)/1_000)
	default:
		return fmt.Sprint(count)
	}
}

// truthyEnv is Pi's reading of a switch in the environment.
func truthyEnv(value string) bool {
	lower := strings.ToLower(value)
	return value == "1" || lower == "true" || lower == "yes"
}
