### Added

- **cli:** `sparkwing team delete` deletes the profile's team
  An owner passes `--confirm SLUG` with the team's slug. The controller revokes
  the team's tokens and cancels its runs at once, then removes its runs, logs,
  artifacts, cache, secrets and subscriptions within minutes. Credit and payment
  records stay. See [deleting a team or account](docs/auth.md#deleting-a-team-or-account)

### Changed

- **controller:** Name the support contact when refusing an owner's only team
  Deleting the only team means deleting the account, which an operator does.
  With `--support-contact` set, the 409 says whom to ask.

### Docs

- **docs:** Document how long deleted team data stays in backups
  See [deleted data in backups](docs/backup-restore.md#deleted-data-in-backups)
