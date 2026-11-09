### Fixed

- **runner:** Concurrent TypeScript jobs keep their SDK packages available during startup.
  SDK preparation serializes incremental npm installs using the selected native lockfile.
  npm owns installed-tree reuse and workspace configuration, so verbose diagnostics cannot trigger a destructive reinstall.
  A non-destructive npm check preserves frozen-lockfile validation before installation.
