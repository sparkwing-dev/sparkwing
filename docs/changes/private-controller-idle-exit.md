### Fixed

- **controller:** Private on-demand controllers exit after 20 idle minutes and retire their supervised runner
  Active runs, queued work, connected clients and held leases keep the controller alive.
  The default home and installed login services remain running. The next CLI call restarts a private controller.
