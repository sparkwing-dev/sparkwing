### Fixed

- **runner:** Concurrent TypeScript jobs keep their SDK packages available during startup
  SDK preparation serializes npm installs using the selected native lockfile and reuses completed installations for unchanged manifests, install environments and effective npm configuration.
