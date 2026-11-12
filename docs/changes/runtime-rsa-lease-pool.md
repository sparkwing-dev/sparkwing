### Changed

- **tests:** Identity fixtures reuse released RSA keys from a process-local pool
  Keys are generated at runtime only when none are free. Live fixtures keep
  distinct keys, and cleanup closes each issuer before releasing its key.
