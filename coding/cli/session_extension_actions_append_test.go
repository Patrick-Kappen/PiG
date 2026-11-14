package cli

import (
	"encoding/json"
	"testing"

	"github.com/MichaelKinsy/PiG/coding"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
)

// agent-session.ts _bindExtensionCore binds appendEntry for every mode, not only subprocess peers.
func TestSessionExtensionActionsKeepNativeAppendEntryAfterModeBind(t *testing.T) {
	services, err := coding.CreateAgentSessionServices(coding.CreateAgentSessionServicesOptions{CWD: t.TempDir(), AgentDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(services.Close)
	runtime := extension.CreateExtensionRuntime()
	runner := inproc.NewRunner(nil, services.CWD(), runtime)
	session, err := coding.NewSession(services, coding.SessionOptions{Runner: runner, NoSession: true, SkipBuiltinTools: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	coding.BindSessionExtensionActions(runner, nil, func() *coding.Session { return session }, extension.ContextActions{}, nil)
	if runtime.AppendEntry == nil {
		t.Fatal("mode bind removed native runtime.AppendEntry")
	}
	if err := runtime.AppendEntry("native-mode-state", map[string]any{"value": "kept"}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range session.Entries() {
		var value struct {
			CustomType string `json:"customType"`
			Data       struct {
				Value string `json:"value"`
			} `json:"data"`
		}
		if err := json.Unmarshal(entry.Raw(), &value); err != nil {
			t.Fatal(err)
		}
		if value.CustomType == "native-mode-state" && value.Data.Value == "kept" {
			return
		}
	}
	t.Fatal("native append did not reach the Session")
}
