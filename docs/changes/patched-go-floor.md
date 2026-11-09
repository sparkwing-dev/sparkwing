### Security

- **sdk + cli (Breaking):** Require Go 1.26.10 or later for source builds
  The module floor now rejects older toolchains, including with
  `GOTOOLCHAIN=local`, to prevent builds with vulnerable standard libraries.
  SDK adopters must raise their consuming module's `go` directive to at least
  `1.26.10` and use a matching or newer toolchain. Existing SDK pins are unchanged.
  See the [Go floor migration guide](docs/migrations/_unreleased.md#patched-go-build-floor).
