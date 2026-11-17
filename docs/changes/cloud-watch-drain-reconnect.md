### Fixed

- **runner:** Reopen the Cloud Pod watch immediately when draining consumes a clean timeout
  A pending controller claim no longer leaves the watch disconnected for an
  extra safety poll. Error events and early closes keep their retry delay.
