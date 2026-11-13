### Fixed

- **delivery:** Check root release builds against the published Go SDK before landing
  Full and affected gates, pre-push and merge rechecks compile the CLI, controller
  and runner against the published pin without the workspace or an SDK replacement.
  Per-land checks cover the host plus linux/arm64, sharing their compiler allowance.
  Full Gate and nightly checks cover all six release OS/architecture targets. Package-owned Markdown inputs also select this check,
  including deletions. Local SDK APIs can no
  longer hide an unpublished dependency. Publish the standalone SDK change and
  bump the root pin before using its new API.
