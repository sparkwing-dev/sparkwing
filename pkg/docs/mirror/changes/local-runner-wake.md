### Changed

- **local runs:** An idle local runner waits for work instead of polling for it
  The local controller holds an idle runner's claim until a commit makes work
  claimable, for at most 10 s, so runs and released jobs start within
  milliseconds. An idle machine sends one claim and writes the runner's liveness
  once every 10 s instead of twice a second. Shared controllers are unchanged.
