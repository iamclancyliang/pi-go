//go:build darwin || linux

package tui

import (
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// waitReadable reports whether fd has input within timeout.
//
// A terminal device cannot always join Go's poller — macOS refuses it — and
// then read deadlines do nothing, so a blocked read could never be
// interrupted to stop. select(2) is used rather than poll(2) because macOS's
// poll does not support devices and reports a terminal as invalid, which
// would read as ready and block.
func waitReadable(fd int, timeout time.Duration) (bool, error) {
	var set unix.FdSet
	set.Set(fd)
	tv := unix.NsecToTimeval(timeout.Nanoseconds())
	n, err := unix.Select(fd+1, &set, nil, nil, &tv)
	if errors.Is(err, unix.EINTR) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return n > 0 && set.IsSet(fd), nil
}
