### Added

- **landing:** Queue focused owned-hardware lands as detached Sparkwing runs
  Use `sparkwing run land --sw-detached --sw-route local --sw-allow-risk destructive,prod -- --queued --brief 'Goal'`.
  Entries share a serial slot, merge main before checks, and retain dashboard
  status, logs and a run ID that lanes can follow. Failed entries release the
  slot for the next land; changed checkouts and refused pushes fail safely.
