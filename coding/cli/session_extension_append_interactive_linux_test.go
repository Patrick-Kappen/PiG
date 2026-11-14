//go:build linux

package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/coding"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/factoryload"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
	"github.com/MichaelKinsy/PiG/internal/codingagent"
	"github.com/MichaelKinsy/PiG/internal/testbudget"
)

const nativeAppendInteractiveEnv = "PIG_TEST_NATIVE_APPEND_INTERACTIVE"

// Pi 1.5.0 agent-session.js _bindExtensionCore:2801-2808 writes one custom entry and emits entry_appended once. Run InteractiveMode.Init on a PTY so its own wireInprocContextActions bind, not the headless binder, supplies the factory's appendEntry.
func TestNativeAppendEntryInteractiveModeBind(t *testing.T) {
	if os.Getenv(nativeAppendInteractiveEnv) == "1" {
		testNativeAppendInteractiveChild(t)
		return
	}
	master, slave := openPTY(t, 40, 140)
	t.Cleanup(func() { _ = master.Close() })
	t.Cleanup(func() { _ = slave.Close() })
	home := t.TempDir()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestNativeAppendEntryInteractiveModeBind$", "-test.v")
	cmd.Env = append(os.Environ(), nativeAppendInteractiveEnv+"=1", "HOME="+home, "PIG_HOME="+home, "PI_HOME="+home,
		"PIG_CODING_AGENT_DIR="+filepath.Join(home, "agent"), "PI_CODING_AGENT_DIR="+filepath.Join(home, "agent"), "PIG_OFFLINE=1", "TERM=xterm-256color")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = slave.Close()
	output := &ptyOutput{}
	copied := make(chan struct{})
	go func() { _, _ = io.Copy(output, master); close(copied) }()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	select {
	case err := <-exited:
		<-copied
		if err != nil {
			t.Fatalf("interactive appendEntry: %v\n%s", err, output.since(0))
		}
	case <-time.After(testbudget.Wait(t)):
		_ = cmd.Process.Kill()
		<-exited
		<-copied
		t.Fatalf("interactive Init did not finish:\n%s", output.since(0))
	}
}

func testNativeAppendInteractiveChild(t *testing.T) {
	t.Helper()
	services, err := coding.CreateAgentSessionServices(coding.CreateAgentSessionServicesOptions{CWD: t.TempDir(), AgentDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(services.Close)
	runtime := extension.CreateExtensionRuntime()
	completed := false
	ext, err := factoryload.LoadExtensionFromFactory(func(pi extension.API) error {
		pi.OnSessionStart(func(context.Context, extension.SessionStartEvent) error {
			pi.AppendEntry("interactive-native-state", "live")
			completed = true
			return nil
		})
		return nil
	}, services.CWD(), nil, runtime, "<inline:interactive-append>")
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
	var events []json.RawMessage
	session.Subscribe(func(event agent.AgentEvent) {
		if event, ok := event.(agent.EntryAppendedEvent); ok {
			mu.Lock()
			events = append(events, event.Entry)
			mu.Unlock()
		}
	})
	var hostErrors []extension.ExtensionError
	runner.AddErrorListener(func(err *extension.ExtensionError) { hostErrors = append(hostErrors, *err) })
	mode := codingagent.NewInteractiveMode(nil, codingagent.InteractiveModeOptions{
		CWD: services.CWD(), AgentDir: services.AgentDir(), Settings: services.Settings(), SettingsManager: services.SettingsManager(),
		SessionHandle: session, ExtensionRunner: runner, NoBuiltinTools: true, NoSkills: true, NoPromptTemplates: true, OfflineMode: true,
	})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() {
		cancel()
		if err := mode.Stop(); err != nil {
			t.Error(err)
		}
	})
	if err := mode.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if !completed || len(hostErrors) != 0 {
		t.Fatalf("session_start append completed=%v, extension errors=%+v", completed, hostErrors)
	}
	// Init subscribed the mode to Events, but this test does not enter its input loop. Discard wire publication before flushing the Session's subscriber delivery.
	session.DiscardEvents()
	if err := session.FlushEvents(t.Context()); err != nil {
		t.Fatal(err)
	}
	var entries []json.RawMessage
	for _, entry := range session.Entries() {
		var value struct {
			Type       string `json:"type"`
			CustomType string `json:"customType"`
			Data       string `json:"data"`
		}
		if err := json.Unmarshal(entry.Raw(), &value); err != nil {
			t.Fatal(err)
		}
		if value.Type == "custom" {
			if value.CustomType != "interactive-native-state" || value.Data != "live" {
				t.Fatalf("unexpected custom entry: %s", entry.Raw())
			}
			entries = append(entries, entry.Raw())
		}
	}
	if len(entries) != 1 {
		t.Fatalf("custom entries = %s, want the one session_start append", entries)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(events, entries) {
		t.Fatalf("entry_appended = %s, want exactly the one stored custom entry %s", events, entries)
	}
}
