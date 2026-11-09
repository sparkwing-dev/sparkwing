### Changed

- **release:** Run local release checks in sequence
  Build, lint, short tests and published-version checks no longer compete for
  temporary build storage during a release cut.
