### Changed

- **controller:** An idle runner's claim poll costs the controller much less
  When a team has none of the requested work queued, a team runner's claim poll now skips the full claim search, which loaded every other live runner, the team's credit balance and its ranking, after one indexed check. The cost of an empty poll no longer grows with the number of runners in the team.
