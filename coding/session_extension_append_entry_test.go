package coding

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/factoryload"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
)

type nativeAppendEntry struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	CustomType string `json:"customType"`
	Data       string `json:"data"`
}

func nativeAppendSession(t *testing.T, runner *inproc.Runner) *Session {
	t.Helper()
	services, err := CreateAgentSessionServices(CreateAgentSessionServicesOptions{CWD: t.TempDir(), AgentDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(services.Close)
	session, err := NewSession(services, SessionOptions{Runner: runner, NoSession: true, SkipBuiltinTools: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	})
	return session
}

func nativeAppendEntries(t *testing.T, session *Session) []nativeAppendEntry {
	t.Helper()
	var entries []nativeAppendEntry
	for _, entry := range session.Entries() {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(entry.Raw(), &header); err != nil {
			t.Fatal(err)
		}
		if header.Type == "custom" {
			var value nativeAppendEntry
			if err := json.Unmarshal(entry.Raw(), &value); err != nil {
				t.Fatal(err)
			}
			entries = append(entries, value)
		}
	}
	return entries
}

func watchNativeAppends(session *Session) func() []nativeAppendEntry {
	var mu sync.Mutex
	var events []nativeAppendEntry
	session.Subscribe(func(event agent.AgentEvent) {
		if event, ok := event.(agent.EntryAppendedEvent); ok {
			var entry nativeAppendEntry
			if err := json.Unmarshal(event.Entry, &entry); err != nil {
				panic(err)
			}
			mu.Lock()
			events = append(events, entry)
			mu.Unlock()
		}
	})
	return func() []nativeAppendEntry {
		mu.Lock()
		defer mu.Unlock()
		return append([]nativeAppendEntry(nil), events...)
	}
}

func assertNativeAppends(t *testing.T, session *Session, events func() []nativeAppendEntry, want []string) {
	t.Helper()
	if err := session.FlushEvents(t.Context()); err != nil {
		t.Fatal(err)
	}
	entries := nativeAppendEntries(t, session)
	var got []string
	for _, entry := range entries {
		got = append(got, entry.CustomType+":"+entry.Data)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("custom entries = %v, want %v", got, want)
	}
	if emitted := events(); !reflect.DeepEqual(emitted, entries) {
		t.Fatalf("entry_appended = %+v, want exactly the stored entries in order %+v", emitted, entries)
	}
}

// Pi 1.5.0 agent-session.js _bindExtensionCore binds the shared runtime on every mode bind; runner.js emit awaits each extension's handlers in registration order.
func TestNativeAppendEntryTwoExtensionsKeepBindingAfterRebind(t *testing.T) {
	runtime := extension.CreateExtensionRuntime()
	var extensions []extension.Extension
	for _, name := range []string{"first", "second"} {
		ext, err := factoryload.LoadExtensionFromFactory(func(pi extension.API) error {
			pi.OnSessionStart(func(_ context.Context, event extension.SessionStartEvent) error {
				pi.AppendEntry(name, event.Reason)
				return nil
			})
			return nil
		}, t.TempDir(), nil, runtime, "<inline:"+name+">")
		if err != nil {
			t.Fatal(err)
		}
		extensions = append(extensions, ext)
	}
	runner := inproc.NewRunner(extensions, t.TempDir(), runtime)
	original := nativeAppendSession(t, runner)
	originalEvents := watchNativeAppends(original)
	live := original
	bind := func() {
		BindSessionExtensionActions(runner, nil, func() *Session { return live }, extension.ContextActions{}, nil)
	}
	emit := func(reason string) {
		t.Helper()
		if _, err := runner.Emit(t.Context(), extension.SessionStartEvent{Type: "session_start", Reason: reason}); err != nil {
			t.Fatal(err)
		}
	}
	var hostErrors []extension.ExtensionError
	runner.AddErrorListener(func(err *extension.ExtensionError) { hostErrors = append(hostErrors, *err) })
	bind()
	emit("startup")
	assertNativeAppends(t, original, originalEvents, []string{"first:startup", "second:startup"})

	live = nativeAppendSession(t, runner)
	liveEvents := watchNativeAppends(live)
	bind()
	emit("new")
	// Reusing both factories again detects a binding lost after the other extension appends.
	emit("resume")
	assertNativeAppends(t, live, liveEvents, []string{"first:new", "second:new", "first:resume", "second:resume"})
	assertNativeAppends(t, original, originalEvents, []string{"first:startup", "second:startup"})
	if len(hostErrors) != 0 {
		t.Fatalf("extension errors: %+v", hostErrors)
	}
}

// Pi 1.5.0 runner.js emitSessionShutdownEvent awaits emit without unbinding the runtime; agent-session.js:2801-2808 still appends and emits entry_appended during the handler.
func TestNativeAppendEntryDuringSessionShutdown(t *testing.T) {
	runtime := extension.CreateExtensionRuntime()
	finished := false
	ext, err := factoryload.LoadExtensionFromFactory(func(pi extension.API) error {
		pi.OnSessionShutdown(func(_ context.Context, event extension.SessionShutdownEvent) error {
			pi.AppendEntry("shutdown-state", event.Reason)
			finished = true
			return nil
		})
		return nil
	}, t.TempDir(), nil, runtime, "<inline:shutdown>")
	if err != nil {
		t.Fatal(err)
	}
	runner := inproc.NewRunner([]extension.Extension{ext}, t.TempDir(), runtime)
	session := nativeAppendSession(t, runner)
	events := watchNativeAppends(session)
	BindSessionExtensionActions(runner, nil, func() *Session { return session }, extension.ContextActions{}, nil)
	var hostErrors []extension.ExtensionError
	runner.AddErrorListener(func(err *extension.ExtensionError) { hostErrors = append(hostErrors, *err) })
	session.EmitSessionShutdown("quit")
	if !finished {
		t.Fatal("session_shutdown returned before appendEntry completed")
	}
	assertNativeAppends(t, session, events, []string{"shutdown-state:quit"})
	if len(hostErrors) != 0 {
		t.Fatalf("extension errors: %+v", hostErrors)
	}
}

func TestNativeAppendEntryWithoutLiveSessionUsesHostErrorPath(t *testing.T) {
	runtime := extension.CreateExtensionRuntime()
	continued := false
	ext, err := factoryload.LoadExtensionFromFactory(func(pi extension.API) error {
		pi.OnSessionStart(func(context.Context, extension.SessionStartEvent) error {
			pi.AppendEntry("must-not-exist", "missing-session")
			return nil
		})
		pi.OnSessionStart(func(context.Context, extension.SessionStartEvent) error {
			continued = true
			return nil
		})
		return nil
	}, t.TempDir(), nil, runtime, "<inline:missing-session>")
	if err != nil {
		t.Fatal(err)
	}
	runner := inproc.NewRunner([]extension.Extension{ext}, t.TempDir(), runtime)
	session := nativeAppendSession(t, runner)
	events := watchNativeAppends(session)
	var live *Session
	BindSessionExtensionActions(runner, nil, func() *Session { return live }, extension.ContextActions{}, nil)
	if err := runtime.AppendEntry("must-not-exist", "direct"); !errors.Is(err, ErrSessionNotReady) || err.Error() != "agent session not initialized" {
		t.Fatalf("appendEntry without a Session = %v, want ErrSessionNotReady", err)
	}
	// Before the live-Session binding, the API panicked with "Extension runtime not initialized. Action methods cannot be called during extension loading." The bound action now returns ErrSessionNotReady; the API panic must be caught by normal host dispatch.
	var hostErrors []extension.ExtensionError
	runner.AddErrorListener(func(err *extension.ExtensionError) { hostErrors = append(hostErrors, *err) })
	if _, err := runner.Emit(t.Context(), extension.SessionStartEvent{Type: "session_start", Reason: "startup"}); err != nil {
		t.Fatal(err)
	}
	if !continued {
		t.Fatal("appendEntry failure escaped host dispatch and stopped later handlers")
	}
	if len(hostErrors) != 1 || hostErrors[0].Error != "extension handler panicked: "+ErrSessionNotReady.Error() || hostErrors[0].Event != "session_start" || hostErrors[0].ExtensionPath != ext.Path {
		t.Fatalf("host errors = %+v, want one ErrSessionNotReady for the failing extension", hostErrors)
	}
	assertNativeAppends(t, session, events, nil)
}
