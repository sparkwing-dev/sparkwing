### Changed

- **runner:** The runner image no longer ships the Go toolchain's `testdata` directories
  They are fixtures for Go's own package tests, and pipelines do not read them, so
  removing them makes the image smaller and faster to pull. `go test` on the
  standard library or on `cmd` packages inside the runner image now lacks those
  fixtures. Building Go programs, including the tools under `src/cmd`, is unchanged.
