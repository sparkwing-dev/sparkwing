### Fixed

- **store:** Find timed-out approval gates through the existing approvals index
  On SQLite, the reaper starts with unresolved approval records and looks up each job by key. This avoids a scan
  over the job history. Unresolved records left by cancelled jobs can still be examined.
