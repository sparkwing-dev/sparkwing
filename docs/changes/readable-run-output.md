### Changed

- **cli:** `sparkwing run` and `runs logs` print a quieter, labelled stream on a terminal
  Each line starts with its job, as in `test | running tests`, and the run ID appears
  only in the run's start line. Step boundaries read `unit started` and `unit failed
  after 23ms`. Scheduling events such as `job_ready`, `job_claimed` and
  `job_admission_granted`, source-preparation timings and the CLI's own `INFO` log lines
  no longer print there. JSON and plain output keep every record.
- **cli:** A failed run ends with a failure block
  The block names each failed job with its step and error, says which dependency
  stopped a cancelled job, and gives the `runs logs --run N --job JOB` and
  `runs retry --failed --run N` commands. The trailing "run status: failed" diagnostic no
  longer follows it, and the exit status stays 1.
- **orchestrator:** A failed run's reason lists cancelled jobs separately
  `failed: test; cancelled: deploy` replaces `failed: test, deploy`.

### Fixed

- **cli:** Multi-line errors print as real lines instead of one quoted string with `\n`
  This covers command errors, `runs status` job and step errors, and run errors in
  `runs logs`. Other control characters stay quoted. `runs status` shows an ordinary
  job failure as `step NAME failed` instead of `sdk_error`.
- **cli:** A Go compile error in pipeline code reports itself as one
  `sparkwing run` prints `the pipeline code in .sparkwing does not compile:` with the
  compiler's lines and file paths relative to the repository root, instead of
  `SDK install failed`. A build failure without compiler errors, such as a module
  download failure, says `failed to build`. In JSON output the error code changes
  from `internal` to `plan_failed`; the exit status stays 1.
