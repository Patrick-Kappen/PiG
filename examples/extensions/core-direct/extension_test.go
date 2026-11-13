package directcore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Patrick-Kappen/MyPiglets/extensions/core/api"
	"github.com/Patrick-Kappen/MyPiglets/extensions/core/run"
	"github.com/Patrick-Kappen/MyPiglets/modules/gateway"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/factoryload"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
	"github.com/MichaelKinsy/PiG/coding/extension/host/subprocess"
)

type sessionManager struct {
	extension.ReadonlySessionManager
	id *string
}

func (m sessionManager) GetSessionId() string { return *m.id }

func loadCore(tb testing.TB, route string, modules []api.Module, id *string) *inproc.Runner {
	tb.Helper()
	var loaded extension.Extension
	var runtime *extension.ExtensionRuntime
	if route == "direct" {
		runtime = extension.CreateExtensionRuntime()
		var err error
		loaded, err = factoryload.LoadExtensionFromFactory(func(host extension.API) error { return Attach(host, modules...) }, tb.TempDir(), nil, runtime, "core-direct")
		if err != nil {
			tb.Fatal(err)
		}
	} else {
		host := subprocess.NewHostWithConfigRoot(tb.TempDir(), tb.TempDir())
		tb.Cleanup(func() { host.Shutdown("core proof complete") })
		bridge := subprocess.NewUIBridge(func() {})
		bridge.SetActions(&subprocess.HostCallbacks{GetSessionID: func() string { return *id }})
		host.SetUIBridge(bridge)
		ext, err := host.LoadInProcess(context.Background(), subprocess.ExtConfig{Name: "core", Enabled: true}, run.Extension("core", modules...).RunWithConn)
		if err != nil {
			tb.Fatal(err)
		}
		loaded, runtime = *ext, host.Runtime()
	}
	runner := inproc.NewRunner([]extension.Extension{loaded}, tb.TempDir(), runtime)
	runner.BindCore(extension.ExtensionActions{}, extension.ContextActions{SessionManager: sessionManager{id: id}}, nil)
	tb.Cleanup(func() { runner.Invalidate("core proof complete") })
	return runner
}

func headers(values map[string]any) extension.ProviderHeaders {
	out := make(extension.ProviderHeaders, len(values))
	for key, value := range values {
		if value == nil {
			out[key] = nil
		} else {
			out[key] = new(value.(string))
		}
	}
	return out
}

func headerValues(values extension.ProviderHeaders) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = nil
		if value != nil {
			out[key] = *value
		}
	}
	return out
}

func TestUnsupportedNativeEventFailsBeforeSetup(t *testing.T) {
	setup := false
	module := api.Module{Name: "unsupported", Core: "1.3", Setup: func(api.Host) error { setup = true; return nil }, Hooks: []api.Hook{{
		Event: api.EventToolCall, Role: api.RoleOwner, Failure: api.FailClosed,
		Handle: func(context.Context, api.Call) (api.Result, error) { return api.Result{}, nil },
	}}}
	err := Attach(nil, module)
	if err == nil || !strings.Contains(err.Error(), `does not support event "tool_call"`) || setup {
		t.Fatalf("Attach: %v, setup=%t", err, setup)
	}
}

func TestNativeSessionIDWithoutManagerOrAfterInvalidation(t *testing.T) {
	runner := inproc.NewRunner(nil, t.TempDir())
	ctx := nativeContext{runner.DispatchContext(t.Context())}
	if _, err := ctx.GetSessionID(); err == nil || !strings.Contains(err.Error(), "no SessionManager") {
		t.Fatal(err)
	}
	runner.Invalidate("core test ended")
	if _, err := ctx.GetSessionID(); err == nil || err.Error() != "core test ended" {
		t.Fatal(err)
	}
}

func TestRealCoreGatewayDirectMatchesFused(t *testing.T) {
	var reference []any
	for _, route := range []string{"fused", "direct"} {
		t.Run(route, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("PIG_HOME", home)
			id := "session-one"
			runner := loadCore(t, route, []api.Module{gateway.Module()}, &id)
			if runner.HasHandlers("session_start") || !runner.HasHandlers("session_shutdown") || !runner.HasHandlers("before_provider_headers") || len(runner.Tools()) != 0 || len(runner.Commands()) != 0 {
				t.Fatal("the real core/gateway capability set changed")
			}
			var observed []any
			for _, input := range []map[string]any{
				{"x-gateway-key": "fixture", "X-Pi-Session": "old", "accept": "json"},
				{"X-GATEWAY-KEY": "fixture"}, {"x-gateway-key": nil}, {"accept": "json"},
			} {
				out, err := runner.EmitBeforeProviderHeaders(t.Context(), headers(input))
				if err != nil {
					t.Fatal(err)
				}
				observed = append(observed, headerValues(out))
			}
			if got := observed[0].(map[string]any); got["x-pi-session"] != "session-one" || got["X-Pi-Session"] != nil {
				t.Fatal(got)
			}
			id = "not-read-until-shutdown"
			out, err := runner.EmitBeforeProviderHeaders(t.Context(), headers(map[string]any{"x-gateway-key": "fixture"}))
			if err != nil || *out["x-pi-session"] != "session-one" {
				t.Fatalf("cache: %v %v", out, err)
			}
			observed = append(observed, headerValues(out))
			if _, err := runner.Emit(t.Context(), extension.SessionShutdownEvent{Type: "session_shutdown", Reason: "quit"}); err != nil {
				t.Fatal(err)
			}
			id = "session-two"
			out, err = runner.EmitBeforeProviderHeaders(t.Context(), headers(map[string]any{"x-gateway-key": "fixture"}))
			if err != nil || *out["x-pi-session"] != "session-two" {
				t.Fatalf("new session: %v %v", out, err)
			}
			observed = append(observed, headerValues(out))
			if _, err := runner.Emit(t.Context(), extension.SessionShutdownEvent{Type: "session_shutdown", Reason: "quit"}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(home, "core", "logs", "core.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var start map[string]any
			if err := json.Unmarshal(data, &start); err != nil {
				t.Fatal(err)
			}
			delete(start, "time")
			observed = append(observed, start)
			if route == "fused" {
				reference = observed
			} else if !reflect.DeepEqual(reference, observed) {
				t.Fatalf("fused=%v direct=%v", reference, observed)
			}
			t.Logf("%s: startup log, six header results, session-ID cache and shutdown/reset agree; no session_start hook, commands, tools or status slots", route)
			if dir := os.Getenv("CORE_PROOF_OUT"); dir != "" {
				wire, err := json.MarshalIndent(observed, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "core-"+route+".json"), wire, 0o600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCoreLifecycleOrderDirectMatchesFused(t *testing.T) {
	for _, route := range []string{"direct", "fused"} {
		t.Run(route, func(t *testing.T) {
			t.Setenv("PIG_HOME", t.TempDir())
			var sequence []string
			probe := api.Module{Name: "lifecycle-probe", Version: "0.1.0", Core: "1.3", Setup: func(api.Host) error { sequence = append(sequence, "setup"); return nil }}
			for _, event := range []api.Event{api.EventSessionStart, api.EventSessionShutdown} {
				probe.Hooks = append(probe.Hooks, api.Hook{Event: event, Role: api.RoleObserver, Failure: api.FailOpen, Handle: func(_ context.Context, call api.Call) (api.Result, error) { return api.Result{}, nil }})
			}
			// Awaited owners make the asserted sequence deterministic; the shared dispatcher tests cover background draining.
			for i := range probe.Hooks {
				probe.Hooks[i].Role = api.RoleOwner
				probe.Hooks[i].Handle = func(_ context.Context, call api.Call) (api.Result, error) {
					sequence = append(sequence, string(call.Event)+":"+call.SessionID)
					return api.Result{}, nil
				}
			}
			id := "session-one"
			runner := loadCore(t, route, []api.Module{gateway.Module(), probe}, &id)
			for _, event := range []extension.ExtensionEvent{extension.SessionStartEvent{Type: "session_start", Reason: "startup"}, extension.SessionShutdownEvent{Type: "session_shutdown", Reason: "quit"}} {
				if _, err := runner.Emit(t.Context(), event); err != nil {
					t.Fatal(err)
				}
			}
			if !slices.Equal(sequence, []string{"setup", "session_start:session-one", "session_shutdown:session-one"}) {
				t.Fatal(sequence)
			}
			t.Logf("ordered lifecycle: %v", sequence)
		})
	}
}
