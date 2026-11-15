package codingagent

import (
	"math"

	"github.com/MichaelKinsy/PiG/coding/extension"
)

// SessionCacheObservation returns a scalar snapshot independent of the warmer's active run. Closing the Session makes its observation unknown.
func (w *CacheWarmer) SessionCacheObservation() *extension.SessionCacheObservation {
	// pig additive (D616): a read neither starts warming nor walks Session history.
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.observation == nil {
		return nil
	}
	observation := *w.observation
	return &observation
}

// DiscardSessionCacheObservation makes a failed or aborted real request unknown without changing warming policy.
func (w *CacheWarmer) DiscardSessionCacheObservation() {
	// pig additive (D616): failed requests do not prove that a prompt cache was written.
	w.mu.Lock()
	defer w.mu.Unlock()
	w.observation = nil
}

func (w *CacheWarmer) observeSessionCacheLocked(request CacheWarmRequest, startedAt int64) {
	// pig additive (D616): use the same effective retention/environment calculation as the warmer, without its replay/economics restrictions.
	w.observation = nil
	if request.Model == nil {
		return
	}
	ttlMs, ok := GetPromptCacheTtlMs(request.Model, request.Options)
	if !ok || ttlMs <= 0 || math.IsNaN(ttlMs) || math.IsInf(ttlMs, 0) {
		return
	}
	w.observation = &extension.SessionCacheObservation{StartedAt: startedAt, TTLms: ttlMs, Provider: request.Model.ProviderMeta.ProviderID, Model: request.Model.ID}
}
