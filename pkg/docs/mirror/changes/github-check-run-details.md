### Added

- **github app:** Check runs on private repositories show job results and annotations
  A completed check run on a private repository now carries the failed job count in its title, each failed job's and
  step's first error line and every `Summary` in its summary, and the full masked errors in its text. Annotations that
  name a file appear inline on the commit and pull request, up to 200 per check run. Jobs and steps appear by
  position, never by ID. Public repositories keep the aggregate-only check, and the controller rereads visibility
  before every write that carries details. See [Check runs](docs/github-app.md#check-runs).
