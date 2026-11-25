### Changed

- **controller:** An idle runner's claim poll costs the controller much less
  A claim poll from a team with nothing queued now returns after one indexed check instead of running the full claim search, which loaded every other live runner, the team's credit balance and its ranking. The cost of an empty poll no longer grows with the number of runners in the team.
