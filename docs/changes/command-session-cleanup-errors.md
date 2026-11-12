### Fixed

- **runner + test harness:** Fail attempts when command-session cleanup fails
  The host retains session-capture and cancellation cleanup failures through
  Close, even if an SDK step returns success. Command results keep the observed
  exit code, signal and output using the existing published SDK fields.
  Test hosts also preserve completed command results when their adapter fails.
  `start_error` stays reserved for commands that never started. Already-gone
  sessions remain successful. The queued-land cancellation fixture accepts Linux
  terminal states Z, X and x, retains its live-process assertion and reports
  process birth, group, session and step-error diagnostics on failure.
