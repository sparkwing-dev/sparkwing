### Fixed

- **cli:** `sparkwing pipeline upgrade` verifies a scaffolded TypeScript pipeline
  It no longer runs `tsc` when `.sparkwing/tsconfig.json` is absent, which
  printed tsc's help and reported that the pipeline code needed changes.
- **engine:** A moved TypeScript SDK pin installs the new SDK into an existing `node_modules`
  The run after an upgrade, or after any pin change, failed with "installed
  TypeScript SDK differs from the native lockfile pin" until `node_modules` was
  deleted.
