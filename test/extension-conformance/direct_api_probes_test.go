package extensionconformance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/tui"
)

// directStateProbe owns a background reader beyond its command's return. The test changes the Session only after the reader arms, then releases the reader without dispatching another extension request or event.
type directStateProbe struct {
	ctx     context.Context
	cancel  context.CancelFunc
	workers sync.WaitGroup
	armed   chan string
	release chan struct{}
	result  chan string
}

func newDirectStateProbe(t *testing.T) *directStateProbe {
	ctx, cancel := context.WithCancel(t.Context())
	p := &directStateProbe{ctx: ctx, cancel: cancel}
	t.Cleanup(func() { p.cancel(); p.workers.Wait() })
	return p
}

type directStateUI struct{ extension.UIContext }

func (directStateUI) Theme() extension.Theme { return tui.ActiveTheme() }

func registerDirectAPIProbes(api extension.API, ui extension.UIContext, p *directStateProbe) {
	api.RegisterCommand("thinking-model", extension.CommandOptions{Handler: func(ctx context.Context, _ string) error {
		level := api.GetThinkingLevel()
		api.SetThinkingLevel("high")
		provider := ai.NewFauxProvider(ai.FauxConfig{ProviderID: "probe"})
		ok, err := api.SetModel(&ai.Model{Provider: provider, ID: "model"})
		if err != nil {
			return err
		}
		data, err := json.Marshal([]any{level, ok})
		if err != nil {
			return err
		}
		commandUI(ctx, ui).Notify(string(data), "info")
		return nil
	}})
	api.RegisterCommand("commands-probe", extension.CommandOptions{Handler: func(ctx context.Context, _ string) error {
		listed := [][]string{}
		for _, c := range api.GetCommands() {
			if c.Name == "conformance-listed" {
				listed = append(listed, []string{c.Name, string(c.Source), c.Description})
			}
		}
		data, err := json.Marshal(listed)
		if err != nil {
			return err
		}
		commandUI(ctx, ui).Notify(string(data), "info")
		return nil
	}})
	api.RegisterCommand("direct-state-probe", extension.CommandOptions{Handler: func(ctx context.Context, kind string) error {
		extCtx := extension.FromContext(ctx)
		read := func() string {
			switch {
			case kind == "model":
				model, err := extCtx.Model()
				if err != nil {
					return "error:" + err.Error()
				}
				return model.ID
			case kind == "session-name":
				return api.GetSessionName()
			case kind == "thinking-level":
				return string(api.GetThinkingLevel())
			case kind == "theme":
				currentUI, err := extCtx.UI()
				if err != nil {
					return "error:" + err.Error()
				}
				return currentUI.Theme().(*tui.Theme).Name
			case strings.HasPrefix(kind, "settings:"):
				data, err := json.Marshal(api.GetSettings()[strings.TrimPrefix(kind, "settings:")])
				if err != nil {
					return "error:" + err.Error()
				}
				return string(data)
			case kind == "mcp":
				servers := api.GetMcpServers()
				names := make([]string, len(servers))
				for i, server := range servers {
					names[i] = server.Name
				}
				return strings.Join(names, ",")
			}
			return fmt.Sprintf("unsupported kind %q", kind)
		}
		armed, release, result := p.armed, p.release, p.result
		p.workers.Go(func() {
			armed <- read()
			select {
			case <-p.ctx.Done():
				return
			case <-release:
			}
			result <- read()
		})
		return nil
	}})
}

func runDirectBackgroundState(t *testing.T, h *harness, rig *statePushRig) {
	t.Helper()
	// Include thinking level as well: native accessors are live, regardless of which SDKs replicate this field.
	for _, kind := range rig.kinds("node") {
		t.Run(kind.name, func(t *testing.T) {
			if kind.setup != nil {
				kind.setup(t)
			}
			p := h.directProbe
			p.armed, p.release, p.result = make(chan string, 1), make(chan struct{}), make(chan string, 1)
			var released sync.Once
			release := func() { released.Do(func() { close(p.release) }) }
			t.Cleanup(func() { release(); p.workers.Wait() })
			command, ok := findCommand(h.runner, "direct-state-probe")
			if !ok {
				t.Fatal("direct-state-probe command missing")
			}
			if err := command.Handler(h.runner.DispatchContext(t.Context()), kind.name); err != nil {
				t.Fatal(err)
			}
			var armed string
			select {
			case armed = <-p.armed:
			case <-t.Context().Done():
				t.Fatal("reader did not arm")
			}
			if !kind.armed(armed) {
				t.Fatalf("reader armed on %q", armed)
			}
			kind.change(t)
			release()
			select {
			case read := <-p.result:
				t.Logf("background read without extension event or request: %q => %q", armed, read)
				if !kind.read(read) {
					t.Errorf("background reader after host change = %q (armed on %q)", read, armed)
				}
			case <-t.Context().Done():
				t.Fatal("reader did not report")
			}
			p.workers.Wait()
		})
	}
}
