### Added

- **runners:** `runners pause` and `runners resume` take `--service-user`
  A runner enrolled with `runners add --service-user` keeps its config in the
  service account's home, which `--config` could not reach. `--service-user`
  targets it the way `runners delete --service-user` does: sudo runs the step
  as that OS user, which sets or clears the pause beside its own credential.
