# Session cache observation (D616)

An extension can read PiG's prompt-cache lifetime for the Session's last dispatched request or successful warmer refresh. This is a passive, product-neutral diagnostic. Reading it starts no request or timer and scans no history. It does not select a cache policy. Pi's events and their fields stay unchanged.

| SDK | Read |
|---|---|
| Go | `ctx.GetSessionCacheObservation()` |
| Rust | `ctx.get_session_cache_observation()` |
| Python | `ctx.get_session_cache_observation()` |
| Lisp | `(pig:ctx-get-session-cache-observation ctx)` |
| Node / TypeScript | `ctx.getSessionCacheObservation()` |

The wire call is `getSessionCacheObservation`, with no arguments. Its result is null when unknown, or an object with `startedAt` (Unix milliseconds), `ttlMs` (a positive finite lifetime in milliseconds), `provider` and `model`. Null does not mean expired. A known observation remains available after its lifetime elapsed. Compare elapsed milliseconds with `ttlMs`; do not assume a five-minute lifetime.

PiG observes real Session requests immediately before streaming, not when the answer or run ends. It uses the warmer's `GetPromptCacheTtlMs` calculation: explicit request retention wins over request environment and process environment, and a missing tier or retention `none` is unknown. Warming mode, replay safety and refresh economics do not control observation. Warming can be off.

A successful refresh renews the observation from its dispatch time, not its completion time. A failed, aborted or retired refresh does not renew it. A failed or aborted real response makes the observation unknown. The provider can still evict a cache early; this is PiG's model-based lifetime, not a guarantee from the provider.

The initial state, a missing lifetime, a model mismatch, a resumed or replaced Session before its first request, and a closed Session are unknown. Historical assistant timestamps do not reconstruct observations. A caller-supplied Agent or warmer does not supply this diagnostic. Returned objects are snapshots.

Go, Rust, Python and Lisp calls block until the host returns the scalar snapshot or an error. Node reads synchronously through its existing host bridge. Calls obey ordinary request cancellation and stale-context checks. They run outside the host's TUI loop and retain no provider context or conversation. Isolated, packed and fused realizations use the same host call.
