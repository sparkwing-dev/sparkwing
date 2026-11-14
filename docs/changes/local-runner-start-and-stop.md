### Fixed

- **cli:** `sparkwing run` refuses a run when no local runner can start
  It names the cause and the fix instead of queueing the run and waiting with no
  output. An unreachable systemd user manager reads as such, with
  `systemctl --user status` and `loginctl enable-linger $USER` as the fix.
  `sparkwing daemon status` prints the runner's state and any failure on a
  `runner:` line, and `sparkwing doctor` reports a `local_runner_unavailable`
  finding.
- **cli:** `sparkwing daemon stop` no longer leaves the controller half-stopped
  When the local runner still holds jobs at the end of the stop wait, the command
  names the runner and the controller exits on its own once that runner finishes.
  A second stop is no longer needed. `sparkwing daemon status` reports `stopping`
  until then, and a command that times out waiting for it says the controller is
  still stopping.
- **cli:** Commands started from a systemd service without a user manager name the cause
  With `INVOCATION_ID` set, sparkwing starts the controller in its own user scope.
  When the user manager is unreachable, the error now says so and how to restore
  it instead of printing the controller log.
