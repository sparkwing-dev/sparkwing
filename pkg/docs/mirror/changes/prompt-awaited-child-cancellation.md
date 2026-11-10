### Fixed

- **runner:** Cancelling a parent stops its awaited child without waiting for the work lease renewal
  Claimed jobs observe cancellation through the existing internal state notices. The three-minute lease and cleanup authority remain unchanged.
