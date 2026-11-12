### Fixed

- **services:** Run generated runner, controller and schedule units from their state home
  Existing managed definitions gain the working directory during refresh while
  retaining captured configuration and schedule cadence. Running processes keep
  their current directory; loaded launchd registrations also need a reload.
