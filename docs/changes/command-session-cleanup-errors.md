### Fixed

- **runner + test harness:** Report command-session cleanup failures
  Host commands preserve session-capture and cancellation cleanup errors instead
  of reporting a successful leader exit while owned children remain. Cleanup
  errors stay visible when the command also exits or receives a signal. The
  queued-land cancellation fixture accepts Linux terminal states Z, X and x,
  retains its live-process assertion and reports process birth, group, session
  and step-error diagnostics on failure.
