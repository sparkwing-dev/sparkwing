### Fixed

- **store:** Find timed-out approval gates without scanning every job in history
  The reaper looks for approval gates past their timeout every second. On SQLite that search read every job the
  store had ever recorded, about 9,000 pages on a store of 6,300 runs, to find the few gates awaiting approval. It
  now starts from the unresolved approvals index and looks up each gate's job by key, so it costs what the open
  gates cost. No schema change.
