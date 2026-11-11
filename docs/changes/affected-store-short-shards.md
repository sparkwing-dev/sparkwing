### Changed

- **ci:** Affected Go checks split selected root store short tests into concurrent shards
  The three shards share the existing Go command allowance with other selected
  root packages. Smaller allowances keep the unsplit command. Full critical tests
  still run after the short commands finish, including when a shard fails.
