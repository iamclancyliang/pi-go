package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/settings"
)

// ExternalEditorCommand is what ctrl+g opens: the `externalEditor` setting,
// then $VISUAL, then $EDITOR, then Notepad on Windows and nano elsewhere — Pi's
// getExternalEditorCommand.
func ExternalEditorCommand(s settings.Settings, getenv func(string) string, goos string) string {
	if configured := strings.TrimSpace(s.ExternalEditor); configured != "" {
		return configured
	}
	for _, name := range []string{"VISUAL", "EDITOR"} {
		if command := getenv(name); command != "" {
			return command
		}
	}
	if goos == "windows" {
		return "notepad"
	}
	return "nano"
}

// EditInExternalEditor hands content to an editor and returns what it left,
// ok false when the editor exited with an error — Pi's editInExternalEditor.
// The command is split on spaces and given the file last; the editor gets the
// streams the interface was drawn on.
func EditInExternalEditor(command, content string, stdin io.Reader, stdout, stderr io.Writer) (string, bool, error) {
	dir, err := os.MkdirTemp("", "pi-editor-")
	if err != nil {
		return "", false, err
	}
	// Removed whatever happens: it holds what was being written.
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", false, err
	}
	parts := strings.Split(command, " ")
	if parts[0] == "" {
		return "", false, fmt.Errorf("no external editor is configured")
	}
	fmt.Fprintf(stdout, "Launching external editor: %s\npi-go will resume when the editor exits.\n", command)
	cmd := exec.Command(parts[0], append(parts[1:], path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("could not start %s: %w", parts[0], err)
	}
	edited, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	return strings.TrimSuffix(string(edited), "\n"), true, nil
}
