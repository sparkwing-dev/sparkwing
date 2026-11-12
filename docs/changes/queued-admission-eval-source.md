### Fixed

- **runner:** Jobs in full concurrency groups stay unclaimed so awaited child pipelines can run; admission races use bounded waits and backed-off polling
- **runner:** Self-hosted evaluators can acquire the sealed pipeline source after a generator's source job finishes
- **controller:** Group queue timeouts include unclaimed waiting and survive controller restarts without changing queue arrival order

### Changed

- **store (Breaking):** Schema 38 persists initial group waits and upgrades schema 37 stores automatically. Older binaries refuse the upgraded store. See the [migration guide](docs/migrations/_unreleased.md#group-queue-timeouts-and-store-schema-38)
