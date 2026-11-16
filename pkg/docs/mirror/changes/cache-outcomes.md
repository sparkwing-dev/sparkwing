### Added

- **runner:** Each dependency cache reports what it did
  Every cache a job declares with `Cache(...)` writes one job-log line and one
  job annotation per attempt, such as `cache npm: miss (key 3f2a91c04b1e); saved
  4.1 MiB` or `cache npm: present on this host (key ...); not restored or saved`.
  `runs status -o json` lists them under `jobs[].annotations`. A missing key file,
  an empty cache directory after the job and a failed save are named instead of
  passing silently.

### Fixed

- **runner:** `Cache(Npm())` restores and saves on a machine whose npm cache starts empty
  The host asked npm for its cache directory before each job, and npm wrote a
  debug log and an update-notifier stamp into that directory even when the
  query failed. The cache then always looked populated, so it was never
  restored or saved, including on fresh Cloud workers. The query now runs
  with npm's log files and update notifier off.
