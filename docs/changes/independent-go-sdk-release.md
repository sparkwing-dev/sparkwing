### Changed

- **release:** Publish the Go SDK only when its tracked module source changes
  SDK tags use independent v0 minor versions and record a source digest. SDK
  tests and Go/TypeScript conformance run before publication. Engine cuts wait
  until the Go proxy serves the selected SDK version before updating their pins.
