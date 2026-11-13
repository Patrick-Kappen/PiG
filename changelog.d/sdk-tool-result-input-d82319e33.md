### Fixed

- Go extensions can read a `tool_result` event's input in order with `ctx.ToolInput()` and `ctx.ToolParams()`, including nested objects, number-like keys and lone UTF-16 surrogate halves. The input includes prepare-hook replacements and earlier `tool_call` handlers' edits. The host and wire contract are unchanged. <!-- fix: d82319e33 -->
