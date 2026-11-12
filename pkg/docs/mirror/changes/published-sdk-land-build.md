### Fixed

- **delivery:** Check root release builds against the published Go SDK before landing
  Full and affected gates, pre-push and merge rechecks compile the CLI, controller
  and runner for all six release OS/architecture targets without the workspace
  or an SDK replacement. Package-owned Markdown inputs also select this check,
  including deletions. Local SDK APIs can no
  longer hide an unpublished dependency. Publish the standalone SDK change and
  bump the root pin before using its new API.
