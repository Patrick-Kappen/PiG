package directcore

import (
	"testing"

	"github.com/Patrick-Kappen/MyPiglets/extensions/core/api"
	"github.com/Patrick-Kappen/MyPiglets/modules/gateway"

	"github.com/MichaelKinsy/PiG/coding/extension"
)

// This measures the real core+gateway, without model latency: the used header transformer and the unconditional shutdown handler. Startup, file logging and first-session-ID lookup are outside the header steady-state timing; shutdown includes ID lookup/reset and draining.
func BenchmarkCoreEvents(b *testing.B) {
	for _, event := range []string{"before_provider_headers", "session_shutdown"} {
		for _, route := range []string{"direct", "fused"} {
			b.Run(event+"/"+route, func(b *testing.B) {
				b.Setenv("PIG_HOME", b.TempDir())
				id := "bench-session"
				runner := loadCore(b, route, []api.Module{gateway.Module()}, &id)
				input := headers(map[string]any{"x-gateway-key": "fixture", "content-type": "application/json"})
				if _, err := runner.EmitBeforeProviderHeaders(b.Context(), input); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				for b.Loop() {
					if event == "before_provider_headers" {
						out, err := runner.EmitBeforeProviderHeaders(b.Context(), input)
						if err != nil || out["x-pi-session"] == nil || *out["x-pi-session"] != id {
							b.Fatalf("headers: %v, %v", out, err)
						}
					} else if _, err := runner.Emit(b.Context(), extension.SessionShutdownEvent{Type: "session_shutdown", Reason: "quit"}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
