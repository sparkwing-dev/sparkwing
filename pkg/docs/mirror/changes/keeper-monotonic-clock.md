### Added

- **logs:** Lease keeper diagnostics place their records on the monotonic clock
  On Linux every keeper record brackets its process origin on `CLOCK_MONOTONIC`.
  Expiries and inherited deadlines that carry a monotonic reading also appear as
  offsets from that origin. A failed renewal records its wire error code, and a cancellation request
  leaves a `cancel_requested` record before the keeper acts on it.
