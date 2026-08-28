package cli

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// osc52Limit bounds what may be sent through the terminal escape.
//
// A long payload desynchronises terminal rendering, so past this the fallback
// refuses rather than corrupting the display of the session the user is in.
const osc52Limit = 100_000

// copyToClipboard puts text on the system clipboard.
//
// The tools are tried in Pi's order, and the reason is Linux: a Wayland session
// has no X11 clipboard, so a working xclip on such a system writes somewhere
// nothing reads. wl-copy is tried first there, and the X11 tools are the
// fallback rather than the default.
//
// The terminal escape is last, not first. Emitting it before trying the real
// tools makes terminals that do not support it print the payload into the
// session the user is looking at.
func copyToClipboard(text string) error {
	var attempts []error
	for _, candidate := range clipboardCommands() {
		err := runClipboard(candidate.name, candidate.args, text)
		if err == nil {
			return nil
		}
		attempts = append(attempts, err)
	}
	if err := writeOSC52(text); err == nil {
		return nil
	} else {
		attempts = append(attempts, err)
	}
	return fmt.Errorf("no way to reach the clipboard: %v", attempts)
}

type clipboardCommand struct {
	name string
	args []string
}

func clipboardCommands() []clipboardCommand {
	switch runtime.GOOS {
	case "darwin":
		return []clipboardCommand{{name: "pbcopy"}}
	case "windows":
		return []clipboardCommand{{name: "clip"}}
	default:
		// Wayland first when there is one: an X11 tool on a Wayland session
		// succeeds and writes to a clipboard nothing is reading.
		var out []clipboardCommand
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			out = append(out, clipboardCommand{name: "wl-copy"})
		}
		return append(out,
			clipboardCommand{name: "xclip", args: []string{"-selection", "clipboard"}},
			clipboardCommand{name: "xsel", args: []string{"--clipboard", "--input"}},
		)
	}
}

func runClipboard(name string, args []string, text string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("%s: not installed", name)
	}
	cmd := exec.Command(path, args...)
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// writeOSC52 asks the terminal itself to hold the text.
//
// The last resort, and it works where no clipboard tool is installed — over
// ssh, in a container. Written to the terminal rather than to stdout, so a
// redirected run cannot put an escape sequence into a file.
func writeOSC52(text string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	if len(encoded) > osc52Limit {
		return fmt.Errorf("too long to send through the terminal")
	}
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("no terminal to send it through: %w", err)
	}
	defer tty.Close()
	_, err = fmt.Fprintf(tty, "\x1b]52;c;%s\x07", encoded)
	return err
}
