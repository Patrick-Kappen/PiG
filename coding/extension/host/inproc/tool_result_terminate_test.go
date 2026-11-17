package inproc_test

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
)

// PiG's additive terminate override has isError's omitted/false/true chaining semantics.
func TestEmitToolResultTerminateChains(t *testing.T) {
	for _, initial := range []bool{false, true} {
		for _, last := range []bool{false, true} {
			t.Run(stringBool(initial)+"/"+stringBool(last), func(t *testing.T) {
				handler := func(want bool, reply any) extension.Extension {
					ext := newFakeExtension("terminate")
					ext.Handlers["tool_result"] = []extension.HandlerFn{func(args ...any) (any, error) {
						raw, err := json.Marshal(args[0])
						if err != nil {
							t.Fatal(err)
						}
						var event map[string]any
						if err := json.Unmarshal(raw, &event); err != nil {
							t.Fatal(err)
						}
						if event["terminate"] != want {
							t.Errorf("handler sees terminate=%v, want %v", event["terminate"], want)
						}
						if reply == nil {
							return nil, nil
						}
						raw, err = json.Marshal(reply)
						return json.RawMessage(raw), err
					}}
					return ext
				}
				var event extension.CustomToolResultEvent
				if err := json.Unmarshal([]byte(`{"type":"tool_result","toolName":"custom","terminate":`+stringBool(initial)+`}`), &event); err != nil {
					t.Fatal(err)
				}
				runner := inproc.NewRunner([]extension.Extension{
					handler(initial, map[string]any{"terminate": !initial}),
					handler(!initial, map[string]any{"details": "keep flag"}),
					handler(!initial, map[string]any{"terminate": last}),
					handler(last, nil),
				}, t.TempDir())
				got, err := runner.EmitToolResult(t.Context(), event)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				var result map[string]any
				if err := json.Unmarshal(raw, &result); err != nil {
					t.Fatal(err)
				}
				if result["terminate"] != last {
					t.Fatalf("result terminate=%v, want %v", result["terminate"], last)
				}
			})
		}
	}
}

func TestToolResultTerminateSurvivesEveryEventVariant(t *testing.T) {
	for _, flag := range []*bool{nil, new(false), new(true)} {
		base := extension.ToolResultEventBase{Terminate: flag}
		for _, event := range []extension.ToolResultEvent{
			extension.BashToolResultEvent{ToolResultEventBase: base},
			extension.PowerShellToolResultEvent{ToolResultEventBase: base},
			extension.ReadToolResultEvent{ToolResultEventBase: base},
			extension.EditToolResultEvent{ToolResultEventBase: base},
			extension.WriteToolResultEvent{ToolResultEventBase: base},
			extension.GrepToolResultEvent{ToolResultEventBase: base},
			extension.FindToolResultEvent{ToolResultEventBase: base},
			extension.LsToolResultEvent{ToolResultEventBase: base},
			extension.CustomToolResultEvent{ToolResultEventBase: base},
		} {
			ext := newFakeExtension("terminate")
			for range 2 {
				ext.Handlers["tool_result"] = append(ext.Handlers["tool_result"], func(args ...any) (any, error) {
					raw, err := json.Marshal(args[0])
					if err != nil {
						return nil, err
					}
					var got struct{ Terminate *bool }
					if err := json.Unmarshal(raw, &got); err != nil {
						return nil, err
					}
					if (got.Terminate == nil) != (flag == nil) || (flag != nil && *got.Terminate != *flag) {
						t.Errorf("%T lost flag in event: %s", event, raw)
					}
					return &extension.ToolResultEventResult{Details: "other edit"}, nil
				})
			}
			runner := inproc.NewRunner([]extension.Extension{ext}, t.TempDir())
			result, err := runner.EmitToolResult(t.Context(), event)
			if err != nil {
				t.Fatal(err)
			}
			if (result.Terminate == nil) != (flag == nil) || (flag != nil && *result.Terminate != *flag) {
				t.Fatalf("%T changed flag without an override: %+v", event, result)
			}
		}
	}
}

func BenchmarkToolResultTerminateChain(b *testing.B) {
	for _, handlers := range []int{0, 1, 8} {
		b.Run(strconv.Itoa(handlers), func(b *testing.B) {
			ext := newFakeExtension("terminate")
			for range handlers {
				ext.Handlers["tool_result"] = append(ext.Handlers["tool_result"], func(args ...any) (any, error) {
					event := args[0].(extension.CustomToolResultEvent)
					return &extension.ToolResultEventResult{Terminate: new(!*event.Terminate)}, nil
				})
			}
			runner := inproc.NewRunner([]extension.Extension{ext}, b.TempDir())
			event := extension.CustomToolResultEvent{ToolResultEventBase: extension.ToolResultEventBase{Terminate: new(false)}, ToolName: "custom"}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := runner.EmitToolResult(b.Context(), event); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func stringBool(value bool) string { return strconv.FormatBool(value) }
