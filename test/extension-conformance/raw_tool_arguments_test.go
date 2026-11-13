package extensionconformance

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/fusepack"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
	"github.com/MichaelKinsy/PiG/coding/extension/host/subprocess"
	"github.com/MichaelKinsy/PiG/extensions/sdk"
	"github.com/MichaelKinsy/PiG/internal/testbudget"
	"github.com/MichaelKinsy/PiG/test/extension-conformance/testfixture/rawargs"
)

// rawArgsBinary is the row's extension as a standalone executable.
var rawArgsBinary testbudget.Once[string]

// rawArgsHarnesses load testfixture/rawargs isolated (its own process) and fused (served in the host's process over an in-memory connection, as a Piglet Binary serves it).
func rawArgsHarnesses() []harnessCase {
	load := func(t *testing.T, fused bool) *harness {
		t.Helper()
		host := subprocess.NewHost(t.TempDir())
		host.SetHostInfo(conformanceHostInfo)
		var loaded *extension.Extension
		var err error
		if fused {
			sdk.SetHostHelpers(fusepack.HostHelpers())
			loaded, err = host.LoadInProcess(context.Background(), subprocess.ExtConfig{Name: "raw-args", Enabled: true}, func(conn net.Conn) error {
				return rawargs.Extension("raw-args").RunWithConn(conn)
			})
		} else {
			bin, buildErr := rawArgsBinary.Do(t, func(ctx context.Context) (string, error) {
				return buildConformanceCommand(ctx, "raw-args", "rawargs/cmd")
			})
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			loaded, err = host.Load(context.Background(), subprocess.ExtConfig{Name: "raw-args", Path: bin, Enabled: true})
		}
		if err != nil {
			host.Shutdown("load failed")
			t.Fatal(err)
		}
		return &harness{runner: inproc.NewRunner([]extension.Extension{*loaded}, t.TempDir()), host: host}
	}
	return []harnessCase{
		{name: "subprocess-go", make: func(t *testing.T) *harness { return load(t, false) }},
		{name: "fused-go", make: func(t *testing.T) *harness { return load(t, true) }},
	}
}

// rawArgsProvider answers the first request with one tool call whose arguments are the given text, as a provider's stream completes them, and the next with text.
type rawArgsProvider struct {
	name, arguments string
	calls           int
}

func (p *rawArgsProvider) ID() string   { return "raw-args" }
func (p *rawArgsProvider) Close() error { return nil }
func (p *rawArgsProvider) Stream(context.Context, ai.TranscriptContext, ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	p.calls++
	message := ai.AssistantMessage{StopReason: ai.StopReasonStop, Content: []ai.AssistantContentBlock{ai.TextContent{Text: "done"}}}
	if p.calls == 1 {
		call := ai.ToolCall{ID: "raw-1", Name: p.name}
		call.SetFinalArguments(p.arguments)
		message = ai.AssistantMessage{StopReason: ai.StopReasonToolUse, Content: []ai.AssistantContentBlock{call}}
	}
	stream := ai.NewAssistantMessageEventStream()
	go func() { _ = stream.Push(ai.DoneEvent{Reason: message.StopReason, Message: &message}) }()
	return stream, nil
}

// dispatchRawToolCall has the model call name with the arguments text and returns the one tool result, through the agent's production dispatch (prepare, validate, execute).
func dispatchRawToolCall(t *testing.T, h *harness, name, arguments string) *agent.ToolResultMessage {
	t.Helper()
	tools, errs := coding.BridgeNewRunnerTools(h.runner.Tools())
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	a := mustNewAgent(agent.AgentOptions{Model: &ai.Model{ID: "raw-1", Provider: &rawArgsProvider{name: name, arguments: arguments}}, Tools: tools})
	messages, err := a.Send(t.Context(), "go")
	if err != nil {
		t.Fatal(err)
	}
	var results []*agent.ToolResultMessage
	for _, message := range messages {
		if message.ToolResult != nil {
			results = append(results, message.ToolResult)
		}
	}
	if len(results) != 1 {
		t.Fatalf("%s: %d tool results, want 1", name, len(results))
	}
	return results[0]
}

// Upstream agent-loop.ts:474-516 (prepareToolCallArguments, prepareToolCall) hands tool.prepareArguments the object the model's arguments parsed to, in the order the model wrote its members, then validates and executes what the hook returns. A Go tool's PrepareArguments gets that text and the host validates and executes its answer as it is, isolated and fused.
func TestRawToolArgumentsReachTheHookAndTheToolInOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping conformance suite in short mode (builds subprocess fixtures)")
	}
	for _, tc := range rawArgsHarnesses() {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.make(t)
			t.Cleanup(func() { h.host.Shutdown("test done") })

			// The hook gets the host's text byte for byte. ctx.ToolInput is JSON.stringify of the executed arguments, which equals that text except that JSON.stringify writes U+2028 as the character, not as an escape.
			for _, tc := range []struct{ arguments, executed string }{
				{`{"z":2,"x":1,"a":1.25}`, `{"z":2,"x":1,"a":1.25}`},
				{`{"b":null,"a":{"y":[1,{"d":0,"c":null}],"x":"s"}}`, `{"b":null,"a":{"y":[1,{"d":0,"c":null}],"x":"s"}}`},
				{`{"s":"\ud800","t":"a\u2028b"}`, "{\"s\":\"\\ud800\",\"t\":\"a\u2028b\"}"},
				{`{"f":1.25,"g":0.30000000000000004,"h":1e-7}`, `{"f":1.25,"g":0.30000000000000004,"h":1e-7}`},
			} {
				result := dispatchRawToolCall(t, h, "raw_echo", tc.arguments)
				if want := "prepare=" + tc.arguments + " execute=" + tc.executed; result.IsError || result.Text() != want {
					t.Errorf("raw_echo %s = %q (error %t), want %q", tc.arguments, result.Text(), result.IsError, want)
				}
			}
			// Pi parses the model's 9007199254740993 as a JavaScript number before prepareArguments runs, so the hook and the tool get 9007199254740992.
			result := dispatchRawToolCall(t, h, "raw_echo", `{"n":9007199254740993,"m":1.0}`)
			if want := `prepare={"n":9007199254740992,"m":1} execute={"n":9007199254740992,"m":1}`; result.IsError || result.Text() != want {
				t.Errorf("raw_echo past 2^53 = %q (error %t), want %q", result.Text(), result.IsError, want)
			}

			result = dispatchRawToolCall(t, h, "raw_replace", `{"z":1}`)
			if want := `prepare={"z":1} execute={"zz":true,"text":"replaced","aa":[2,1]}`; result.IsError || result.Text() != want {
				t.Errorf("raw_replace = %q (error %t), want %q", result.Text(), result.IsError, want)
			}
			result = dispatchRawToolCall(t, h, "raw_replace", `{"bad":true}`)
			if !result.IsError || !strings.HasPrefix(result.Text(), "Validation failed") {
				t.Errorf("raw_replace with arguments the schema rejects = %q (error %t), want the host's validation error", result.Text(), result.IsError)
			}

			result = dispatchRawToolCall(t, h, "raw_fail", `{"a":1}`)
			if !result.IsError || result.Text() != "raw prepare refused the arguments" {
				t.Errorf("raw_fail = %q (error %t), want the hook's error and no execution", result.Text(), result.IsError)
			}

			// A hook that returns nothing answers the JSON null, as the map hook's nil map did, and the host's validation rejects it.
			result = dispatchRawToolCall(t, h, "prepare_nil", `{"a":1}`)
			if !result.IsError || !strings.HasPrefix(result.Text(), "Validation failed") {
				t.Errorf("prepare_nil = %q (error %t), want the host's validation error and no execution", result.Text(), result.IsError)
			}
		})
	}
}

// Upstream runner.ts emitToolCall hands a tool_call handler event.input, the object in the model's member order. A Go handler reads it with ctx.ToolInput.
func TestToolCallHandlerToolInputKeepsTheOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping conformance suite in short mode (builds subprocess fixtures)")
	}
	for _, tc := range rawArgsHarnesses() {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.make(t)
			t.Cleanup(func() { h.host.Shutdown("test done") })
			wire := json.RawMessage(`{"z":2,"x":{"q":null,"b":1},"a":1.25}`)
			var input map[string]any
			if err := json.Unmarshal(wire, &input); err != nil {
				t.Fatal(err)
			}
			result, err := h.runner.EmitToolCall(t.Context(), extension.CustomToolCallEvent{
				ToolCallEventBase: extension.ToolCallEventBase{Type: "tool_call", ToolCallID: "probe-1", WireInput: &wire},
				ToolName:          "tool_call_probe",
				Input:             input,
			})
			if err != nil {
				t.Fatal(err)
			}
			if want := "input=" + string(wire); result == nil || !result.Block || result.Reason != want {
				t.Fatalf("tool_call result = %+v, want a block with reason %s", result, want)
			}
		})
	}
}

// buildConformanceCommand builds testfixture/<dir> into the fixture root as name.
func buildConformanceCommand(ctx context.Context, name, dir string) (string, error) {
	bin := filepath.Join(fixtureRoot, "raw-args", testExecutableName(name))
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	cmd.Dir = filepath.Join(fixtureSourceRoot, "test", "extension-conformance", "testfixture", filepath.FromSlash(dir))
	// Root-module fixtures require the workspace's local SDK, not the published release. Workspace mode rejects -mod=mod.
	flags := strings.Join(slices.DeleteFunc(strings.Fields(os.Getenv("GOFLAGS")), func(f string) bool { return f == "-mod=mod" }), " ")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK="+filepath.Join(fixtureSourceRoot, "go.work"), "GOFLAGS="+flags)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build %s: %w\n%s", name, err, out)
	}
	return bin, nil
}
