### Fixed

- **controller:** Keep billing checkpoints within their awarded range after a restart or backward clock step, so valid claim releases and settlement can finish
  Log a startup warning when the restored billing clock is more than one minute ahead of wall time.
