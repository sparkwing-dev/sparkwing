### Fixed

- **controller:** Private on-demand controllers exit after 20 idle minutes and retire their supervised runner
  Active runs, queued work, connected clients and held leases keep the controller alive.
  The OS account's default home and installed login services remain running.
  A `.sparkwing` directory under an overridden `HOME` counts as private when the account lookup identifies another home. Symlink aliases of the account default stay persistent. The next CLI call restarts a private controller.
  Failed runner retirement resumes serving and waits another full idle window.
  Unrelated schedule registrations do not disable private-home retirement.
