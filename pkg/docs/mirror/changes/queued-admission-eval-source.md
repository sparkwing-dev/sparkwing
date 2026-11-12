### Fixed

- **runner:** Jobs in full concurrency groups stay unclaimed so awaited child pipelines can run; admission races use bounded waits and backed-off polling
- **runner:** Self-hosted evaluators can acquire the sealed pipeline source after a generator's source job finishes
- **controller:** Group queue timeouts include unclaimed waiting and survive controller restarts without changing queue arrival order
- **controller:** Claim searches skip full groups together so large fan-outs do not delay runnable child jobs

### Changed

- **store (Breaking):** The schema baseline advances to version 38 to persist initial group waits. Schema 37 stores upgrade automatically. Older binaries refuse the upgraded store. See the [migration guide](docs/migrations/_unreleased.md#upgrade-the-store-in-place-to-schema-baseline-38)
