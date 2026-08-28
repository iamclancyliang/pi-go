package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// newEntryID makes an identity that sorts by when it was made.
//
// Time first, randomness after. Sorting by identity then agrees with the order
// things happened, which is what lets a directory listing or a set of records be
// ordered without reading a timestamp field out of each one — and what keeps two
// entries written in the same millisecond apart.
//
// Not a UUID library: the only properties needed are uniqueness and that order,
// and a dependency that provides them is a dependency to keep current.
func newEntryID(at time.Time) string {
	var suffix [10]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		// Randomness is unavailable, which on a running system means something
		// is very wrong. The nanosecond keeps ids distinct enough to finish the
		// session rather than failing a write that has already happened.
		return fmt.Sprintf("%013x-%016x", at.UTC().UnixMilli(), at.UnixNano())
	}
	return fmt.Sprintf("%013x-%s", at.UTC().UnixMilli(), hex.EncodeToString(suffix[:]))
}

// NewSessionID makes an identity for one conversation.
//
// The same shape as an entry's, so a session file and the records inside it
// sort the same way and a reader cannot mistake one kind of id for another
// having a different form.
func NewSessionID(at time.Time) string { return newEntryID(at) }
