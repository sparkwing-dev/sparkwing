### Fixed

- **runner:** An enrolled runner with `local_admission` claims no jobs while no local controller runs
  Previously each claimed job failed with the fix in its error. The runner now
  withdraws its job capacity until the local controller runs a build it can speak
  to, keeps planning, and logs one `runner idle` line with the fix. It logs
  `runner takes jobs again` when the controller answers.
