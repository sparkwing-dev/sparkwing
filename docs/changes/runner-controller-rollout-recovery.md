### Fixed

- **runner:** Tolerate controller rollouts with a ten-minute claim recovery budget
  Fresh claims retain exponential backoff and Retry-After advice while held work
  keeps its execution context and existing lease limits.
