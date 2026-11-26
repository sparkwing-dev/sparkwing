### Fixed

- **cli:** `pipeline plan` previews the jobs an ordinary local run on the same checkout would plan
  The preview's Plan now receives the git context that `sparkwing run` gives it: the checked-out branch, the commit
  SHA, the default branch, the checkout's repository and the local trigger. A Plan that adds a job only on `main`
  previously left that job out of the preview on `main`, even though the run on `main` executed it. On a detached HEAD
  the branch is empty in both commands. Pipeline discovery still runs without git context, as it does in a run.
  A run that uses `--cache-from` or the machine's cache profile identifies the repository by its origin rather than
  the checkout path, so a Plan that reads the repository can still differ from the preview in that case.
