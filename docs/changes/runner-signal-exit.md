### Fixed

- **runner:** Report a clean operator stop as a successful exit
  After SIGTERM or interrupt, `start` and `warm` exit 0 when only cancellation
  errors remain, including during DNS, credentials, bundle transfers and SDK
  setup. Cancellation without a shutdown signal, and genuine runtime or cleanup
  failures, still exit 1. Service drain deadlines are unchanged.
