### Added

- **runner:** Each dependency cache reports what it did
  Every cache a job declares with `Cache(...)` writes one job-log line and one
  job annotation when the job's last attempt finishes, such as
  `cache npm: miss (key 3f2a91c04b1e); saved 4.1 MiB` or
  `cache npm: present on this host (key ...); not restored or saved`.
  `runs status -o json` lists them under `jobs[].annotations`. A cache the job
  ran without says why: no key file in the checkout, a failed restore, or a
  failed save. An empty cache directory after the job is named too. Lines mask
  the job's secrets and show only paths inside the checkout.

### Fixed

- **runner:** `Cache(Npm())` restores and saves on a machine whose npm cache starts empty
  The host asked npm for its cache directory before each job, and npm wrote a
  debug log and an update-notifier stamp into that directory even when the
  query failed. The cache then always looked populated, so it was never
  restored or saved, including on fresh Cloud workers. The query now runs
  with npm's log files and update notifier off.
