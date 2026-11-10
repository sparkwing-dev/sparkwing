### Fixed

- **runner:** Cancelling a parent stops its awaited child without waiting for the work lease renewal
  Successful run-cancel requests wake the controller reaper after commit. Claimed jobs observe cancellation by reading only their own claim row every 3 s. The three-minute lease and cleanup authority remain unchanged.
