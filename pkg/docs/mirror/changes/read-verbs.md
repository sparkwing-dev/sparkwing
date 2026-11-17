### Changed

- **cli (Breaking):** Read selected details with `pipeline show`, `repos show`, `secrets show` and `debug show`, and cache state with `cache status`
  The former spellings fail with a usage error naming the replacement. Flags,
  output fields and exit codes keep their meaning. See the
  [migration guide](docs/migrations/_unreleased.md#read-verbs-use-list-show-and-status).
