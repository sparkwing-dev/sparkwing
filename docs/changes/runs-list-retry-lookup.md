### Fixed

- **store:** List runs without scanning the team's runs once per listed run
  Each listed or read run looks up its newest retry. On SQLite that lookup walked every run of the team in
  creation order, so a 51-run page of a 6,000-run store took several seconds. The lookup now searches the retry
  index directly. No schema change.
