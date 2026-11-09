### Fixed

- Post-deploy scans preserve deploy provenance through native child execution, share one work slot on owned runners, and coalesce full examples per revision and hour. Deploys continue while scans await their children. Automatic full scans follow production, and superseded waiting scans stop before build work.

### Added

- Go SDK: `AwaitChild` observes a child started by the current job and returns its terminal status while releasing runner capacity.
