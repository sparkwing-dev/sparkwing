### Fixed

- **engine:** Avoid a second executable-sized allocation when verifying cached releases
  Verification hashes into an owned snapshot, preserving both release signatures and immutable installation bytes.
