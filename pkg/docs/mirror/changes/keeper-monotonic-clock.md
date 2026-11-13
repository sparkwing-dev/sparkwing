### Added

- **logs:** Lease keeper diagnostics place their records on the monotonic clock
  On Linux every keeper record brackets its process origin on `CLOCK_MONOTONIC`.
  Expiries and inherited deadlines also appear as monotonic offsets from that
  origin. A failed renewal records its wire error code, and a cancellation request
  leaves a `cancel_requested` record before the keeper acts on it.

### Changed

- **runner:** A server-supplied lease expiry is held on the monotonic clock
  The keeper converts the expiry when it arrives, so a later wall-clock step no
  longer shortens or extends a running unit's lease.
