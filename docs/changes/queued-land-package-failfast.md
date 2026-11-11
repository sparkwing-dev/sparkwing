### Changed

- **ci:** Queued Land stops admitting packages after an affected-test failure
  Packages already running finish and report their failures. The failed job
  lists skipped short and critical packages and exact commands to rerun the
  selected work. Pre-push and Gate keep collecting all results.
