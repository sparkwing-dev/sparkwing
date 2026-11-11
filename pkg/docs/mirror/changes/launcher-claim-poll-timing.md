### Fixed

- **launcher:** Wait from claim response completion and honor controller poll advice to prevent idle-poll rate-limit warnings under variable request latency
  Retain advice across failed responses. Treat a wrapped first-claim rate limit as an INFO deferral while joined faults remain warnings.
  Stop quietly on parent cancellation, including Job teardown, while preserving warnings for independent faults and gateway failures.
