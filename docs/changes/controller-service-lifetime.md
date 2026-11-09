### Fixed

- Local controllers started from systemd services survive the calling unit's
  exit. Desktop updates preserve each controller's existing secrets key and
  configuration while restarting it.
