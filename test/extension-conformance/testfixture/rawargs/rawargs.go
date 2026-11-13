// Package rawargs is the Go extension of the raw tool-argument conformance row: tools whose prepareArguments returns the arguments' JSON text unchanged, other text, an error or nothing, and a tool_call handler that reports the input it reads.
package rawargs

import (
	"errors"
	"strings"
	"sync"

	"github.com/MichaelKinsy/PiG/extensions/sdk"
	"github.com/MichaelKinsy/PiG/extensions/sdk/json"
)

// Extension returns the row's extension under name.
func Extension(name string) *sdk.Extension {
	ext := sdk.New(name)
	// last is the text the last raw hook got; the row dispatches one call at a time.
	var mu sync.Mutex
	var last string
	remember := func(args json.RawMessage) {
		mu.Lock()
		last = string(args)
		mu.Unlock()
	}
	// report is the text the last raw hook got and the arguments the tool executes with.
	report := func(ctx sdk.Context) (any, error) {
		mu.Lock()
		prepared := last
		mu.Unlock()
		return map[string]any{"content": "prepare=" + prepared + " execute=" + string(ctx.ToolInput())}, nil
	}
	object := sdk.Schema{"type": "object"}
	ext.RegisterTool(sdk.ToolDefinition{
		Name: "raw_echo", Label: "raw_echo", Description: "Prepare with a raw hook that returns its input", Parameters: object,
		PrepareArguments: func(args json.RawMessage) (json.RawMessage, error) {
			remember(args)
			return args, nil
		},
		Execute: func(ctx sdk.Context, _ map[string]any) (any, error) { return report(ctx) },
	})
	ext.RegisterTool(sdk.ToolDefinition{
		Name: "raw_replace", Label: "raw_replace", Description: "Prepare with a raw hook that returns other arguments",
		Parameters: sdk.Schema{"type": "object", "required": []string{"text"}, "properties": map[string]any{"text": map[string]any{"type": "string"}}},
		PrepareArguments: func(args json.RawMessage) (json.RawMessage, error) {
			remember(args)
			if strings.Contains(string(args), `"bad"`) {
				return json.RawMessage(`{"text":["not","a","string"]}`), nil
			}
			return json.RawMessage(`{"zz":true,"text":"replaced","aa":[2,1]}`), nil
		},
		Execute: func(ctx sdk.Context, _ map[string]any) (any, error) { return report(ctx) },
	})
	ext.RegisterTool(sdk.ToolDefinition{
		Name: "raw_fail", Label: "raw_fail", Description: "Prepare with a raw hook that fails", Parameters: object,
		PrepareArguments: func(json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("raw prepare refused the arguments")
		},
		Execute: func(sdk.Context, map[string]any) (any, error) { return map[string]any{"content": "executed"}, nil },
	})
	ext.RegisterTool(sdk.ToolDefinition{
		Name: "prepare_nil", Label: "prepare_nil", Description: "Prepare with a hook that returns nothing", Parameters: object,
		PrepareArguments: func(json.RawMessage) (json.RawMessage, error) { return nil, nil },
		Execute:          func(sdk.Context, map[string]any) (any, error) { return map[string]any{"content": "executed"}, nil },
	})
	// The handler blocks calls of tool_call_probe with the input it read as the reason.
	ext.OnEvent("tool_call", func(ctx sdk.Context, data map[string]any) (any, error) {
		if data["toolName"] != "tool_call_probe" {
			return nil, nil
		}
		return map[string]any{"block": true, "reason": "input=" + string(ctx.ToolInput())}, nil
	})
	return ext
}
