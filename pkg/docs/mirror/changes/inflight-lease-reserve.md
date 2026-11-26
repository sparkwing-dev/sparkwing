### Changed

- **controller:** A team over its request budget keeps its running jobs alive
  When `--requests-per-token-minute` refuses a team, a reserve of one eighth of that budget, on top of it, still admits lease renewals, node transitions and step start and finish from a claim or eval credential whose lease is current. Running jobs now slow down instead of losing their lease or their result. Snapshot reads, claim polls, log writes, console requests, result replays and publisher credentials never use the reserve. A team's sustained ceiling becomes 9/8 of the budget, and its worst one-minute window 9/4 of it instead of 2 times. A refusal from the spent reserve is counted as `inflight_reserve` in `sparkwing_principal_throttled_total`.
- **runner:** A rate-limited job result is retried until it is recorded
  A node transition the controller refuses with `rate_limited`, including the final result, now retries after its Retry-After plus jitter until it is recorded, the lease ends, or one lease length passes after the first refusal. It previously gave up after the client's four attempts and lost the result.
