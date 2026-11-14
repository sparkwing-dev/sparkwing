### Fixed

- **schedules:** `schedules list --timer` no longer calls a working timer stale
  It compared the timer's binary with the controller's own executable, so the
  result depended on which command started the controller. A timer installed in
  the last three minutes now reads healthy until its first tick.
- **schedules:** A timer that fails to install no longer leaves schedules armed
  Installation sets up the timer before arming, and a rerun enables a timer
  whose files were written but never enabled. `schedules list` warns when armed
  schedules have no working timer.
- **schedules:** `schedules uninstall` no longer leaves a failed timer in the user manager
  It stops the timer before unlinking its units and clears their failed state.
