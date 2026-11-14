package extension

// SessionCacheObservation is PiG's last dispatched session request's prompt-cache lifetime. StartedAt is Unix milliseconds and TTLms is the effective model lifetime in milliseconds. A non-nil observation can be expired; nil means unknown, not expired. This is a model-based observation, not proof that the provider retained the cache.
// pig additive (D616): product-neutral, read-only cache diagnostics selected by an extension.
type SessionCacheObservation struct {
	StartedAt int64   `json:"startedAt"`
	TTLms     float64 `json:"ttlMs"`
	Provider  string  `json:"provider"`
	Model     string  `json:"model"`
}

// GetSessionCacheObservation reads the current Session's observation without starting warming or waiting for a provider. Unknown state is nil. A stale context returns its usual error.
func (c *Context) GetSessionCacheObservation() (*SessionCacheObservation, error) {
	// pig additive (D616): a diagnostic host call does not extend a ported event.
	if err := c.assertActive(); err != nil {
		return nil, err
	}
	if c.actions.GetSessionCacheObservation == nil {
		return nil, nil
	}
	return c.actions.GetSessionCacheObservation(), nil
}
