package mcpext

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/MichaelKinsy/PiG/coding/extension"
)

// ConnectionPool keeps the stdio server connections of a session that a session replacement ends, so the extension of
// the next session takes them over instead of starting the servers again. It also closes, in the background, the
// connections that no session takes over, so a replacement does not wait for a server to exit. One pool serves every
// session of a process; [Options.Pool] passes it to each session's extension.
//
// pig divergence (D236): Pi closes every server on session_shutdown and waits for it, and starts every server again on the next
// session_start.
type ConnectionPool struct {
	mu     sync.Mutex
	parked map[string]*Connection
	// closing counts the connections the pool closes in the background.
	closing sync.WaitGroup
}

// NewConnectionPool returns an empty pool.
func NewConnectionPool() *ConnectionPool { return &ConnectionPool{parked: map[string]*Connection{}} }

// park keeps connection under key until a session takes it, replacing (and closing) one parked under the same key.
func (p *ConnectionPool) park(key string, connection *Connection) {
	p.mu.Lock()
	previous := p.parked[key]
	p.parked[key] = connection
	p.mu.Unlock()
	if previous != nil && previous != connection {
		p.closeInBackground(previous)
	}
}

// take removes and returns the connection parked under key, or nil.
func (p *ConnectionPool) take(key string) *Connection {
	p.mu.Lock()
	defer p.mu.Unlock()
	connection := p.parked[key]
	delete(p.parked, key)
	return connection
}

// closeParked closes, in the background, every connection no session took.
func (p *ConnectionPool) closeParked() {
	p.mu.Lock()
	parked := p.parked
	p.parked = map[string]*Connection{}
	p.mu.Unlock()
	for _, connection := range parked {
		p.closeInBackground(connection)
	}
}

// closeInBackground closes connection on a goroutine the pool owns; [ConnectionPool.Close] waits for it.
func (p *ConnectionPool) closeInBackground(connection *Connection) {
	p.closing.Go(connection.Close)
}

// Close closes the parked connections and waits until every connection the pool closes has closed. The extension calls
// it on the session_shutdown that ends the process.
func (p *ConnectionPool) Close() {
	p.closeParked()
	p.closing.Wait()
}

// connectionOwner is the session a connection reports to: its extension, the session's lifetime, and the server and
// connection attempt it belongs to. A session that takes a connection over replaces it.
type connectionOwner struct {
	ext     *Extension
	session context.Context
	server  *server
	attempt *attemptToken
}

// current reports whether the connection's work still applies to its owner.
func (o *connectionOwner) current() bool {
	o.ext.mu.Lock()
	defer o.ext.mu.Unlock()
	return o.ext.attemptCurrentLocked(o.session, o.server, o.attempt)
}

// poolKey identifies a server connection a later session can take over: the same entry, started in the same working
// directory (a stdio server's `cwd` is relative to it).
func poolKey(entry McpServerEntry, cwd string) string {
	//portlint:allow jsonescape the key only identifies a connection inside this process; no one compares it with Pi's JSON
	key, _ := json.Marshal(struct {
		Name, Source, Scope, Override, Cwd string
		Config                             extension.McpServerConfig
	}{entry.Name, entry.Source, entry.Scope, entry.Override, cwd, entry.Config})
	return string(key)
}

// isStdio reports whether the entry starts a local process: only those are kept across sessions. An HTTP server costs
// no process, and its OAuth or provider-token authentication belongs to the extension that created it.
func isStdio(entry McpServerEntry) bool { return entry.Config.Command != "" && !entry.Config.IsHTTP() }
