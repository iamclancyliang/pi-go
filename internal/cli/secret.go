package cli

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
)

// readSecret reads one line from the terminal without showing it.
//
// From /dev/tty rather than stdin, because stdin is where the conversation
// comes from and may be a pipe. Echo is turned off around the read, so the key
// does not stay in the scrollback of the session it was typed into — which
// outlives the terminal and is copied when someone shares their screen.
//
// If echo cannot be turned off, the key is NOT read: silently reading it in
// plain sight would put a credential on screen that the user was told would not
// appear.
func readSecret() (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", err
	}
	defer tty.Close()

	restore, err := silenceEcho(tty)
	if err != nil {
		return "", err
	}
	defer restore()

	line, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// silenceEcho turns terminal echo off and returns how to put it back.
//
// Through stty rather than a raw ioctl: the terminal settings are the shell's
// to restore too, and shelling out keeps this to one well-understood command
// instead of platform-specific termios handling.
func silenceEcho(tty *os.File) (func(), error) {
	saved, err := sttyOutput(tty, "-g")
	if err != nil {
		return nil, err
	}
	if err := stty(tty, "-echo"); err != nil {
		return nil, err
	}
	return func() { _ = stty(tty, saved) }, nil
}

func stty(tty *os.File, args ...string) error {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = tty
	cmd.Stdout = os.Stderr
	return cmd.Run()
}

func sttyOutput(tty *os.File, args ...string) (string, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = tty
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
