package events

import "sync"

// Sequence hands out the ordering numbers on the observable stream.
//
// It exists so that more than one producer can share a single order. In the
// JSON stream, lifecycle events, reply events, and — under `--mode rpc` —
// command responses are three families on one stdout, and a consumer
// reconstructs their true order from one monotonic counter (ADR-0009). If each
// family numbered itself, "before" would stop meaning anything the moment two
// families interleaved.
//
// The zero value is a valid, unused sequence starting at 1.
type Sequence struct {
	mu sync.Mutex
	n  int
}

// Next returns the next number, starting at 1.
//
// Whoever writes to the stream must allocate its number and write it under one
// critical section of their own, or a lower number can still reach the wire
// after a higher one. Next guarantees the numbers are distinct and increasing;
// it cannot guarantee the writes happen in that order.
func (s *Sequence) Next() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return s.n
}
