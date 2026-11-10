### Fixed

- **runner:** Cancelling a parent stops its awaited child without waiting for the work lease renewal
  Claimed jobs observe cancellation by reading only their own claim row once per second. The three-minute lease and cleanup authority remain unchanged.
