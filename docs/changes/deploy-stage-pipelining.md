### Changed

- **deploy:** Build and stage the next candidate during production promotion
  Separate job queues protect staging rollout plus gate and production promotion.
  Stale staging waiters stop before credentials or rollout. Concurrent GitOps
  writes preserve both environments and production rollback commits.
