### Changed

- **controller:** An idle runner's claim poll costs the controller much less
  A claim poll from a team with nothing queued now returns after one indexed check instead of running the full claim search, which loaded every other live runner, the team's credit balance and its ranking. A poll that reports the same slots, labels and platform as the runner's last stored observation now only restamps that observation in one statement, with no team lock and no second authority walk. A changed observation still takes the locked write, and placement holds still count liveness from the runner's last poll.
