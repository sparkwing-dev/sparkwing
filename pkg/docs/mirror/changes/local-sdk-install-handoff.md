### Changed

- **cli:** Reuse a verified local Go SDK installation within one invocation
  The owner checks source and tool identities and the executable digest before
  reuse. Changed inputs take the full installation path.
