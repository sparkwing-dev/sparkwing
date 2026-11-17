### Fixed

- **runners:** `runners delete --service-user` deletes the root-only enrollment record
  The privileged step used to leave `<home root>/.enrollments/<account>.json`
  behind after it stopped the service. `runners delete` now deletes the record
  through a second privileged step, `runners uninstall-service --revoked`, once
  the controller confirms the runner token is revoked. A failed revoke keeps
  the record, names the token, and asks you to rerun `runners delete`.
