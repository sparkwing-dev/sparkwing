### Fixed

- **cli:** `pipeline plan` previews the jobs a local run on the same checkout would plan
  The preview's Plan now receives the same git context `sparkwing run` gives it: the checked-out branch, the commit SHA,
  the default branch, the repository and the local trigger. A Plan that adds a job only on `main` previously showed that
  job as missing from the preview on `main`, even though the run on `main` executed it. On a detached HEAD the branch is
  empty in both commands.
