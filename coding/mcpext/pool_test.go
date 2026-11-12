package mcpext_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/mcpext"
	"github.com/MichaelKinsy/PiG/mcp"
	"github.com/MichaelKinsy/PiG/mcp/mcptest"
)

// Not upstream tests: regression guards for a session_start that replaces the servers of an earlier one without a
// session_shutdown (RPC binds a replacement twice), and for the connection pool of D236.

// serverProcesses stands in for the processes of stdio servers: every transport the extension creates is one start,
// and closing the transport ends it.
type serverProcesses struct {
	mu      sync.Mutex
	started map[string]int
	running map[string]int
	// gate, when set, holds every start until it is closed, as a slow server start does.
	gate chan struct{}
	// entered receives the server name when a start begins, before the gate.
	entered chan string
	// closeGate, when set, holds every close until it is closed, as a server that is slow to exit does.
	closeGate chan struct{}
}

func newServerProcesses() *serverProcesses {
	return &serverProcesses{started: map[string]int{}, running: map[string]int{}, entered: make(chan string, 16)}
}

func (p *serverProcesses) counts(name string) (started, running int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.started[name], p.running[name]
}

// closingTransport is the client end of a fake server that reports its close to the processes.
type closingTransport struct {
	*mcptest.InMemoryTransport
	once  sync.Once
	close func()
}

func (t *closingTransport) Close() error {
	err := t.InMemoryTransport.Close()
	t.once.Do(t.close)
	return err
}

func (p *serverProcesses) create(entry mcpext.McpServerEntry, _ string, _ mcp.AuthProvider) (mcp.Transport, error) {
	p.entered <- entry.Name
	if p.gate != nil {
		<-p.gate
	}
	client, server := mcptest.NewInMemoryTransportPair()
	server.OnMessage(func(message mcp.JSONRPCMessage) {
		if !message.IsRequest() {
			return
		}
		var result string
		switch message.Method {
		case "initialize":
			result = `{"protocolVersion":"` + mcp.LatestProtocolVersion + `","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1.0.0"}}`
		case "tools/list":
			result = `{"tools":[{"name":"ping","inputSchema":{"type":"object"}}]}`
		default:
			result = `{"content":[{"type":"text","text":"pong"}]}`
		}
		go func() { _ = server.Send(context.Background(), mcp.NewResult(*message.ID, json.RawMessage(result))) }()
	})
	_ = server.Start()
	p.mu.Lock()
	p.started[entry.Name]++
	p.running[entry.Name]++
	p.mu.Unlock()
	return &closingTransport{InMemoryTransport: client, close: func() {
		if p.closeGate != nil {
			<-p.closeGate
		}
		p.mu.Lock()
		p.running[entry.Name]--
		p.mu.Unlock()
	}}, nil
}

// commandEntry is a server the pool keeps (a command), urlEntry one it does not.
func commandEntry(name string, args ...string) mcpext.McpServerEntry {
	return mcpext.McpServerEntry{Name: name, Config: extension.McpServerConfig{Command: "fake-" + name, Args: args}, Source: "test", Scope: "global"}
}

func urlEntry(name string) mcpext.McpServerEntry {
	return mcpext.McpServerEntry{Name: name, Config: extension.McpServerConfig{URL: "http://" + name + ".invalid/mcp"}, Source: "test", Scope: "global"}
}

// poolSession is the MCP extension of one session over processes, with the servers config returns at each start.
func poolSession(t *testing.T, processes *serverProcesses, pool *mcpext.ConnectionPool, config func() []mcpext.McpServerEntry) (*mcpext.Extension, *fakeHost) {
	t.Helper()
	host := newFakeHost()
	host.registerBuiltin(mcpext.CodemodeToolName, "builtin:codemode")
	ext := mcpext.New(host, mcpext.Options{
		LoadConfig:      func(context.Context) mcpext.LoadedMcpConfig { return mcpext.LoadedMcpConfig{Servers: config()} },
		CreateTransport: processes.create,
		Credentials:     mcpext.NewMcpOAuthCredentialStoreWithBackend(&mcpext.InMemoryAuthStorageBackend{}, ""),
		LogPath:         filepath.Join(t.TempDir(), "mcp.log"),
		Pool:            pool,
	})
	return ext, host
}

func fixed(entries ...mcpext.McpServerEntry) func() []mcpext.McpServerEntry {
	return func() []mcpext.McpServerEntry { return entries }
}

func hasTool(host *fakeHost, name string) bool {
	return slices.ContainsFunc(host.GetAllTools(), func(tool extension.ToolInfo) bool {
		return tool.Name == name && tool.Exposure != extension.ToolExposureHidden
	})
}

// RPC mode binds a replaced session twice (rpc-mode.ts:435-441), so the extension gets a second session_start while
// the servers of the first are still starting. Pi's second start replaces them before they spawn (they wait for
// setImmediate, index.ts:1140): each server runs once. The second start takes the first start's connections over; it
// neither starts the servers again nor leaves the first start's servers running after the shutdown.
func TestASecondSessionStartTakesOverTheConnectionsOfTheFirst(t *testing.T) {
	processes := newServerProcesses()
	processes.gate = make(chan struct{})
	ext, host := poolSession(t, processes, nil, fixed(commandEntry("files"), urlEntry("docs")))
	ctx := eventContext(t.TempDir(), &notifications{})
	ext.SessionStart(ctx)
	// Both servers of the first start are starting.
	for range 2 {
		<-processes.entered
	}
	ext.SessionStart(ctx)
	close(processes.gate)
	ext.Pending()
	waitFor(t, "the tools of both servers", func() bool { return hasTool(host, "mcp__files__ping") && hasTool(host, "mcp__docs__ping") })
	for _, name := range []string{"files", "docs"} {
		if started, running := processes.counts(name); started != 1 || running != 1 {
			t.Fatalf("%s: started %d, running %d; want 1 and 1", name, started, running)
		}
	}
	ext.SessionShutdown()
	for _, name := range []string{"files", "docs"} {
		if _, running := processes.counts(name); running != 0 {
			t.Fatalf("%s: %d running after the shutdown, want 0", name, running)
		}
	}
}

// A second session_start whose config changed a server while it was starting closes the server it no longer uses,
// instead of leaving it running.
func TestASecondSessionStartClosesAStartingServerItReplaces(t *testing.T) {
	processes := newServerProcesses()
	processes.gate = make(chan struct{})
	var mu sync.Mutex
	args := "one"
	ext, _ := poolSession(t, processes, nil, func() []mcpext.McpServerEntry {
		mu.Lock()
		defer mu.Unlock()
		return []mcpext.McpServerEntry{commandEntry("files", args)}
	})
	ctx := eventContext(t.TempDir(), &notifications{})
	ext.SessionStart(ctx)
	<-processes.entered
	mu.Lock()
	args = "two"
	mu.Unlock()
	ext.SessionStart(ctx)
	close(processes.gate)
	ext.Pending()
	waitFor(t, "the replaced server to close", func() bool {
		started, running := processes.counts("files")
		return started == 2 && running == 1
	})
	ext.SessionShutdown()
	if _, running := processes.counts("files"); running != 0 {
		t.Fatalf("%d running after the shutdown, want 0", running)
	}
}

// D236: a session replacement hands the running stdio servers to the next session, whose extension registers their
// tools and calls them, without starting them again. An HTTP server is connected again.
func TestThePoolKeepsAStdioServerAcrossASessionReplacement(t *testing.T) {
	processes := newServerProcesses()
	pool := mcpext.NewConnectionPool()
	config := fixed(commandEntry("files"), urlEntry("docs"))
	cwd := t.TempDir()
	first, _ := poolSession(t, processes, pool, config)
	first.SessionStart(eventContext(cwd, &notifications{}))
	first.Pending()
	waitFor(t, "the first session's servers", func() bool {
		_, files := processes.counts("files")
		_, docs := processes.counts("docs")
		return files == 1 && docs == 1
	})
	first.SessionShutdownFor("new")

	second, host := poolSession(t, processes, pool, config)
	second.SessionStart(eventContext(cwd, &notifications{}))
	second.Pending()
	waitFor(t, "the tools in the second session", func() bool { return hasTool(host, "mcp__files__ping") && hasTool(host, "mcp__docs__ping") })
	if started, running := processes.counts("files"); started != 1 || running != 1 {
		t.Fatalf("files: started %d, running %d; want 1 and 1 (kept)", started, running)
	}
	waitFor(t, "the first session's HTTP connection to close", func() bool {
		started, running := processes.counts("docs")
		return started == 2 && running == 1
	})
	definition, ok := host.tool("mcp__files__ping")
	if !ok {
		t.Fatal("mcp__files__ping is not registered in the second session")
	}
	result, err := definition.Execute(t.Context(), "call-1", json.RawMessage(`{}`), nil)
	if err != nil || len(result.Content) == 0 {
		t.Fatalf("call through the kept server = %+v, %v", result, err)
	}

	second.SessionShutdown()
	for _, name := range []string{"files", "docs"} {
		if _, running := processes.counts(name); running != 0 {
			t.Fatalf("%s: %d running after quit, want 0", name, running)
		}
	}
}

// D236: the next session starts a server again when its entry or the working directory changed, and the pool closes
// the server the previous session left.
func TestThePoolDoesNotKeepAServerWhoseEntryOrDirectoryChanged(t *testing.T) {
	for _, tc := range []struct {
		name          string
		second        mcpext.McpServerEntry
		sameDirectory bool
	}{
		{"entry", commandEntry("files", "--other"), true},
		{"directory", commandEntry("files"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			processes := newServerProcesses()
			pool := mcpext.NewConnectionPool()
			cwd := t.TempDir()
			first, _ := poolSession(t, processes, pool, fixed(commandEntry("files")))
			first.SessionStart(eventContext(cwd, &notifications{}))
			first.Pending()
			first.SessionShutdownFor("new")
			if !tc.sameDirectory {
				cwd = t.TempDir()
			}
			second, _ := poolSession(t, processes, pool, fixed(tc.second))
			second.SessionStart(eventContext(cwd, &notifications{}))
			second.Pending()
			waitFor(t, "the previous session's server to close", func() bool {
				started, running := processes.counts("files")
				return started == 2 && running == 1
			})
			second.SessionShutdown()
			if _, running := processes.counts("files"); running != 0 {
				t.Fatalf("%d running after quit, want 0", running)
			}
		})
	}
}

// D236: a session replacement does not wait for the servers it closes to exit; the shutdown that ends the process
// does.
func TestAReplacementClosesServersInTheBackground(t *testing.T) {
	processes := newServerProcesses()
	processes.closeGate = make(chan struct{})
	pool := mcpext.NewConnectionPool()
	cwd := t.TempDir()
	first, _ := poolSession(t, processes, pool, fixed(urlEntry("docs")))
	first.SessionStart(eventContext(cwd, &notifications{}))
	first.Pending()
	replaced := make(chan struct{})
	go func() {
		first.SessionShutdownFor("new")
		close(replaced)
	}()
	select {
	case <-replaced:
	case <-time.After(10 * time.Second):
		t.Fatal("the replacement waited for the server to exit")
	}
	second, _ := poolSession(t, processes, pool, fixed())
	second.SessionStart(eventContext(cwd, &notifications{}))
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(processes.closeGate)
	}()
	second.SessionShutdown()
	if _, running := processes.counts("docs"); running != 0 {
		t.Fatalf("%d running after quit returned, want 0: quit must wait for the pool", running)
	}
}

// Without a pool (Pi's behavior) a session replacement closes every server and the next session starts it again.
func TestWithoutAPoolAReplacementStartsTheServersAgain(t *testing.T) {
	processes := newServerProcesses()
	cwd := t.TempDir()
	first, _ := poolSession(t, processes, nil, fixed(commandEntry("files")))
	first.SessionStart(eventContext(cwd, &notifications{}))
	first.Pending()
	first.SessionShutdownFor("new")
	if _, running := processes.counts("files"); running != 0 {
		t.Fatalf("%d running after the replacement, want 0", running)
	}
	second, _ := poolSession(t, processes, nil, fixed(commandEntry("files")))
	second.SessionStart(eventContext(cwd, &notifications{}))
	second.Pending()
	waitFor(t, "the server to start again", func() bool {
		started, running := processes.counts("files")
		return started == 2 && running == 1
	})
	second.SessionShutdown()
}
