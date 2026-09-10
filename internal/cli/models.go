package cli

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

const modelListingTimeout = 15 * time.Second

// ModelPatternError reports every command-line scope pattern that could not
// be applied to the provider's fresh listing. A scope must be deliberate: an
// unresolved entry must not turn into permission to cycle through unrelated
// models.
type ModelPatternError struct {
	Diagnostics []string
}

func (e *ModelPatternError) Error() string {
	if len(e.Diagnostics) == 0 {
		return "model scope could not be resolved"
	}
	return "model scope: " + strings.Join(e.Diagnostics, "; ")
}

// listAvailableModels performs provider discovery on demand.
//
// A model port may not know how to enumerate its provider. That absence is
// deliberately typed so callers can keep direct model selection working and
// can distinguish it from an empty successful list or a failed network call.
func listAvailableModels(ctx context.Context, port ai.Port, provider string) ([]ai.ModelRef, error) {
	lister, ok := port.(ai.ModelLister)
	if !ok {
		return nil, ai.ErrModelListingUnsupported
	}
	if ctx == nil {
		ctx = context.Background()
	}
	listingCtx, cancel := context.WithTimeout(ctx, modelListingTimeout)
	defer cancel()
	models, err := lister.ListModels(listingCtx)
	if err != nil {
		if listingCtx.Err() != nil {
			return nil, listingCtx.Err()
		}
		var providerErr *ai.ProviderError
		if errors.Is(err, ai.ErrModelListingUnsupported) {
			return nil, err
		}
		if errors.As(err, &providerErr) {
			return nil, err
		}
		return nil, &ai.ProviderError{Provider: provider, Failure: ai.FailureTransient, Detail: err.Error()}
	}
	return normalizeModelRefs(models, provider), nil
}

// normalizeModelRefs gives the model selector a stable order and removes
// duplicate provider/id pairs. Provider listings are external input, so blank
// records are ignored and a provider omitted by a local lister is filled from
// the port that answered it.
func normalizeModelRefs(models []ai.ModelRef, provider string) []ai.ModelRef {
	out := make([]ai.ModelRef, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model.Provider = strings.TrimSpace(model.Provider)
		if model.Provider == "" {
			model.Provider = provider
		}
		model.ID = strings.TrimSpace(model.ID)
		if model.Provider == "" || model.ID == "" {
			continue
		}
		key := model.Provider + "\x00" + model.ID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// cycleModels applies Pi's model-cycle rule to provider-reported identifiers:
// a nonempty scope is intersected with the current listing, one candidate is
// not cyclable, and an absent current model starts at index zero before the
// forward step. The model switch is committed only after the candidate is
// selected, so a failed open leaves the current model untouched.
func cycleModels(ctx context.Context, port ai.Port, provider, current string,
	scoped []ai.ModelRef, thinking ai.ThinkingLevel, switchModel func(provider, model string) error,
) (*ai.ModelCycle, error) {
	return cycleModelsWithPatterns(ctx, port, provider, current, scoped, nil, thinking, switchModel)
}

func cycleModelsWithPatterns(ctx context.Context, port ai.Port, provider, current string,
	scoped []ai.ModelRef, patterns []string, thinking ai.ThinkingLevel,
	switchModel func(provider, model string) error,
) (*ai.ModelCycle, error) {
	available, err := listAvailableModels(ctx, port, provider)
	if err != nil {
		return nil, err
	}
	if len(patterns) > 0 {
		// A raw --models scope is resolved only after the caller asks to cycle.
		// Every supplied pattern must resolve. Falling back to the complete
		// listing after a typo could select a model the caller did not name.
		var resolveErr error
		scoped, resolveErr = resolveModelPatterns(patterns, available)
		if resolveErr != nil {
			return nil, resolveErr
		}
	}
	candidates := available
	isScoped := len(scoped) > 0
	if isScoped {
		availableIDs := make(map[string]struct{}, len(available))
		for _, model := range available {
			availableIDs[model.Provider+"\x00"+model.ID] = struct{}{}
		}
		candidates = make([]ai.ModelRef, 0, len(scoped))
		seenScoped := make(map[string]struct{}, len(scoped))
		for _, model := range scoped {
			model.Provider = strings.TrimSpace(model.Provider)
			model.ID = strings.TrimSpace(model.ID)
			if model.Provider == "" {
				model.Provider = provider
			}
			if model.Provider == "" || model.ID == "" {
				continue
			}
			key := model.Provider + "\x00" + model.ID
			if _, ok := availableIDs[key]; ok {
				if _, duplicate := seenScoped[key]; duplicate {
					continue
				}
				seenScoped[key] = struct{}{}
				candidates = append(candidates, model)
			}
		}
	}
	if len(candidates) <= 1 {
		return nil, nil
	}
	currentIndex := -1
	for i, model := range candidates {
		if model.Provider == provider && model.ID == current {
			currentIndex = i
			break
		}
	}
	if currentIndex == -1 {
		currentIndex = 0
	}
	next := candidates[(currentIndex+1)%len(candidates)]
	if err := switchModel(next.Provider, next.ID); err != nil {
		return nil, err
	}
	return &ai.ModelCycle{Model: next, ThinkingLevel: thinking, IsScoped: isScoped}, nil
}

// resolveModelPatterns is the command-line scope's deliberately small subset
// of Pi's resolver. It supports case-insensitive exact provider/id references,
// bare exact ids, and * ? [ ] globs against either form. It leaves colon
// thinking suffixes and fuzzy alias selection to the later thinking/selector
// work; a colon remains part of a model id, which preserves ids such as
// "qwen2.5:7b".
func resolveModelPatterns(patterns []string, available []ai.ModelRef) ([]ai.ModelRef, error) {
	resolved := make([]ai.ModelRef, 0, len(available))
	seen := make(map[string]struct{}, len(available))
	var diagnostics []string
	add := func(model ai.ModelRef) {
		key := strings.ToLower(model.Provider + "\x00" + model.ID)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		resolved = append(resolved, model)
	}

	for _, raw := range patterns {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			diagnostics = append(diagnostics, `empty pattern is not supported`)
			continue
		}
		lower := strings.ToLower(pattern)
		if !hasModelGlob(pattern) {
			if model, ok := exactModelReference(lower, available); ok {
				add(model)
			} else {
				diagnostics = append(diagnostics, fmt.Sprintf("%q matched no available model", patternModelDiagnostic(pattern)))
			}
			continue
		}
		if _, err := path.Match(lower, ""); err != nil {
			diagnostics = append(diagnostics, fmt.Sprintf("%q has unsupported glob syntax: %v", pattern, err))
			continue
		}
		matched := false
		for _, model := range available {
			full := strings.ToLower(model.Provider + "/" + model.ID)
			id := strings.ToLower(model.ID)
			if modelGlobMatch(lower, full) || modelGlobMatch(lower, id) {
				matched = true
				add(model)
			}
		}
		if !matched {
			diagnostics = append(diagnostics, fmt.Sprintf("%q matched no available model", patternModelDiagnostic(pattern)))
		}
	}
	if len(diagnostics) > 0 {
		return resolved, &ModelPatternError{Diagnostics: diagnostics}
	}
	return resolved, nil
}

func patternModelDiagnostic(pattern string) string {
	if i := strings.LastIndex(pattern, ":"); i >= 0 && i+1 < len(pattern) {
		if _, err := ai.ParseThinkingLevel(pattern[i+1:]); err == nil {
			return pattern + " (per-pattern thinking levels are not supported)"
		}
	}
	return pattern
}

func exactModelReference(reference string, available []ai.ModelRef) (ai.ModelRef, bool) {
	var bare ai.ModelRef
	bareMatches := 0
	for _, model := range available {
		full := strings.ToLower(model.Provider + "/" + model.ID)
		if full == reference {
			return model, true
		}
		if strings.ToLower(model.ID) == reference {
			bare = model
			bareMatches++
		}
	}
	return bare, bareMatches == 1
}

func hasModelGlob(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

func modelGlobMatch(pattern, value string) bool {
	matched, err := path.Match(pattern, value)
	return err == nil && matched
}
