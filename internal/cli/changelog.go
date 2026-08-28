package cli

import _ "embed"

// changelog is CHANGELOG.md, embedded at build time so /changelog answers
// wherever the binary runs — the person asking is running the program, not
// sitting in its repository.
//
// go:embed cannot reach outside the package directory, so this is a COPY of
// the root CHANGELOG.md. A test compares the two byte for byte: a copy that
// can drift silently would eventually show users a changelog the repository
// has moved past, and the test turns that drift into a failing gate instead.
//
//go:embed CHANGELOG.md
var changelog string
