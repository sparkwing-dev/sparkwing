### Changed

- **controller (Breaking):** Object presence requests require an explicit deadline and return no partial result after cancellation. See [the migration guide](docs/migrations/_unreleased.md#bound-object-presence-requests).

- **controller (Breaking):** Remove the ineffective `trust_local_builds` setting and its team API.
  The schema baseline advances to version 37. Existing configured data upgrades
  in place. See [the migration guide](docs/migrations/_unreleased.md#upgrade-the-store-in-place-to-schema-baseline-37).
  Execution trust continues to use sealed provenance and host facts.
- **runs:** Select latest pipeline observations through one store owner. CLI
  repository status and dashboard summaries use the same start-time, run-number
  and run-ID order. Reference authorization and success-pin policy stay unchanged.
