### Fixed

- **runner:** Report a clean operator stop as a successful exit
  After SIGTERM or interrupt, `start` and `warm` exit 0 when only cancellation
  errors remain, including during DNS, credentials, bundle transfers and SDK
  setup. Cancellation without a shutdown signal, and genuine runtime or cleanup
  failures, including a child killed externally before cancellation, still exit 1.
  Hosted failures are reported as they occur; shutdown keeps a bounded summary
  with the first genuine failure and its count. Pending claim and acquisition
  failures remain visible. Windows SDK and Git owners distinguish their own
  termination from a prior external death or ordinary exit 1. Service drain deadlines are unchanged.
