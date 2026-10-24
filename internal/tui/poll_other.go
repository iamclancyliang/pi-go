//go:build !darwin && !linux

package tui

import "time"

// waitReadable cannot poll on this platform; it reports input as ready and
// lets the read block.
func waitReadable(int, time.Duration) (bool, error) { return true, nil }
