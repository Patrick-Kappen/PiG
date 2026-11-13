package extensionconformance

import (
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding"
)

// Pi's _beforeToolCall and emitToolCall pass the parsed object's UTF-16 units to the handler unchanged. Drive the actual Session hooks, not a copy of their decode logic.
func TestToolCallSurrogateInputThroughSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping conformance suite in short mode (builds subprocess fixtures)")
	}
	for _, hc := range rawArgsHarnesses() {
		t.Run(hc.name, func(t *testing.T) {
			for _, tc := range []struct{ name, args, want string }{
				{"high", `{"y":"\ud800"}`, `{"y":"\ud800"}`},
				{"low", `{"y":"\udc00"}`, `{"y":"\udc00"}`},
				{"key", `{"\ud800":"key"}`, `{"\ud800":"key"}`},
				{"nested", `{"nested":{"y":"\ud800"}}`, `{"nested":{"y":"\ud800"}}`},
				{"reversed", `{"y":"\udc00\ud800"}`, `{"y":"\udc00\ud800"}`},
				{"pair", `{"y":"\ud83d\ude00"}`, `{"y":"😀"}`},
				{"replacement", `{"y":"�"}`, `{"y":"�"}`},
				{"ordinary", `{"z":2,"y":"text"}`, `{"z":2,"y":"text"}`},
				{"empty", `{}`, `{}`},
			} {
				for _, toolName := range []string{"raw_event_plain", "raw_event_echo", "raw_event_edit"} {
					t.Run(tc.name+"/"+toolName, func(t *testing.T) {
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
						session, err := coding.NewSession(services, coding.SessionOptions{NoSession: true, SkipBuiltinTools: true, Runner: h.runner, Tools: tools, Model: &ai.Model{ID: "surrogate", Provider: &rawArgsProvider{name: toolName, arguments: tc.args}}})
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
						prepared := tc.want
						if toolName == "raw_event_plain" {
							prepared = ""
						}
						executed := tc.want
						if toolName == "raw_event_edit" {
							if tc.want == "{}" {
								executed = `{"added":true}`
							} else {
								executed = strings.TrimSuffix(tc.want, "}") + `,"added":true}`
							}
						}
						want := "prepare=" + prepared + " tool_call=" + tc.want + " execute=" + executed
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
						if len(results) != 1 || results[0] != want {
							t.Errorf("result=%q, want [%s]", results, want)
						}
					})
				}
			}
		})
	}
}
