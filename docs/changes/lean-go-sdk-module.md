### Changed

- **sdk + api (Breaking):** Split the Go SDK into its own module with a Go 1.25 minimum
  Pipeline import paths stay the same. SDK versions are independent of engine
  versions. Host-backed author tests import `github.com/sparkwing-dev/sparkwing/sparkwingtest`.
  See the [migration guide](docs/migrations/_unreleased.md#lean-go-sdk-module).
