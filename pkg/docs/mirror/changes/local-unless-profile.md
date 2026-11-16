### Removed

- **cli + config (Breaking):** The saved default profile is removed; commands use the local controller unless `--profile` names one
  `profiles set --default` is gone, `profiles list` and `profiles show` drop the `default` field and
  column, and `default: true` in the machine config fails the load with the migration instruction. Pass
  `--profile NAME` on each command that should reach a controller. See the
  [migration guide](docs/migrations/_unreleased.md#the-saved-default-profile-and-machine-run-route-are-removed).
- **cli + config (Breaking):** A bare `sparkwing run` stays local; machine `run.routing.route` is removed
  `--sw-route burst` applies the machine `run.routing` overflow preferences for one run, and
  `--sw-route cloud` still runs directly on Cloud. `run.routing.route` in the machine config fails
  the load and names the replacement. Managed git hooks therefore always run locally. See the
  [migration guide](docs/migrations/_unreleased.md#machine-runroutingroute).
