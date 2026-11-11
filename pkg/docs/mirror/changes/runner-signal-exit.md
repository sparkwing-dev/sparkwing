### Fixed

- **runner:** Report a clean operator stop as a successful exit
  `start` and `warm` exit 0 after signal-only cancellation, so service managers
  keep a clean stop out of the failed state. Runtime and cleanup failures still
  exit 1.
