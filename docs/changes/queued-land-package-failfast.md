### Changed

- **ci:** Queued Land stops admitting packages after an affected-test failure
  Packages already running finish and report their failures. The failed job
  lists skipped short and critical packages and exact commands to rerun the
  selected work. Module allocations are shared with collect-all execution,
  and the three store shards are admitted together as one package. Pre-push
  and Gate keep collecting all results.
