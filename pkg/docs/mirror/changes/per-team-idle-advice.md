### Changed

- **controller:** Idle poll advice follows each team's own queue
  The interval a controller suggests to an idle runner now grows from the last award or admitted run of the runner's own team. Every admitted run counts: CLI and API submissions, retries, child runs, scheduled runs and GitHub App runs, so a team's runners return to their 500 ms cadence as soon as its work is queued. Another team's activity no longer returns every runner on the controller to polling every 500 ms for 8 s. The Cloud launcher, which serves every team, still follows any team's activity.
