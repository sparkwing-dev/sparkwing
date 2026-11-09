### Fixed

- **controller:** Apply repository runner requirements before planning SDK pipelines that have no YAML entry

### Removed

- **config (Breaking):** Remove runner requirements from dispatch metadata because settings owns their precedence. Read the source declarations from `projectconfig.Config`. See the [migration guide](docs/migrations/_unreleased.md#dispatch-requirement-metadata).
