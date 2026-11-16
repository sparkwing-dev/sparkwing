### Added

- **cli + runner:** `sparkwing runners pause` and `sparkwing runners resume`
  Pause stops the runner enrolled on this machine from claiming new work while
  its held jobs finish, and resume lets it claim again on its next poll without a
  restart. The pause is a local file beside the runner credential, so
  `sparkwing runners list` shows `paused` on this machine only; other machines and
  the console see the runner idle.
