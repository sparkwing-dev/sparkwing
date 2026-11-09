### Added

- **CLI:** List and download committed job files with `sparkwing runs artifacts`
  JSON emits one record per file. Downloads verify size and SHA-256 and write to a new destination.

### Fixed

- **CLI:** Remove partial artifact downloads when Ctrl-C interrupts the transfer.
