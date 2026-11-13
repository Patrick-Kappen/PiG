package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
)

// argumentsTextProvider answers the first request with one tool call whose complete arguments text is arguments, as a provider's stream finishes it (Pi's parseToolCallArguments), and the next with text.
type argumentsTextProvider struct {
	arguments string
	calls     int
}

func (p *argumentsTextProvider) ID() string   { return "arguments-text" }
func (p *argumentsTextProvider) Close() error { return nil }
func (p *argumentsTextProvider) Stream(context.Context, ai.TranscriptContext, ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	p.calls++
	message := ai.AssistantMessage{StopReason: ai.StopReasonStop, Content: []ai.AssistantContentBlock{ai.TextContent{Text: "done"}}}
	if p.calls == 1 {
		call := ai.ToolCall{ID: "call-1", Name: "open"}
		call.SetFinalArguments(p.arguments)
		message = ai.AssistantMessage{StopReason: ai.StopReasonToolUse, Content: []ai.AssistantContentBlock{call}}
	}
	stream := ai.NewAssistantMessageEventStream()
	go func() { _ = stream.Push(ai.DoneEvent{Reason: message.StopReason, Message: &message}) }()
	return stream, nil
}

// openSchemaTool accepts any object and records what its hook and Execute got.
type openSchemaTool struct {
	prepared, executed []string
}

func (t *openSchemaTool) Name() string  { return "open" }
func (t *openSchemaTool) Label() string { return "open" }
func (t *openSchemaTool) Schema() ai.ToolSchema {
	return ai.ToolSchema{Name: "open", Parameters: map[string]any{"type": "object"}}
}
func (t *openSchemaTool) ExecutionMode() ToolExecutionMode { return ToolModeSequential }
func (t *openSchemaTool) Execute(_ context.Context, _ string, params json.RawMessage, _ ToolUpdateCallback) (AgentToolResult, error) {
	t.executed = append(t.executed, string(params))
	return AgentToolResult{Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "ok"}}}, nil
}

// preparingTool is openSchemaTool with a prepare hook that replaces the arguments with {"a":1}.
type preparingTool struct{ openSchemaTool }

func (t *preparingTool) PrepareArguments(raw json.RawMessage) (json.RawMessage, error) {
	t.prepared = append(t.prepared, string(raw))
	return json.RawMessage(`{"a":1}`), nil
}

// Pi 1.5.0 parseToolCallArguments (pi-ai utils/json-parse.ts) gives a finished call whose text is not a JSON object arguments {} and an argumentsError; agent-loop.ts prepareToolCall still runs tool.prepareArguments({}), but its result keeps the call's argumentsError, and validateToolArguments (pi-ai utils/validation.ts) refuses the call with `Invalid arguments for tool "<name>": <argumentsError>` before execute. Empty or blank text is {} without an error and runs.
func TestToolCallWithArgumentsErrorIsRefusedBeforeExecute(t *testing.T) {
	refused := func(error, text string) string {
		return "Invalid arguments for tool \"open\": " + error + "\n\nReceived arguments:\n" + text
	}
	const notObject = "Tool arguments must be a JSON object"
	for _, tc := range []struct {
		arguments  string
		want       string // tool result text
		isError    bool
		executed   string // Execute's arguments without a hook; "" when it must not run
		hookResult bool   // with the hook, Execute runs with the hook's {"a":1}
	}{
		{"[]", refused(notObject, "[]"), true, "", false},
		{"42", refused(notObject, "42"), true, "", false},
		{`"text"`, refused(notObject, `"text"`), true, "", false},
		{"null", refused(notObject, "null"), true, "", false},
		{`{"a":`, refused("Unexpected end of JSON input", `{"a":`), true, "", false},
		{"", "ok", false, "{}", true},
		{"   ", "ok", false, "{}", true},
		{`{"a":1}`, "ok", false, `{"a":1}`, true},
	} {
		for _, hook := range []bool{false, true} {
			var tool AgentTool
			var record *openSchemaTool
			if hook {
				p := &preparingTool{}
				tool, record = p, &p.openSchemaTool
			} else {
				o := &openSchemaTool{}
				tool, record = o, o
			}
			a := mustNewAgent(AgentOptions{Model: &ai.Model{ID: "m", Provider: &argumentsTextProvider{arguments: tc.arguments}}, Tools: []AgentTool{tool}})
			messages, err := a.Send(t.Context(), "go")
			if err != nil {
				t.Fatal(err)
			}
			var result *ToolResultMessage
			for _, message := range messages {
				if message.ToolResult != nil {
					result = message.ToolResult
				}
			}
			if result == nil {
				t.Fatalf("%q hook=%t: no tool result", tc.arguments, hook)
			}
			t.Logf("%q hook=%t: isError=%t model gets %q, prepare got %q, execute got %q", tc.arguments, hook, result.IsError, result.Text(), record.prepared, record.executed)
			if result.Text() != tc.want || result.IsError != tc.isError {
				t.Errorf("%q hook=%t: result %q (error %t), want %q (error %t)", tc.arguments, hook, result.Text(), result.IsError, tc.want, tc.isError)
			}
			wantExecuted := tc.executed
			if hook && tc.hookResult {
				wantExecuted = `{"a":1}`
			}
			switch {
			case wantExecuted == "" && len(record.executed) != 0:
				t.Errorf("%q hook=%t: Execute ran with %q, want no execution", tc.arguments, hook, record.executed)
			case wantExecuted != "" && (len(record.executed) != 1 || record.executed[0] != wantExecuted):
				t.Errorf("%q hook=%t: Execute got %q, want %q", tc.arguments, hook, record.executed, wantExecuted)
			}
			// Pi runs prepareArguments with the call's arguments, {} for a refused text, before validation refuses it.
			if hook && (len(record.prepared) != 1 || (tc.isError && record.prepared[0] != "{}")) {
				t.Errorf("%q hook=%t: prepare got %q, want one call with {} for a refused text", tc.arguments, hook, record.prepared)
			}
		}
	}
}
