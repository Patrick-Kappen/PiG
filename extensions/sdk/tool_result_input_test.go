package sdk

import (
	"testing"

	"github.com/MichaelKinsy/PiG/extensions/sdk/json"
)

func BenchmarkToolResultInput(b *testing.B) {
	input := json.RawMessage(`{"2":"two","10":"ten","z":2,"nested":{"y":"\ud800","b":[{"q":null,"c":1}]},"a":1.25}`)
	ctx := Context{toolInput: &input}
	b.ReportAllocs()
	for b.Loop() {
		if len(ctx.ToolInput()) == 0 || ctx.ToolParams() == nil {
			b.Fatal("missing input")
		}
	}
}

// runner.emitToolResult passes the prepared input object to handlers. Reading it must not round-trip through an unordered map or replace lone UTF-16 halves.
func TestToolResultHandlerReadsOrderedInputAndParams(t *testing.T) {
	ext := New("result-input")
	ext.OnToolResult(func(ctx Context, _ map[string]any) (any, error) {
		params := ctx.ToolParams()
		if params == nil {
			return map[string]any{"input": string(ctx.ToolInput()), "params": ""}, nil
		}
		encoded, err := json.Marshal(params)
		return map[string]any{"input": string(ctx.ToolInput()), "params": string(encoded)}, err
	})
	host, _, done := surfaceHost(t, ext, nil)
	defer surfaceShutdown(t, host, done)
	for _, tc := range []struct{ input, want string }{
		{`{"z":1,"10":10,"2":2,"a":{"y":"\ud800","b":[{"q":null,"c":1}]}}`, `{"2":2,"10":10,"z":1,"a":{"y":"\ud800","b":[{"q":null,"c":1}]}}`},
		{`{}`, `{}`},
		{`null`, ``},
	} {
		_, resp := runSurfaceRequest(t, host, "result-"+tc.input, &requestMsg{Method: "event", Event: "tool_result", HandlerID: 1, Args: json.RawMessage(`{"input":` + tc.input + `}`)}, nil)
		if resp.Error != nil {
			t.Fatal(resp.Error)
		}
		var got struct{ Input, Params string }
		if err := json.Unmarshal(resp.Result, &got); err != nil {
			t.Fatal(err)
		}
		wantInput := tc.want
		if tc.input == "null" {
			wantInput = "null"
		}
		if got.Input != wantInput || got.Params != tc.want {
			t.Errorf("%s => %+v, want input %q params %q", tc.input, got, wantInput, tc.want)
		}
	}
}
