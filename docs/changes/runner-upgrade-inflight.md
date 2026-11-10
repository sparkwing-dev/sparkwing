### Fixed

- **Runners:** Keep accepted replacement engines available after an installed binary upgrade, so later jobs and children use their sealed bytes.
- **Runners:** Keep claims live during existing work before a routine automatic update, so awaited child jobs can finish. Log update drain entry and exit with versions, held units and the reason.
