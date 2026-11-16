### Added

- **runners:** Run a shared runner under a dedicated OS user
  `sparkwing runners add --service-user NAME` creates or reuses an unprivileged
  OS user, stores the runner credential and Sparkwing home in that user's home,
  and installs a systemd system service with `ProtectHome=yes` or a launchd
  daemon running as it. Pipeline code then cannot read the teammate's SSH keys,
  cloud credentials or browser data. `runners remove --service-user NAME` stops
  it and revokes the credential. See the new shared-runners page.
- **runners:** Warn about shared runners that can read personal credentials
  `runners add` warns when a shared runner enrolls into an account holding SSH
  keys, cloud credentials or browser profiles, and when `--repos` spans several
  repositories, which can then claim each other's jobs and secrets.
