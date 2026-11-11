### Fixed

- **Landing:** affected Go tests use the shared host's CPU allowance even when the submitting shell sets `GOMAXPROCS` or `GOFLAGS`. The test job reserves the same allowance it uses and overrides process and saved Go build flags for short and critical tests while preserving other saved Go configuration.
