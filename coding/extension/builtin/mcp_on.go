//go:build !pig_strip_mcp

package builtin

import (
	"github.com/MichaelKinsy/PiG/coding/mcpext"
	"github.com/MichaelKinsy/PiG/coding/mcpext/mcptui"
	"github.com/MichaelKinsy/PiG/internal/pigstrip"
)

// McpOptions configures the MCP extension.
type McpOptions = mcpext.Options

// mcpEntries is the mcp registry row, absent while MCP is stripped at runtime as in a build without it.
// pig additive (D92): a runtime stripped mcp registers nothing, like the pig_strip_mcp build.
func mcpEntries(options Options) []Extension {
	if pigstrip.Has(pigstrip.ListExtensions, "mcp") {
		return nil
	}
	mcp := options.Mcp
	if mcp.Terminal == nil {
		mcp.Terminal = mcptui.Terminal() // The pig binary shows MCP in the terminal; embedders that want it headless use mcpext directly.
	}
	return []Extension{{Name: "mcp", Replaceable: true, Factory: mcpext.CreateMcpExtension(mcp)}}
}

// processMcpPool is the one connection pool of the process: every session's MCP extension that [Options.KeepMcpServersAcrossSessions] configured shares it.
var processMcpPool = mcpext.NewConnectionPool()

// KeepMcpServersAcrossSessions makes a session replacement (`/new`, `/resume`, `/fork`, `/reload`) hand the running stdio
// MCP servers to the next session instead of stopping and starting them, and close the servers it does not keep in the
// background.
// pig divergence (D236): Pi stops every server on session_shutdown, waits for it, and starts them again.
func (o *Options) KeepMcpServersAcrossSessions() { o.Mcp.Pool = processMcpPool }

// RefuseMcpConfigFiles makes the MCP extension start no server from `mcp.json` and report message instead. An empty message accepts them.
func (o *Options) RefuseMcpConfigFiles(message string) { o.Mcp.RefuseConfigFiles = message }

// ConfigureMcp sets where the MCP extension reads `mcp.json` and keeps its credentials and log (the agent directory and the
// per-project configuration directory name), how it opens an OAuth authorization URL, and, when given, how it copies one.
func (o *Options) ConfigureMcp(agentDir, configDirName string, openURL func(url string), copyToClipboard ...func(text string) error) {
	o.Mcp.AgentDir, o.Mcp.ConfigDirName, o.Mcp.OpenURL = agentDir, configDirName, openURL
	if len(copyToClipboard) > 0 {
		o.Mcp.CopyToClipboard = copyToClipboard[0]
	}
}
