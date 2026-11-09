### Fixed

- Pipeline binary caches rebuild when effective Go settings, including FIPS mode, Git-ignored compiled and embedded inputs, Go file overlays or PGO profiles change. Writers sharing a linked cache leave automatic pruning to its owner; symlinked state homes retain owner limits.
