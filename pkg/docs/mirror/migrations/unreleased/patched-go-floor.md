## Patched Go build floor

Sparkwing and its Go SDK require Go 1.26.10 or later because older toolchains
can build against vulnerable standard libraries.

Before upgrading the SDK, install Go 1.26.10 or later and raise the consuming
module's `go` directive from `1.26` or `1.26.0` to `1.26.10` or later. Run
`go mod tidy` after updating the SDK pin. With automatic toolchain selection,
Go can download a suitable toolchain. With `GOTOOLCHAIN=local`, the installed
compiler must meet the floor or the build fails. Existing SDK pins retain their
previous requirements and receive no retroactive security protection.
