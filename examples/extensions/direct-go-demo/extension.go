// Package directdemo is a compiled-in extension that records native API calls.
package directdemo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
)

// LogEnv names the append-only JSONL observation file. JSON here is diagnostic output, not extension transport.
const LogEnv = "PIG_DIRECT_GO_DEMO_LOG"

// Extension registers against the host's native API, without the Go extension SDK.
func Extension(api extension.API) error {
	path := os.Getenv(LogEnv)
	if path == "" {
		return errors.New("PIG_DIRECT_GO_DEMO_LOG is required")
	}
	var mu sync.Mutex
	record := func(stage string, data any) error {
		mu.Lock()
		defer mu.Unlock()
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		err = json.NewEncoder(file).Encode(struct {
			Stage string `json:"stage"`
			Data  any    `json:"data"`
		}{stage, data})
		return errors.Join(err, file.Close())
	}
	tool := func(name string) extension.ToolDefinition {
		return extension.ToolDefinition{
			Name: name, Label: name, Description: "Return the native tool argument text.",
			Parameters: json.RawMessage(`{"type":"object","additionalProperties":true}`),
			PrepareArguments: func(args json.RawMessage) (json.RawMessage, error) {
				return args, record("prepare", string(args))
			},
			Execute: func(_ context.Context, _ string, args json.RawMessage, _ extension.AgentToolUpdateCallback) (extension.AgentToolResult, error) {
				return extension.AgentToolResult{Content: []ai.ToolResultMessageContent{ai.TextContent{Text: string(args)}}}, record("execute", string(args))
			},
		}
	}
	api.RegisterTool(tool("direct_echo"))
	api.OnSessionStart(func(ctx context.Context, event extension.SessionStartEvent) error {
		if err := record("session_start", event); err != nil {
			return err
		}
		api.RegisterTool(tool("direct_late"))
		if err := record("runtime_tool", "direct_late"); err != nil {
			return err
		}
		ui, err := extension.FromContext(ctx).UI()
		if err != nil {
			return err
		}
		ui.Notify("Direct Go extension loaded", "info")
		ui.SetStatus("direct-go-demo", "native loaded")
		return record("ui_calls", []string{"notify:Direct Go extension loaded", "status:direct-go-demo:native loaded"})
	})
	api.OnBeforeAgentStart(func(_ context.Context, event extension.BeforeAgentStartEvent) (extension.BeforeAgentStartEventResult, error) {
		return extension.BeforeAgentStartEventResult{}, record("before_agent_start", event)
	})
	api.OnContext(func(_ context.Context, event extension.ContextEvent) (extension.ContextEventResult, error) {
		return extension.ContextEventResult{}, record("context", event)
	})
	api.OnToolCall(func(_ context.Context, event extension.ToolCallEvent) (extension.ToolCallEventResult, error) {
		custom, ok := event.(extension.CustomToolCallEvent)
		if !ok {
			return extension.ToolCallEventResult{}, errors.New("expected CustomToolCallEvent")
		}
		raw, err := custom.InputJSON()
		if err != nil {
			return extension.ToolCallEventResult{}, err
		}
		return extension.ToolCallEventResult{}, record("tool_call", struct {
			Input map[string]any `json:"input"`
			Text  string         `json:"text"`
		}{custom.Input, string(raw)})
	})
	api.OnToolResult(func(_ context.Context, event extension.ToolResultEvent) (extension.ToolResultEventResult, error) {
		custom, ok := event.(extension.CustomToolResultEvent)
		if !ok {
			return extension.ToolResultEventResult{}, errors.New("expected CustomToolResultEvent")
		}
		return extension.ToolResultEventResult{}, record("tool_result", struct {
			Input map[string]any `json:"input"`
			Text  string         `json:"text"`
		}{custom.Input, string(custom.WireInput)})
	})
	api.OnTurnEnd(func(_ context.Context, event extension.TurnEndEvent) (extension.TurnEndEventResult, error) {
		return extension.TurnEndEventResult{}, record("turn_end", event)
	})
	api.OnSessionShutdown(func(_ context.Context, event extension.SessionShutdownEvent) error {
		return record("session_shutdown", event)
	})
	api.RegisterCommand("direct-demo", extension.CommandOptions{
		Description: "Append native extension state and read the branch without a model turn.",
		Handler: func(ctx context.Context, args string) error {
			api.AppendEntry("direct-go-demo-state", map[string]any{"value": args})
			manager, err := extension.FromContext(ctx).SessionManager()
			if err != nil {
				return err
			}
			if manager == nil {
				return errors.New("session manager is not bound")
			}
			branch := manager.GetBranch()
			raw := make([]json.RawMessage, len(branch))
			for i, entry := range branch {
				raw[i] = entry.Raw()
			}
			return record("command_branch", raw)
		},
	})
	return record("factory", fmt.Sprintf("%T", api))
}
