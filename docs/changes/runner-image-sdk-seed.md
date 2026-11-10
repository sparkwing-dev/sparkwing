### Fixed

- **runner:** Seed release images from the independently versioned SDK pin
  Image packaging reads the selected engine source's Go SDK requirement instead
  of fetching the SDK at the engine version. Bootstrap cache keys retain the
  engine version.
