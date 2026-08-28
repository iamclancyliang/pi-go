package tools

import (
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func runtimeGosched() { runtime.Gosched() }

func timeoutAfterSeconds(n int) <-chan time.Time {
	return time.After(time.Duration(n) * time.Second)
}

// TestOneFileIsMutatedByOneCallAtATime proves the exclusion directly rather
// than through a write and a hope.
//
// Asserting it through the filesystem does not work: a whole-file write of a
// few kilobytes usually completes in one syscall, so the file comes back intact
// whether or not anything serialised the callers. That test passes with the
// lock removed, which makes it evidence of nothing. Counting overlap inside the
// guarded section is the thing actually being claimed.
func TestOneFileIsMutatedByOneCallAtATime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contended.txt")

	var inside, overlaps int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = fileMutations.do(path, func() error {
				if atomic.AddInt32(&inside, 1) > 1 {
					atomic.AddInt32(&overlaps, 1)
				}
				// Long enough that an unguarded section would reliably be
				// entered by another goroutine while this one holds it.
				for n := 0; n < 2000; n++ {
					runtimeGosched()
				}
				atomic.AddInt32(&inside, -1)
				return nil
			})
		}()
	}
	wg.Wait()

	if overlaps != 0 {
		t.Fatalf("%d calls entered the guarded section while another held it", overlaps)
	}
}

// TestDifferentFilesAreNotSerialised is the reason this is a per-file lock and
// not a Sequential declaration. A guard that made every mutation wait for every
// other would serialise a round of unrelated writes.
func TestDifferentFilesAreNotSerialised(t *testing.T) {
	dir := t.TempDir()
	const callers = 8

	// Every caller holds its own file and waits for all the others to arrive.
	// If the lock were global this could not complete, because the first holder
	// would still be waiting when the others were blocked behind it.
	var arrived sync.WaitGroup
	arrived.Add(callers)
	done := make(chan struct{})

	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = fileMutations.do(filepath.Join(dir, string(rune('a'+n))), func() error {
				arrived.Done()
				arrived.Wait()
				return nil
			})
		}(i)
	}
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-timeoutAfterSeconds(10):
		t.Fatal("mutations of different files waited for each other")
	}
}

// TestTheLockTableDoesNotGrow: a map entry per path that is never removed is a
// leak in a process that edits many files over a long session.
func TestTheLockTableDoesNotGrow(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 200; i++ {
		path := filepath.Join(dir, string(rune('a'+i%26))+"-file")
		if err := fileMutations.do(path, func() error { return nil }); err != nil {
			t.Fatalf("do: %v", err)
		}
	}
	fileMutations.mu.Lock()
	held := len(fileMutations.locks)
	fileMutations.mu.Unlock()
	if held != 0 {
		t.Fatalf("%d locks were left behind after every caller finished", held)
	}
}
