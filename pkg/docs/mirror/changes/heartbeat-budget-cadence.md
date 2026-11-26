### Fixed

- **controller:** Explain the recommended heartbeat budget by the real renewal cadence
  The `--heartbeats-per-runner-minute` help and the `heartbeatsPerRunnerMinute`
  chart comment now say that a shipped runner renews each claim it holds once a
  minute, so the recommended 1200 allows ten times that for about 120
  concurrent claims per runner. They previously described a 3-second heartbeat
  that no runner sends. No budget value or profile changes.
