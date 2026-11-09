### Removed

- **controller (Breaking):** Remove the unused engine compatibility query
  `/api/v1/capabilities` returns deployment features. The `engine_version` query
  no longer selects an engine release or its support window. Engine selection,
  signed version enforcement and runner policy keep their existing behavior.
  See the [migration guide](docs/migrations/_unreleased.md#engine-compatibility-query).
