### Removed

- **cli + config (Breaking):** The saved default profile is removed; commands use the local controller unless `--profile` names one
  `profiles set --default` is gone, `profiles list` and `profiles show` drop the `default` field and
  column, and `default: true` in the machine config fails the load as an unknown key. Pass
  `--profile NAME` on each command that should reach a controller. See the
  [migration guide](docs/migrations/_unreleased.md#the-saved-default-profile-is-removed).
