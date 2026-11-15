package extensionconformance

import (
	"encoding/json"
	"net"
	"os"
	"slices"
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/subprocess"
	"github.com/MichaelKinsy/PiG/extensions/sdk"
	"github.com/MichaelKinsy/PiG/test/extension-conformance/sdkscenarios"
)

// The same scenario asserts live known/null/expired reads, not a placement-specific fallback.
func TestSessionCacheObservationTopologies(t *testing.T) {
	scenarios := sdkscenarios.All()
	index := slices.IndexFunc(scenarios, func(s sdkscenarios.Scenario) bool { return s.ID == "session-cache-observation-known-and-unknown" })
	if index < 0 {
		t.Fatal("cache observation scenario missing")
	}
	scenario := scenarios[index]
	for _, language := range sdkscenarios.SDKs {
		t.Run("packed/"+string(language), func(t *testing.T) {
			rig := newScenarioRig(t, language, scenario, func(r *scenarioRig) { r.configs[0].Isolation = "" })
			if got := rig.observe(scenario); !slices.Equal(got, scenario.Want) {
				t.Fatalf("got %q want %q", got, scenario.Want)
			}
		})
	}
	t.Run("fused/go", func(t *testing.T) {
		rig := newScenarioRig(t, sdkscenarios.Go, scenario, func(r *scenarioRig) {
			ext := sdk.New("scenario-driver-go")
			ext.Command("q-cache", "Read cache observation", func(ctx sdk.Context, path string) error {
				got, err := ctx.GetSessionCacheObservation()
				if err != nil {
					return err
				}
				body, err := json.Marshal(got)
				if err != nil {
					return err
				}
				return os.WriteFile(path, body, 0600)
			})
			loaded, err := r.host.LoadInProcess(t.Context(), subprocess.ExtConfig{Name: "scenario-driver-go", Enabled: true}, func(conn net.Conn) error { return ext.RunWithConn(conn) })
			if err != nil {
				t.Fatal(err)
			}
			r.loaded = []extension.Extension{*loaded}
		})
		if got := rig.observe(scenario); !slices.Equal(got, scenario.Want) {
			t.Fatalf("got %q want %q", got, scenario.Want)
		}
	})
}
