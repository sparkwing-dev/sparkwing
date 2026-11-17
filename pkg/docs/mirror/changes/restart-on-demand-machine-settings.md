### Fixed

- **cli:** Restart reloads same-version on-demand controllers
  `sparkwing daemon restart` now replaces an on-demand controller even when its
  CLI version matches. Machine budget changes apply to the successor runner,
  while the old runner finishes held jobs under its previous limits. The
  restart receipt reports `restarted: true` after the replacement is ready.
