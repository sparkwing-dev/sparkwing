### Changed

- **controller:** Idle poll advice follows each team's own queue
  The interval a controller suggests to an idle runner now grows from the last award, scheduled run or GitHub App run of the runner's own team. Another team's activity no longer returns every runner on the controller to polling every 500 ms for 8 s. The Cloud launcher, which serves every team, still follows any team's activity.
