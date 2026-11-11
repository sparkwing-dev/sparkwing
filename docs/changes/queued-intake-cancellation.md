### Fixed

- **runner:** Cancelling a run during planning finishes it without waiting for its owner lease to expire
  Cancellation before plan acceptance creates no jobs or concurrency waiter and leaves other runs' capacity intact.
