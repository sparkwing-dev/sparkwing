### Changed

- **toolchain:** Build Sparkwing and its runner images with Go 1.27.4
  The engine and pipeline modules still accept Go 1.26.0 or later, and the Go SDK
  still accepts Go 1.25.0 or later. The GitHub Actions migration recipe now pins
  Go 1.27.4.
