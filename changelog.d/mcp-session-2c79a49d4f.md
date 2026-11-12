### Changed

- A session replacement keeps the running stdio MCP servers. `/new`, `/resume`, `/fork`, `/reload` and the RPC commands `new_session`, `switch_session`, `fork` and `clone` hand each connected stdio server to the next session when its entry and the working directory did not change, so the server does not start again. The other servers close in the background, and the replacement no longer waits for them to exit: with a server that ignores SIGTERM, such as the Azure MCP Server, every replacement waited 2.5 s. Pi stops and starts every server on each replacement (D236). Reconnect in `/mcp` restarts a kept server.

### Fixed

- In RPC mode, a session replacement no longer leaves a second process of each MCP server running. RPC mode starts a replaced session twice, and the second start replaced the servers of the first without closing them, so a server the first start had already spawned ran until PiG exited. The second start now takes over the first start's connections, and each server runs once. <!-- fix: 2c79a49d4f88174df7f246c4dbc3822c5e58cd2c -->
