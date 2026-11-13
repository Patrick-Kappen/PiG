package coding

import (
	"encoding/json"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
)

type surrogatePreparingTool struct {
	probeTool
	prepared []string
}

func (p *surrogatePreparingTool) PrepareArguments(args json.RawMessage) (json.RawMessage, error) {
	p.prepared = append(p.prepared, string(args))
	return args, nil
}

// Pi's JSON.parse and agent-session.ts _beforeToolCall preserve UTF-16 code units in event.input; runner.ts emitToolCall passes that same object to every handler. JSON.stringify escapes unpaired halves and joins a valid pair.
func TestSessionToolCallPreservesSurrogateInput(t *testing.T) {
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
		t.Run(tc.name, func(t *testing.T) {
			tool := &surrogatePreparingTool{}
			var seen []string
			ext := toolCallExtension(func(args ...any) (any, error) {
				e := args[0].(extension.CustomToolCallEvent)
				raw, err := e.InputJSON()
				if err != nil {
					return nil, err
				}
				seen = append(seen, string(raw))
				return nil, nil
			})
			response := func([]ai.Message) *ai.AssistantMessage {
				call := ai.ToolCall{ID: "surrogate", Name: "probe"}
				call.SetFinalArguments(tc.args)
				return &ai.AssistantMessage{Content: []ai.AssistantContentBlock{call}, Provider: "faux", Model: "faux-1", StopReason: ai.StopReasonToolUse}
			}
			h := newRecoveryHarness(t, harnessOptions{tools: []agent.AgentTool{tool}, extension: ext}, response, fauxReply("done", ai.StopReasonStop, 0))
			if _, err := h.session.Send(t.Context(), "go"); err != nil {
				t.Fatal(err)
			}
			t.Logf("prepare=%q tool_call=%q execute=%q", tool.prepared, seen, tool.executions())
			for label, values := range map[string][]string{"prepare": tool.prepared, "tool_call": seen, "execute": tool.executions()} {
				if len(values) != 1 || values[0] != tc.want {
					t.Errorf("%s=%q, want [%s]", label, values, tc.want)
				}
			}
		})
	}
}

func BenchmarkSessionToolCallInput(b *testing.B) {
	for _, tc := range []struct{ name, args string }{
		{"ordinary", `{"z":2,"y":"text","nested":{"a":[1,2,3]}}`},
		{"surrogate", `{"z":2,"y":"\ud800","nested":{"\udc00":[1,2,3]}}`},
	} {
		b.Run(tc.name, func(b *testing.B) {
			runner := inproc.NewRunner([]extension.Extension{toolCallExtension(func(...any) (any, error) { return nil, nil })}, b.TempDir())
			b.Cleanup(func() { runner.Invalidate("benchmark done") })
			session := &Session{runner: runner}
			args := json.RawMessage(tc.args)
			b.ReportAllocs()
			for b.Loop() {
				if result := session.toolCallHook(b.Context(), "call", "", "probe", args); result.Err != nil {
					b.Fatal(result.Err)
				}
			}
		})
	}
}

// Changing a lone half to a literal replacement character is a real edit, not an unchanged input.
func TestSessionToolCallSurrogateReplacementIsAnEdit(t *testing.T) {
	tool := &probeTool{}
	ext := toolCallExtension(func(args ...any) (any, error) {
		args[0].(extension.CustomToolCallEvent).Input["y"] = "�"
		return nil, nil
	})
	response := func([]ai.Message) *ai.AssistantMessage {
		call := ai.ToolCall{ID: "surrogate", Name: "probe"}
		call.SetFinalArguments(`{"y":"\ud800"}`)
		return &ai.AssistantMessage{Content: []ai.AssistantContentBlock{call}, Provider: "faux", Model: "faux-1", StopReason: ai.StopReasonToolUse}
	}
	h := newRecoveryHarness(t, harnessOptions{tools: []agent.AgentTool{tool}, extension: ext}, response, fauxReply("done", ai.StopReasonStop, 0))
	if _, err := h.session.Send(t.Context(), "go"); err != nil {
		t.Fatal(err)
	}
	if got := tool.executions(); len(got) != 1 || got[0] != `{"y":"�"}` {
		t.Fatalf("execute=%q, want replacement character", got)
	}
}
