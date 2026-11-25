### Changed

- **controller:** An idle runner's claim poll costs the controller much less
  A claim poll from a team with nothing queued now returns after one indexed check instead of running the full claim search, which loaded every other live runner, the team's credit balance and its ranking. A poll that reports the same slots, labels and platform as the runner's last stored observation, while that observation is younger than a quarter of the placement liveness window and at most 15 s, writes nothing and takes no team lock. A changed observation is still written at once, so placement holds see slot changes immediately.
