### Fixed

- **runner:** Reuse signed engine verification without copying the executable for every job attempt
  Every reuse still checks the executable checksum and verification metadata. Corrupt entries fail with an integrity error instead of being silently replaced.
