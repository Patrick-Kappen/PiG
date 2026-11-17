### Added

- PiG `tool_result` handlers can set or clear `terminate` across all extension SDKs (D652). Omission preserves the tool's flag, later handlers win, and the run ends after a batch only when every finalized result terminates.
