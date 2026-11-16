### Fixed

- **runners:** `runners delete --service-user` deletes the root-only enrollment record
  The privileged step used to leave `<home root>/.enrollments/<account>.json`
  behind after it stopped the service. It now deletes the record once it has
  read the runner token from it, and names the path when the deletion fails.
