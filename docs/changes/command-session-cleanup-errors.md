### Fixed

- **runner + test harness:** Report command-session cleanup failures
  The host retains session-capture and cancellation cleanup failures and fails
  the owning attempt at Close, even if the SDK step returns success. Command
  results preserve the observed exit code, signal and output. An optional
  `cleanup_error` diagnostic lets both SDKs report the post-execution failure;
  `start_error` remains reserved for commands that never started. Already-gone
  sessions remain successful. The
  queued-land cancellation fixture accepts Linux terminal states Z, X and x,
  retains its live-process assertion and reports process birth, group, session
  and step-error diagnostics on failure.
