package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

const codexModelCacheTTL = 5 * time.Minute

type modelProvider interface {
	ID() string
	stream(context.Context, map[string]any, func(map[string]any) error) error
	collect(context.Context, map[string]any) (map[string]any, error)
	listModels(context.Context) ([]string, error)
}

type codexModelProvider struct {
	backend codexBackend
}

func (p codexModelProvider) ID() string { return "codex" }

func (p codexModelProvider) stream(ctx context.Context, payload map[string]any, fn func(map[string]any) error) error {
	return p.backend.stream(ctx, payload, fn)
}

func (p codexModelProvider) collect(ctx context.Context, payload map[string]any) (map[string]any, error) {
	return p.backend.collect(ctx, payload)
}

func (p codexModelProvider) listModels(ctx context.Context) ([]string, error) {
	return p.backend.listModels(ctx), nil
}

type providerRoute struct {
	Provider                     string
	RequestedModel               string
	ActualUpstreamModel          string
	ControlPlaneProjectAvailable bool
	CatalogSize                  int
	OAuthTokenExpiry             time.Time
}

type providerRouter struct {
	codex        modelProvider
	antigravity  *antigravityProvider
	mu           sync.Mutex
	codexModels  []string
	codexExpires time.Time
}

func newProviderRouter(cfg config, backend codexBackend) (*providerRouter, error) {
	router := &providerRouter{codex: codexModelProvider{backend: backend}}
	if !cfg.AntigravityEnabled {
		return router, nil
	}
	antigravity, err := newAntigravityProvider(cfg)
	if err != nil {
		return nil, err
	}
	router.antigravity = antigravity
	return router, nil
}

func (r *providerRouter) codexIDs(ctx context.Context) []string {
	if r == nil || r.codex == nil {
		return nil
	}
	now := time.Now()
	r.mu.Lock()
	if now.Before(r.codexExpires) && len(r.codexModels) > 0 {
		ids := append([]string(nil), r.codexModels...)
		r.mu.Unlock()
		return ids
	}
	r.mu.Unlock()
	ids, err := r.codex.listModels(ctx)
	if err != nil || len(ids) == 0 {
		ids = append([]string(nil), defaultModels...)
	}
	r.mu.Lock()
	r.codexModels = uniqueModelIDs(ids)
	r.codexExpires = now.Add(codexModelCacheTTL)
	ids = append([]string(nil), r.codexModels...)
	r.mu.Unlock()
	return ids
}

func (r *providerRouter) resolve(ctx context.Context, requested string) (modelProvider, providerRoute, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return nil, providerRoute{}, &backendError{400, "A model is required."}
	}
	codexMatch := containsModelID(r.codexIDs(ctx), requested)
	if codexMatch {
		return r.codex, providerRoute{
			Provider:            r.codex.ID(),
			RequestedModel:      requested,
			ActualUpstreamModel: requested,
		}, nil
	}
	if r.antigravity == nil {
		return nil, providerRoute{}, &backendError{400, fmt.Sprintf("Model %q is not owned by an enabled provider.", requested)}
	}
	resolution, err := r.antigravity.resolveModel(ctx, requested)
	if err != nil {
		return nil, providerRoute{}, err
	}
	if !resolution.Verified() || !isStableAntigravityModel(resolution.ActualUpstreamModel) {
		return nil, providerRoute{}, &backendError{400, fmt.Sprintf("Model %q was not verified in the current Antigravity catalog; no fallback model was selected.", requested)}
	}
	return r.antigravity, providerRoute{
		Provider:                     r.antigravity.ID(),
		RequestedModel:               resolution.RequestedModel,
		ActualUpstreamModel:          resolution.ActualUpstreamModel,
		ControlPlaneProjectAvailable: r.antigravity.projectSnapshot() != "",
		CatalogSize:                  r.antigravity.catalogSize(),
		OAuthTokenExpiry:             r.antigravity.oauthExpiry(),
	}, nil
}

func (r *providerRouter) listModels(ctx context.Context) ([]string, error) {
	if r == nil {
		return nil, nil
	}
	ids := r.codexIDs(ctx)
	if r.antigravity != nil {
		antigravityIDs, err := r.antigravity.listModels(ctx)
		if err == nil {
			ids = append(ids, antigravityIDs...)
		}
	}
	return uniqueModelIDs(ids), nil
}

func uniqueModelIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func containsModelID(ids []string, requested string) bool {
	for _, id := range ids {
		if id == requested {
			return true
		}
	}
	return false
}

var _ modelProvider = codexModelProvider{}
