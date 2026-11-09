### Added

- **CLI:** List and download committed job files with `sparkwing runs artifacts`
  Listings default to 40 files and support `--limit` and `--cursor`. JSON emits
  one record per file followed by continuation metadata. Downloads verify size
  and SHA-256 and write to a new destination.

### Fixed

- **CLI:** Remove partial artifact downloads when Ctrl-C interrupts the transfer.
- **CLI:** Keep signed storage URLs out of transfer errors and publish verified
  downloads atomically.
- **API:** Bound artifact listings and document the exact-path download selector.
