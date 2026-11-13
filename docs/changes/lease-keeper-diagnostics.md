### Added

- **orchestrator:** Record lease keeper timing in structured run logs
  OWNER and NODE traces expose renewal timing, effective expiry and watcher resets
  through existing run-log permissions. Human log views hide these best-effort
  diagnostics; JSON retains them and identifies dropped records. Diagnostic bytes
  share real-output cuts or the final seal instead of starting upload timers.
