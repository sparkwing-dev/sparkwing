### Fixed

- **runner:** Retire the standalone local runner before closing its controller
  Standalone runs no longer leave a supervised runner waiting on a controller that has already exited.
  Stop retries against the same controller keep waiting for the retiring process after it enters draining.
