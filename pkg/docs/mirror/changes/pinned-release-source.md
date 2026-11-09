### Changed

- **releases:** `sparkwing run release -- --commit SHA` cuts a selected source in a detached worktree while main keeps accepting lands. Publication tags the prepared release commit and merges its metadata into fresh main, preserving newer code and unreleased fragments.

### Fixed

- **releases:** Manual cuts run local verification when exact-source CI evidence cannot be reused. Release-cut discovery accepts default arguments and keeps the SDK pin stable until the final commit job starts.
