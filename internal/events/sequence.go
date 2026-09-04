package events

import "sync"

// Sequence hands out ordering numbers.
//
// One order is a promise about DELIVERY, not just about numbers: whoever uses a
// Sequence must allocate the number and deliver the thing it numbers under one
// critical section, or a lower number can still arrive after a higher one taken
// by whoever reached the lock first. Next guarantees the numbers are distinct
// and increasing; it cannot guarantee the deliveries happen in that order. The
// runtime's emitter and the JSON stream's writer each keep their own and each
// hold their own lock across the delivery, which is why each of their orders
// is real and why they are not the same order.
//
// The zero value is a valid, unused sequence starting at 1.
type Sequence struct {
	mu sync.Mutex
	n  int
}

// Next returns the next number, starting at 1.
func (s *Sequence) Next() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return s.n
}
