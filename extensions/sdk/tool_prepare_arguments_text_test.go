package sdk

import (
	"errors"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/extensions/sdk/json"
)

// rawArgumentInputs are argument texts a map loses: member order, an explicit null, a lone surrogate half, an integer past 2^53, and a float.
var rawArgumentInputs = []string{
	`{"z":2,"x":1,"a":1.25}`,
	`{"b":null,"a":{"y":[1,{"d":0,"c":null}],"x":"s"}}`,
	`{"s":"\ud800","t":"a\u2028b"}`,
	`{"n":9007199254740993,"m":-9007199254740993}`,
	`{"f":1.25,"g":0.30000000000000004,"h":1e-7}`,
}

// Upstream agent-loop.ts:707-716 hands prepareArguments the object the model's arguments parsed to, in their member order. The hook gets the host's text byte for byte and its answer goes back to the host unchanged.
func TestToolPrepareArgumentsGetsAndAnswersTheHostsBytes(t *testing.T) {
	ext := New("prepare-text")
	var seen []string
	ext.RegisterTool(ToolDefinition{
		Name: "raw", Label: "raw", Description: "Raw", Parameters: Schema{"type": "object"},
		PrepareArguments: func(args json.RawMessage) (json.RawMessage, error) {
			seen = append(seen, string(args))
			switch {
			case strings.Contains(string(args), `"fail"`):
				return nil, errors.New("raw prepare exploded")
			case strings.Contains(string(args), `"invalid"`):
				return json.RawMessage(`{"a":`), nil
			case strings.Contains(string(args), `"nil"`):
				return nil, nil
			case strings.Contains(string(args), `"empty"`):
				return json.RawMessage{}, nil
			case strings.Contains(string(args), `"replace"`):
				return json.RawMessage(`{"q":1,"p":[2,1]}`), nil
			}
			return args, nil
		},
		Execute: func(Context, map[string]any) (any, error) { return "ok", nil },
	})
	host, reg, done := surfaceHost(t, ext, nil)
	defer surfaceShutdown(t, host, done)
	if !reg.Tools[0].PreparesArguments {
		t.Fatalf("declaration = %+v, want prepares_arguments", reg.Tools[0])
	}

	for i, input := range rawArgumentInputs {
		_, resp := runSurfaceRequest(t, host, "raw-"+string(rune('a'+i)), &requestMsg{Method: "tool_prepare_arguments", Tool: "raw", Args: json.RawMessage(input)}, nil)
		if resp.Error != nil || string(resp.Result) != input {
			t.Errorf("no-op raw hook answered %s, %+v; want %s", resp.Result, resp.Error, input)
		}
		if got := seen[len(seen)-1]; got != input {
			t.Errorf("raw hook got %s, want %s", got, input)
		}
	}
	_, resp := runSurfaceRequest(t, host, "raw-replace", &requestMsg{Method: "tool_prepare_arguments", Tool: "raw", Args: json.RawMessage(`{"replace":true}`)}, nil)
	if resp.Error != nil || string(resp.Result) != `{"q":1,"p":[2,1]}` {
		t.Errorf("replacing raw hook answered %s, %+v", resp.Result, resp.Error)
	}
	_, resp = runSurfaceRequest(t, host, "raw-fail", &requestMsg{Method: "tool_prepare_arguments", Tool: "raw", Args: json.RawMessage(`{"fail":true}`)}, nil)
	if resp.Error == nil || resp.Error.Message != "raw prepare exploded" || resp.Result != nil {
		t.Errorf("failing raw hook answered %s, %+v; want the hook's error", resp.Result, resp.Error)
	}
	_, resp = runSurfaceRequest(t, host, "raw-invalid", &requestMsg{Method: "tool_prepare_arguments", Tool: "raw", Args: json.RawMessage(`{"invalid":true}`)}, nil)
	if resp.Error == nil || resp.Error.Message != "prepareArguments returned invalid JSON" {
		t.Errorf("invalid raw answer = %s, %+v; want an error", resp.Result, resp.Error)
	}
	// A hook that returns nothing answers null, as a nil map did before the hook took text; the host's validation then rejects the call.
	for _, input := range []string{`{"nil":true}`, `{"empty":true}`} {
		_, resp = runSurfaceRequest(t, host, "raw-"+input, &requestMsg{Method: "tool_prepare_arguments", Tool: "raw", Args: json.RawMessage(input)}, nil)
		if resp.Error != nil || string(resp.Result) != "null" {
			t.Errorf("hook returning nothing for %s answered %s, %+v; want null", input, resp.Result, resp.Error)
		}
	}
}

// A tool_call or tool_result event handler reads the event's input as text in the order the host wrote it, as a JavaScript handler reads event.input.
func TestToolCallEventHandlerReadsTheInputInOrder(t *testing.T) {
	ext := New("tool-call-input")
	inputs := make(chan string, 2)
	ext.OnEvent("tool_call", func(ctx Context, _ map[string]any) (any, error) {
		inputs <- string(ctx.ToolInput())
		return nil, nil
	})
	ext.OnEvent("tool_result", func(ctx Context, _ map[string]any) (any, error) {
		inputs <- string(ctx.ToolInput())
		return nil, nil
	})
	host, _, done := surfaceHost(t, ext, nil)
	defer surfaceShutdown(t, host, done)
	input := `{"z":2,"x":1,"a":1.25,"o":{"y":null,"b":"\ud800"}}`
	_, resp := runSurfaceRequest(t, host, "event-1", &requestMsg{Method: "event", Event: "tool_call", HandlerID: 1, Args: json.RawMessage(`{"type":"tool_call","toolName":"t","toolCallId":"c","input":` + input + `}`)}, nil)
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if got := <-inputs; got != input {
		t.Errorf("tool_call ToolInput = %s, want %s", got, input)
	}
	_, resp = runSurfaceRequest(t, host, "event-2", &requestMsg{Method: "event", Event: "tool_result", HandlerID: 2, Args: json.RawMessage(`{"type":"tool_result","input":` + input + `}`)}, nil)
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if got := <-inputs; got != input {
		t.Errorf("tool_result ToolInput = %s, want %s", got, input)
	}
}

// Upstream agent-loop.ts prepareToolCallArguments: a prepareArguments hook that returns its input leaves the call unchanged, so the SDK answers the host the bytes it sent, in the order the model wrote the members (a map would have sorted them).
func TestToolPrepareArgumentsReturningItsInputAnswersTheSentBytes(t *testing.T) {
	ext := New("prepare-identity")
	ext.RegisterTool(ToolDefinition{
		Name: "p", Label: "p", Description: "p", Parameters: Schema{"type": "object"},
		PrepareArguments: func(args json.RawMessage) (json.RawMessage, error) { return args, nil },
		Execute:          func(Context, map[string]any) (any, error) { return "ok", nil },
	})
	host, _, done := surfaceHost(t, ext, nil)
	defer surfaceShutdown(t, host, done)
	const input = `{"z":2,"x":1,"a":1.25}`
	_, resp := runSurfaceRequest(t, host, "identity", &requestMsg{Method: "tool_prepare_arguments", Tool: "p", Args: json.RawMessage(input)}, nil)
	t.Logf("sent %s, SDK answered %s", input, resp.Result)
	if string(resp.Result) != input {
		t.Fatalf("SDK answered %s, want %s", resp.Result, input)
	}
}
