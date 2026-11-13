package extensionconformance

import (
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding"
)

// Pi's agent-loop prepareToolCall/finalizeExecutedToolCall and runner.emitToolResult pass the validated, prepared arguments, including tool_call edits, to tool_result. Drive the actual Session hooks and bridges without a model service.
func TestToolResultInputThroughSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping conformance suite in short mode (builds subprocess fixtures)")
	}
	for _, hc := range rawArgsHarnesses() {
		t.Run(hc.name, func(t *testing.T) {
			for _, tc := range []struct{ name, args, want string }{
				{"plain", `{"z":2,"10":"ten","2":"two","nested":{"y":"\ud800","b":1},"a":1.25}`, `{"2":"two","10":"ten","z":2,"nested":{"y":"\ud800","b":1},"a":1.25}`},
				{"plain", `{}`, `{}`},
				{"prepare", `{"z":2,"a":1}`, `{"zz":true,"nested":{"y":"\ud800","b":2},"aa":[2,1]}`},
				{"edit", `{"z":2,"10":"ten","2":"two","nested":{"y":"text","b":1},"a":1.25}`, `{"2":"two","10":"ten","nested":{"y":"text","b":1},"a":1.25,"added":true}`},
			} {
				t.Run(tc.name+"/"+tc.args, func(t *testing.T) {
					h := hc.make(t)
					t.Cleanup(func() { h.host.Shutdown("test done") })
					tools, errs := coding.BridgeNewRunnerTools(h.runner.Tools())
					if len(errs) != 0 {
						t.Fatal(errs)
					}
					services, err := coding.CreateAgentSessionServices(coding.CreateAgentSessionServicesOptions{CWD: t.TempDir(), AgentDir: t.TempDir()})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(services.Close)
					session, err := coding.NewSession(services, coding.SessionOptions{NoSession: true, SkipBuiltinTools: true, Runner: h.runner, Tools: tools, Model: &ai.Model{ID: "result-input", Provider: &rawArgsProvider{name: "result_" + tc.name, arguments: tc.args}}})
					if err != nil {
						t.Fatal(err)
					}
					done := make(chan struct{})
					go func() {
						defer close(done)
						for e := range session.Events() {
							coding.AcknowledgeEvent(e)
						}
					}()
					t.Cleanup(func() { _ = session.Close(); <-done })
					messages, err := session.Send(t.Context(), "go")
					if err != nil {
						t.Fatal(err)
					}
					var results []string
					for _, m := range messages {
						if m.ToolResult != nil {
							if m.ToolResult.IsError {
								t.Errorf("tool failed: %s", m.ToolResult.Text())
							}
							results = append(results, m.ToolResult.Text())
						}
					}
					t.Logf("%s => %q", tc.args, results)
					if len(results) != 1 || results[0] != "input="+tc.want {
						t.Errorf("result=%q, want [input=%s]", results, tc.want)
					}
				})
			}
		})
	}
}
