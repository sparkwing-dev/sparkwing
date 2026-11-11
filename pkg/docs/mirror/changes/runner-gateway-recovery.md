### Fixed

- **runner:** Keep fresh claims alive through temporary non-JSON gateway failures
  during controller replacement, with bounded backoff, jitter and Retry-After
  advice. Held work keeps running while claims recover. Authentication and
  protocol failures still stop claims.
