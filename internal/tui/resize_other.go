//go:build !unix

package tui

// watchResize has no resize signal to watch on this platform; the size is
// read again on every frame, so a resize shows at the next one.
func watchResize(func()) (stop func()) { return func() {} }
