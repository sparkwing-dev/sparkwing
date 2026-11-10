### Fixed

- **secrets:** Let concurrent local key creators agree on the published key
  A key appearing during a credential lookup no longer turns a missing-file observation into a fatal resolution error. Dangling credential links still fail closed.
