### Added

- Piglet Binary builds accept an explicit per-extension `realization: direct` for native Go factories through `coding/extension.API` (D234). Other members retain the fused SDK path.

### Fixed

- Ordinary Piglet builds no longer import the manually linked MyPiglets core demonstration from the stock entry point. <!-- fix: dc65da418 -->
- Direct component verification rejects a non-extension component even when a decoded plan marks it as Go and binary-materialized. <!-- fix: c0efe40dd -->
