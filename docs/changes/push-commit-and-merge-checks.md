### Fixed

- **delivery:** Refuse main pushes whose source commit differs from checked-out HEAD
  Merge rechecks retain fast push contracts, qatrust vet and lint before running affected tests once.
- **delivery:** Select main tests against Git's advertised remote commit
  Main updates must fast-forward. Creating main runs every committed Go module's short tests and full critical set.
