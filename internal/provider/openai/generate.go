package openai

import (
	"context"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// Generate answers in one piece.
//
// The stream collected, not a second implementation: two request-building paths
// drift, and only one of them ends up covered by the tests that matter. The
// draining itself is shared, because what a finished reply is made of does not
// depend on which provider produced it.
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
