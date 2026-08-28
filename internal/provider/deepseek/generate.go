package deepseek

import (
	"context"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// Generate answers in one piece.
//
// It is the stream collected, not a second implementation. Pi has exactly one
// production path — its non-streaming call is its streaming call awaited — and
// the same holds here. Two request-building paths drift, and only one of them
// ends up covered by the tests that matter. The draining is shared too, because
// what a finished reply is made of does not depend on which provider produced
// it — including that a failed final attempt is still an attempt, and is
// reported as one even when the provider said nothing about what it used.
func (p *Port) Generate(ctx context.Context, req ai.Request) (ai.Response, error) {
	events, err := p.Stream(ctx, req)
	if err != nil {
		return ai.Response{}, err
	}
	return ai.Collect(providerName, events)
}

// Compile-time proof that this satisfies both boundaries.
var (
	_ ai.Port          = (*Port)(nil)
	_ ai.StreamingPort = (*Port)(nil)
)
