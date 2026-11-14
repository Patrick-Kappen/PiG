package extensionconformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/factoryload"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
	"github.com/MichaelKinsy/PiG/coding/extension/host/subprocess"
	"github.com/MichaelKinsy/PiG/extensions/sdk"
)

// Both realizations read the conversation length, return no context mutation, and run an identity prepare hook followed by a constant successful tool result. Diagnostic file IO, model calls, startup, and Session persistence are outside the measurement. Tool timing includes RunToolCall's argument preparation, validation, and execution, not just two callbacks.
func demoBenchmarkRunner(b *testing.B, realization string) *inproc.Runner {
	b.Helper()
	var loaded extension.Extension
	var runtime *extension.ExtensionRuntime
	if realization == "direct" {
		runtime = extension.CreateExtensionRuntime()
		var err error
		loaded, err = factoryload.LoadExtensionFromFactory(func(api extension.API) error {
			api.OnContext(func(_ context.Context, event extension.ContextEvent) (extension.ContextEventResult, error) {
				if len(event.Messages) != 200 {
					return extension.ContextEventResult{}, errors.New("wrong conversation length")
				}
				return extension.ContextEventResult{}, nil
			})
			api.RegisterTool(extension.ToolDefinition{
				Name: "bench_echo", Label: "bench_echo", Description: "Benchmark native dispatch", Parameters: json.RawMessage(`{"type":"object"}`),
				PrepareArguments: func(args json.RawMessage) (json.RawMessage, error) { return args, nil },
				Execute: func(context.Context, string, json.RawMessage, extension.AgentToolUpdateCallback) (extension.AgentToolResult, error) {
					return extension.AgentToolResult{Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "ok"}}}, nil
				},
			})
			return nil
		}, b.TempDir(), nil, runtime, "direct-bench")
		if err != nil {
			b.Fatal(err)
		}
	} else {
		host := subprocess.NewHostWithConfigRoot(b.TempDir(), b.TempDir())
		b.Cleanup(func() { host.Shutdown("benchmark complete") })
		ext := sdk.New("fused-bench")
		ext.OnEvent("context", func(_ sdk.Context, data map[string]any) (any, error) {
			if len(data["messages"].([]any)) != 200 {
				return nil, errors.New("wrong conversation length")
			}
			return nil, nil
		})
		ext.ToolWithPrepareArguments("bench_echo", "Benchmark native dispatch", sdk.Schema{"type": "object"},
			func(args json.RawMessage) (json.RawMessage, error) { return args, nil },
			func(sdk.Context, map[string]any) (any, error) {
				return sdk.ToolResult{Content: "ok"}, nil
			})
		value, err := host.LoadInProcess(b.Context(), subprocess.ExtConfig{Name: "fused-bench", Enabled: true}, ext.RunWithConn)
		if err != nil {
			b.Fatal(err)
		}
		loaded, runtime = *value, host.Runtime()
	}
	runner := inproc.NewRunner([]extension.Extension{loaded}, b.TempDir(), runtime)
	b.Cleanup(func() { runner.Invalidate("benchmark complete") })
	return runner
}

func BenchmarkDirectGoDemoContext(b *testing.B) {
	for _, realization := range []string{"direct", "fused"} {
		b.Run(realization, func(b *testing.B) {
			runner := demoBenchmarkRunner(b, realization)
			messages := make([]extension.AgentMessage, 200)
			for i := range messages {
				text := fmt.Sprintf("%04d", i) + strings.Repeat("x", 10236)
				if i%2 == 0 {
					messages[i].User = &agent.UserMessage{Role: "user", Content: ai.UserText(text)}
				} else {
					messages[i].Assistant = &agent.AssistantMessage{Role: "assistant", Content: []ai.AssistantContentBlock{ai.TextContent{Text: text}}}
				}
			}
			wire, err := json.Marshal(extension.ContextEvent{Type: "context", Messages: messages})
			if err != nil {
				b.Fatal(err)
			}
			b.Logf("200 messages, 2048000 content bytes, %d encoded event bytes", len(wire))
			b.ReportAllocs()
			b.SetBytes(2048000)
			for b.Loop() {
				result, err := runner.EmitContext(b.Context(), messages)
				if err != nil || len(result) != len(messages) {
					b.Fatalf("context: %d messages, error %v", len(result), err)
				}
			}
		})
	}
}

func BenchmarkDirectGoDemoPreparedTool(b *testing.B) {
	for _, realization := range []string{"direct", "fused"} {
		b.Run(realization, func(b *testing.B) {
			runner := demoBenchmarkRunner(b, realization)
			tools, errs := coding.BridgeNewRunnerTools(runner.Tools())
			if len(errs) != 0 {
				b.Fatal(errs)
			}
			call := agent.AgentToolCall{ID: "bench", Name: "bench_echo"}
			call.SetFinalArguments(`{"z":2,"x":1,"a":1.25}`)
			options := agent.RunToolCallOptions{Tools: tools}
			b.ReportAllocs()
			for b.Loop() {
				result, err := agent.RunToolCall(b.Context(), call, options)
				if err != nil || result.IsError || result.Result.Text() != "ok" {
					b.Fatalf("tool: %+v, error %v", result, err)
				}
			}
		})
	}
}
