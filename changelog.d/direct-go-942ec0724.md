### Added
- Add an opt-in compiled-in Go extension demonstration selected by `PIG_DIRECT_GO_DEMO_LOG`, with a local fake-provider Session test and native-versus-fused dispatch benchmarks.

### Fixed
- Keep native `appendEntry` bound when a CLI mode rebinds the Session's extension actions. <!-- fix: 942ec0724 -->
