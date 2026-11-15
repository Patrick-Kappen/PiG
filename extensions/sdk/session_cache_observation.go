package sdk

// SessionCacheObservation is PiG's model-based cache lifetime for the last dispatched session request or successful warmer refresh. StartedAt is Unix milliseconds. TTLms is the effective model lifetime in milliseconds. Elapsed observations remain available; nil means unknown. The provider can evict a cache earlier.
type SessionCacheObservation struct {
	StartedAt int64   `json:"startedAt"`
	TTLms     float64 `json:"ttlMs"`
	Provider  string  `json:"provider"`
	Model     string  `json:"model"`
}

// GetSessionCacheObservation reads passive diagnostics without warming the cache, reading history, or waiting for a provider.
func (c Context) GetSessionCacheObservation() (*SessionCacheObservation, error) {
	// pig additive (D616): this separate host call does not change Pi's events.
	return hostValue[*SessionCacheObservation](c, "getSessionCacheObservation", nil)
}
