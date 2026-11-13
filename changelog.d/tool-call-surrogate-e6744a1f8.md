### Fixed

- Preserve lone UTF-16 surrogate units in `tool_call` input keys and values, including handler edits, as Pi does. A handler that replaces a lone half with U+FFFD now changes the executed arguments. <!-- fix: e6744a1f8 -->
