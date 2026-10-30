//go:build !windows

package interactive

import (
	"os"
	"os/signal"
	"syscall"
)

// suspendProcess stops this process group the way ctrl+z in a shell does, and
// returns when the shell continues it — Pi's handleCtrlZ. The terminal is
// already back in the shell's hands when this runs.
func suspendProcess() error {
	resumed := make(chan os.Signal, 1)
	signal.Notify(resumed, syscall.SIGCONT)
	defer signal.Stop(resumed)
	// The whole group, as a shell's ctrl+z would stop it: a tool's subprocess
	// left running in the background would otherwise keep writing to the
	// terminal the shell has taken back.
	if err := syscall.Kill(0, syscall.SIGTSTP); err != nil {
		return err
	}
	<-resumed
	return nil
}
