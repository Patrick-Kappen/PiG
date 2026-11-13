package extensionconformance

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/factoryload"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
	directdemo "github.com/MichaelKinsy/PiG/examples/extensions/direct-go-demo"
)

type demoBranchEntry struct {
	CustomType string `json:"customType"`
	Data       struct {
		Value string `json:"value"`
	} `json:"data"`
}

// This uses the conformance suite's rawArgsProvider fake, not an authenticated or network provider: its SetFinalArguments preserves the source text that a map-based FauxToolCall cannot carry.
func TestDirectGoDemoThroughSession(t *testing.T) {
	path := os.Getenv("DIRECT_GO_DEMO_TEST_LOG")
	if path == "" {
		path = filepath.Join(t.TempDir(), "observations.jsonl")
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(directdemo.LogEnv, path)
	cwd := t.TempDir()
	runtime := extension.CreateExtensionRuntime()
	loaded, err := factoryload.LoadExtensionFromFactory(directdemo.Extension, cwd, nil, runtime, "builtin:direct-go-demo")
	if err != nil {
		t.Fatal(err)
	}
	runner := inproc.NewRunner([]extension.Extension{loaded}, cwd, runtime)
	services, err := coding.CreateAgentSessionServices(coding.CreateAgentSessionServicesOptions{CWD: cwd, AgentDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(services.Close)
	const arguments = `{"z":2,"x":1,"a":1.25}`
	session, err := coding.NewSession(services, coding.SessionOptions{
		Runner: runner, NoSession: true, SkipBuiltinTools: true,
		Model: &ai.Model{ID: "raw-args", Provider: &rawArgsProvider{name: "direct_echo", arguments: arguments}},
	})
	if err != nil {
		t.Fatal(err)
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for event := range session.Events() {
			coding.AcknowledgeEvent(event)
		}
	}()
	t.Cleanup(func() { _ = session.Close(); <-drained })
	ui := newRecordingUI(&[]string{}, &[]string{})
	if err := session.BindExtensions(t.Context(), coding.ExtensionBindings{UIContext: ui, Mode: extension.ExtensionMode("rpc"), OnError: func(event *extension.ExtensionError) { t.Error(event.Error) }}); err != nil {
		t.Fatal(err)
	}
	if _, ok := session.GetToolDefinition("direct_late"); !ok {
		t.Fatal("runtime tool did not reach Session's tool registry")
	}
	// Exercise the same mode rebind as the CLI, which must not remove api.AppendEntry.
	coding.BindSessionExtensionActions(runner, nil, func() *coding.Session { return session }, extension.ContextActions{}, nil)
	if !runner.ExecuteCommand(t.Context(), "direct-demo", "session-test") {
		t.Fatal("native command did not register")
	}
	messages, err := session.Send(t.Context(), "local fake-provider fixture")
	if err != nil {
		t.Fatal(err)
	}
	resultFound := false
	for _, message := range messages {
		if result := message.ToolResult; result != nil {
			if result.IsError || result.Text() != arguments {
				t.Fatalf("tool result: %+v", result)
			}
			resultFound = true
		}
	}
	if !resultFound {
		t.Fatal("no tool result")
	}
	session.EmitSessionShutdown("quit")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stages []string
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
		var observation struct {
			Stage string          `json:"stage"`
			Data  json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(line, &observation); err != nil {
			t.Fatal(err)
		}
		stages = append(stages, observation.Stage)
		switch observation.Stage {
		case "prepare", "execute":
			var text string
			if err := json.Unmarshal(observation.Data, &text); err != nil || text != arguments {
				t.Fatalf("%s text=%q err=%v", observation.Stage, text, err)
			}
			t.Logf("%s: json.RawMessage, exact text %s", observation.Stage, text)
		case "tool_call", "tool_result":
			var data struct {
				Text  string         `json:"text"`
				Input map[string]any `json:"input"`
			}
			if err := json.Unmarshal(observation.Data, &data); err != nil || data.Text != arguments || data.Input["z"] != 2.0 || data.Input["x"] != 1.0 || data.Input["a"] != 1.25 {
				t.Fatalf("%s data=%+v err=%v", observation.Stage, data, err)
			}
			t.Logf("%s: typed event, Input map (no order); WireInput exact text %s", observation.Stage, data.Text)
		case "command_branch":
			var entries []demoBranchEntry
			if err := json.Unmarshal(observation.Data, &entries); err != nil || !slices.ContainsFunc(entries, func(entry demoBranchEntry) bool {
				return entry.CustomType == "direct-go-demo-state" && entry.Data.Value == "session-test"
			}) {
				t.Fatalf("state not read back: %s (%v)", observation.Data, err)
			}
		}
	}
	for _, stage := range []string{"factory", "session_start", "runtime_tool", "ui_calls", "command_branch", "before_agent_start", "context", "prepare", "tool_call", "execute", "tool_result", "turn_end", "session_shutdown"} {
		if !slices.Contains(stages, stage) {
			t.Errorf("missing %s in %v", stage, stages)
		}
	}
	t.Logf("all native stages reached; observations: %s", path)
}
