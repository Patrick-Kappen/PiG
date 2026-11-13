package extensionconformance

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/factoryload"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
)

func assertDirectUserBashRawReturnDoesNotCompile(t *testing.T) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mod := "module direct-invalid-result\n\ngo 1.26.0\nrequire github.com/MichaelKinsy/PiG v0.0.0\nreplace github.com/MichaelKinsy/PiG => " + filepath.ToSlash(root) + "\nreplace github.com/MichaelKinsy/PiG/extensions/sdk => " + filepath.ToSlash(filepath.Join(root, "extensions", "sdk")) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o600); err != nil {
		t.Fatal(err)
	}
	source := `package invalid
import ("context"; "encoding/json"; "github.com/MichaelKinsy/PiG/coding/extension")
func DirectExtension(api extension.API) error {
 api.OnUserBash(func(context.Context, extension.UserBashEvent) (extension.UserBashEventResult, error) {
  return json.RawMessage("{\"operations\":null}"), nil
 })
 return nil
}
`
	if err := os.WriteFile(filepath.Join(dir, "invalid.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "-trimpath", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "cannot use json.RawMessage") || !strings.Contains(string(output), "as extension.UserBashEventResult") {
		t.Fatalf("expected native callback type rejection, got %v: %s", err, output)
	}
	t.Logf("native malformed raw result is rejected at compilation: %s", output)
}

// makeDirectGoHarness registers the same native reference fixture through the production factory API instead of constructing the Runner's record directly. Piglet builder tests prove generated main-package linkage separately.
func makeDirectGoHarness(t *testing.T) *harness {
	t.Helper()
	return makeDirectGoHarnessWithAPIProbes(t, false)
}

func makeAPIProbeHarness(t *testing.T, tc harnessCase) *harness {
	t.Helper()
	if tc.name == "direct-go" {
		return makeDirectGoHarnessWithAPIProbes(t, true)
	}
	return tc.make(t)
}

func makeDirectGoHarnessWithAPIProbes(t *testing.T, withProbes bool) *harness {
	t.Helper()
	notify, status, actions := &[]string{}, &[]string{}, &[]string{}
	ui := newRecordingUI(notify, status)
	fixture := makeInprocFixture(ui, actions)
	runtime := extension.CreateExtensionRuntime()
	var probe *directStateProbe
	if withProbes {
		probe = newDirectStateProbe(t)
	}
	loaded, err := factoryload.LoadExtensionFromFactory(func(api extension.API) error {
		if withProbes {
			registerDirectAPIProbes(api, ui, probe)
		}
		for _, name := range slices.Sorted(maps.Keys(fixture.Tools)) {
			api.RegisterTool(fixture.Tools[name].Definition)
		}
		for _, name := range slices.Sorted(maps.Keys(fixture.Commands)) {
			c := fixture.Commands[name]
			api.RegisterCommand(name, extension.CommandOptions{Description: c.Description, Handler: c.Handler, GetArgumentCompletions: c.GetArgumentCompletions})
		}
		for _, name := range slices.Sorted(maps.Keys(fixture.Flags)) {
			f := fixture.Flags[name]
			api.RegisterFlag(name, extension.FlagOptions{Type: f.Type, Default: f.Default, Description: f.Description})
		}
		for name, renderer := range fixture.MessageRenderers {
			api.RegisterMessageRenderer(name, renderer)
		}
		for name, renderer := range fixture.EntryRenderers {
			api.RegisterEntryRenderer(name, renderer)
		}
		for _, renderer := range fixture.ToolRenderers {
			api.RegisterToolRenderer(renderer)
		}
		api.RegisterMarkdownTransformer(fixture.MarkdownTransformer)
		for _, shortcut := range fixture.RegisteredShortcuts() {
			api.RegisterShortcut(shortcut.Shortcut, extension.ShortcutOptions{Description: shortcut.Description, Handler: shortcut.Handler})
		}
		methods := map[string]string{
			"user_bash": "OnUserBash", "tool_execution_update": "OnToolExecutionUpdate", "tool_execution_end": "OnToolExecutionEnd",
			"tool_call": "OnToolCall", "tool_result": "OnToolResult", "project_trust": "OnProjectTrust",
			"ui_prompt_start": "OnUiPromptStart", "ui_prompt_end": "OnUiPromptEnd", "cache_warming_decision": "OnCacheWarmingDecision",
			"agent_before_settle": "OnAgentBeforeSettle", "session_start": "OnSessionStart", "session_shutdown": "OnSessionShutdown",
			"after_provider_response": "OnAfterProviderResponse", "session_info_changed": "OnSessionInfoChanged", "session_before_compact": "OnSessionBeforeCompact",
			"session_compact": "OnSessionCompact", "session_compact_failed": "OnSessionCompactFailed", "turn_end": "OnTurnEnd",
		}
		for name, handlers := range fixture.Handlers {
			method := reflect.ValueOf(api).MethodByName(methods[name])
			if !method.IsValid() {
				t.Fatalf("fixture event %s lacks a native API binding", name)
			}
			signature := method.Type().In(0)
			for _, handler := range handlers {
				// Only the test fixture stores erased handlers. The production API receives its actual typed callback and owns the registration.
				callback := reflect.MakeFunc(signature, func(args []reflect.Value) []reflect.Value {
					call := []any{args[1].Interface(), args[0].Interface()}
					if len(args) > 2 {
						call = append(call, args[2].Interface())
					}
					result, err := handler(call...)
					values := make([]reflect.Value, signature.NumOut())
					for i := range values {
						values[i] = reflect.Zero(signature.Out(i))
					}
					if result != nil && len(values) == 2 {
						value := reflect.ValueOf(result)
						if value.Kind() == reflect.Pointer && !value.Type().AssignableTo(signature.Out(0)) {
							value = value.Elem()
						}
						if !value.IsValid() || !value.Type().AssignableTo(signature.Out(0)) {
							values[len(values)-1] = reflect.ValueOf(fmt.Errorf("native conformance fixture cannot return %T as %s", result, signature.Out(0)))
							return values
						}
						values[0] = value
					}
					if err != nil {
						values[len(values)-1] = reflect.ValueOf(err)
					}
					return values
				})
				method.Call([]reflect.Value{callback})
			}
		}
		return nil
	}, t.TempDir(), nil, runtime, inprocFixtureName)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Name = fixture.Name
	runner := inproc.NewRunner([]extension.Extension{loaded}, t.TempDir(), runtime)
	runner.SetUIContext(ui, extension.ExtensionMode(conformanceMode))
	options := conformanceSystemPromptOptions()
	var coreActions extension.ExtensionActions
	if withProbes {
		coreActions.GetCommands = func() []extension.SlashCommandInfo {
			return []extension.SlashCommandInfo{{Name: "conformance-listed", Source: "prompt", Description: "Listed by the host"}}
		}
	}
	runner.BindCore(coreActions, extension.ContextActions{GetSystemPromptOptions: func() *extension.BuildSystemPromptOptions { return &options }, IsProjectTrusted: func() bool { return conformanceProjectTrusted }}, nil)
	return &harness{runner: runner, notify: notify, status: status, actions: actions, ui: ui, directProbe: probe, cleanup: func() { runner.Invalidate("direct conformance complete") }}
}
