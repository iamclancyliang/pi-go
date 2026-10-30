//go:build windows

package interactive

import "errors"

// suspendProcess cannot stop a process on Windows, which has no job control;
// the mode says so before it would be called.
func suspendProcess() error {
	return errors.New("suspend to background is not supported on Windows")
}
