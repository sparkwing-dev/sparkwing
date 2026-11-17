### Fixed

- **cli:** Cold local runs keep the installed CLI's controller across pipeline engine pins
  An older pinned pipeline can run after an upgrade migrates the store and the
  daemon stops. Burst runs also start the local controller before handoff so
  local fallback uses the installed CLI. A CLI that cannot open the store
  refuses with `sparkwing update` advice before any backup restore remedy.
