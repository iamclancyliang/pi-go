package ai

import (
	"context"
	"errors"
)

// ModelRef identifies a model a provider reported as available.
//
// A provider listing carries identity only. Model facts such as context window
// and reasoning support remain in the separately sourced catalogue, so a
// provider cannot accidentally turn an identifier listing into an unsourced
// claim about how a model behaves. A model can be callable even when no facts
// have been recorded for it.
type ModelRef struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

// ModelLister is the optional model-discovery capability of a model port.
//
// Listing is deliberately separate from Port: providers are allowed to omit
// it, and a caller must be able to use a model by name when discovery is not
// available. Implementations must perform the provider call only when this
// method is invoked; constructing or using a Port must not discover models.
type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelRef, error)
}

// ErrModelListingUnsupported says that a provider has no listing endpoint.
// This is distinct from an empty successful listing and from a provider call
// that failed, because each one leads a caller to a different next action.
var ErrModelListingUnsupported = errors.New("model listing is not supported by this provider")

// ModelCycle is the result of moving to another model for the next turn.
type ModelCycle struct {
	Model         ModelRef      `json:"model"`
	ThinkingLevel ThinkingLevel `json:"thinkingLevel"`
	IsScoped      bool          `json:"isScoped"`
}
