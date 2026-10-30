package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/settings"
)

func TestTheExternalEditorIsTheSettingThenTheEnvironmentThenNano(t *testing.T) {
	env := map[string]string{}
	getenv := func(name string) string { return env[name] }
	if got := ExternalEditorCommand(settings.Settings{}, getenv, "linux"); got != "nano" {
		t.Fatalf("nothing set: %q", got)
	}
	if got := ExternalEditorCommand(settings.Settings{}, getenv, "windows"); got != "notepad" {
		t.Fatalf("nothing set on windows: %q", got)
	}
	env["EDITOR"] = "vim"
	if got := ExternalEditorCommand(settings.Settings{}, getenv, "linux"); got != "vim" {
		t.Fatalf("EDITOR: %q", got)
	}
	env["VISUAL"] = "code --wait"
	if got := ExternalEditorCommand(settings.Settings{}, getenv, "linux"); got != "code --wait" {
		t.Fatalf("VISUAL: %q", got)
	}
	if got := ExternalEditorCommand(settings.Settings{ExternalEditor: " emacs "}, getenv, "linux"); got != "emacs" {
		t.Fatalf("the setting: %q", got)
	}
}

func TestEditingOutsideHandsBackWhatTheEditorLeft(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "editor.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'edited %s\\n' \"$(cat \"$1\")\" > \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	edited, ok, err := EditInExternalEditor(script, "the draft", strings.NewReader(""), &out, &out)
	if err != nil || !ok || edited != "edited the draft" {
		t.Fatalf("got %q %v %v", edited, ok, err)
	}
	if !strings.Contains(out.String(), "Launching external editor: "+script) {
		t.Fatalf("the launch was not announced: %q", out.String())
	}

	failing := filepath.Join(dir, "failing.sh")
	if err := os.WriteFile(failing, []byte("#!/bin/sh\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := EditInExternalEditor(failing, "kept", strings.NewReader(""), &out, &out); ok || err != nil {
		t.Fatalf("a failing editor gave ok=%v err=%v", ok, err)
	}
	if _, _, err := EditInExternalEditor(filepath.Join(dir, "missing"), "kept", strings.NewReader(""), &out, &out); err == nil {
		t.Fatal("an editor that cannot start was not an error")
	}
}
