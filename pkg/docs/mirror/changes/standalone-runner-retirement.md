### Fixed

- **runner:** Retire the standalone local runner before closing its controller
  Standalone runs no longer leave a supervised runner waiting on a controller that has already exited.
