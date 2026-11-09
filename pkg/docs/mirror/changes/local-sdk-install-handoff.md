### Changed

- **cli:** Reuse a verified local Go SDK installation within one invocation
  The owner checks source and tool identities and the executable digest before
  reuse. Changed inputs take the full installation path.
- **engine (Breaking):** Bind SDK installation to the selected engine identity
  The CLI and selected SDK installer use the updated internal installation
  protocol together.
