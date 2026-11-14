package coding

import "github.com/MichaelKinsy/PiG/coding/extension"

// SessionCacheObservation returns the last session request's model-based prompt-cache lifetime, including when warming is off. Unknown state is nil, whereas an elapsed lifetime remains available. No history is retained or reconstructed after resume. Caller-supplied Agents and warmers do not supply this diagnostic.
func (s *Session) SessionCacheObservation() *extension.SessionCacheObservation {
	// pig additive (D616): use the Session-owned warmer's scalar observation, not its scheduling status.
	warmer, _ := s.cacheWarmer()
	if warmer == nil {
		return nil
	}
	observation := warmer.SessionCacheObservation()
	model := s.Model()
	if observation == nil || model == nil || observation.Provider != providerID(model) || observation.Model != model.ID {
		return nil
	}
	return observation
}
