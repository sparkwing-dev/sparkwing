### Fixed

- **cli:** Cold local runs keep the installed CLI's controller across pipeline engine pins
  An older pinned pipeline can run after an upgrade migrates the store and the
  daemon stops. A CLI that cannot open the store refuses before engine handoff.
