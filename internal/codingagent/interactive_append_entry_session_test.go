package codingagent_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/coding"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/factoryload"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
	icodingagent "github.com/MichaelKinsy/PiG/internal/codingagent"
	"github.com/MichaelKinsy/PiG/tui"
)

// Pi 1.5.0 _bindExtensionCore appends through the Session and emits entry_appended; interactive mode renders the custom entry once. Drive both subscriber and UI delivery so a second render from the event cannot duplicate the transcript.
func TestInteractiveNativeAppendEntryPublishesAndRendersOnce(t *testing.T) {
	services, err := coding.CreateAgentSessionServices(coding.CreateAgentSessionServicesOptions{CWD: t.TempDir(), AgentDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(services.Close)
	runtime := extension.CreateExtensionRuntime()
	const marker = "native-append-transcript-marker"
	data := &struct{ Text string }{Text: marker}
	ext, err := factoryload.LoadExtensionFromFactory(func(pi extension.API) error {
		pi.RegisterEntryRenderer("native-state", func(entry extension.CustomEntry, _ extension.EntryRenderOptions, _ extension.Theme) extension.Component {
			if entry.Data != data {
				return tui.NewText("renderer-data-identity-lost")
			}
			return tui.NewText(data.Text)
		})
		pi.OnSessionStart(func(context.Context, extension.SessionStartEvent) error {
			pi.AppendEntry("native-state", data)
			return nil
		})
		return nil
	}, services.CWD(), nil, runtime, "<inline:append-render>")
	if err != nil {
		t.Fatal(err)
	}
	runner := inproc.NewRunner([]extension.Extension{ext}, services.CWD(), runtime)
	session, err := coding.NewSession(services, coding.SessionOptions{Runner: runner, NoSession: true, SkipBuiltinTools: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	})
	var mu sync.Mutex
	var published []json.RawMessage
	session.Subscribe(func(event agent.AgentEvent) {
		if event, ok := event.(agent.EntryAppendedEvent); ok {
			mu.Lock()
			published = append(published, event.Entry)
			mu.Unlock()
		}
	})
	var hostErrors []extension.ExtensionError
	runner.AddErrorListener(func(err *extension.ExtensionError) { hostErrors = append(hostErrors, *err) })
	h := icodingagent.NewTestHarness(t, icodingagent.InteractiveModeOptions{
		CWD: services.CWD(), AgentDir: services.AgentDir(), Settings: services.Settings(), SettingsManager: services.SettingsManager(),
		SessionHandle: session, ExtensionRunner: runner, NoBuiltinTools: true,
	}, nil)
	if _, err := runner.Emit(t.Context(), extension.SessionStartEvent{Type: "session_start", Reason: "startup"}); err != nil {
		t.Fatal(err)
	}
	if len(hostErrors) != 0 {
		t.Fatalf("extension errors: %+v", hostErrors)
	}
	if err := session.FlushEvents(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Chat posts behind the append action's UI task, and FlushEvents acknowledges event handling first.
	if chat := h.Chat(); strings.Count(chat, marker) != 1 {
		t.Fatalf("transcript = %q, want exactly one rendered append", chat)
	}
	var stored []json.RawMessage
	for _, entry := range session.Entries() {
		if entry.Base().Type == "custom" {
			stored = append(stored, entry.Raw())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(stored) != 1 || len(published) != 1 || string(stored[0]) != string(published[0]) {
		t.Fatalf("stored entries = %s, entry_appended = %s; want the same one entry", stored, published)
	}
}
