### Changed

- Go SDK: a tool's `PrepareArguments` (`ToolPrepareArgumentsFunc`) takes the arguments as the JSON text the host sent and returns JSON text, which the host validates and executes as it is. The hook sees the members in the order the model wrote them, as Pi's `prepareArguments` does, and a hook that returns its input leaves the call unchanged; the map form answered with the keys sorted. A nil return is the JSON `null`, as a nil map was.
- Go SDK: in a `tool_call` handler, `ctx.ToolInput()` and `ctx.ToolParams()` return the event's input in the model's member order; they were empty there.

### Upgrading

- Go extensions: change a `PrepareArguments` hook (`ToolPrepareArgumentsFunc`, also the hook argument of `ToolWithPrepareArguments`) from `func(map[string]any) (map[string]any, error)` to `func(json.RawMessage) (json.RawMessage, error)`. Decode `args` where the hook reads members, and return `args` to leave the call unchanged. `pig extension upgrade` names the change (rule `prepare-arguments-json-text`) but does not rewrite it.
