### Fixed

- **cli:** Keep configuration and integrity refusals actionable in final errors
  Missing-secret and local-key refusals retain their configuration class and
  repair. Corrupt artifacts and compiled pipeline cache entries name the
  affected item and recovery action. Unreadable inputs, a missing TypeScript
  `main` field and invalid integers give a usable correction without exposing
  secret argument values or signed URLs.
