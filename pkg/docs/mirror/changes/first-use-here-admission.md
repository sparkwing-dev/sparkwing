### Fixed

- **cli:** Start local admission before submitting a remote `--runner here` trigger
  A fresh home can host its first job without starting a daemon by hand. Startup
  failures explain the repair before allocating a run, and resumed here runs
  restore local admission before claiming work.
