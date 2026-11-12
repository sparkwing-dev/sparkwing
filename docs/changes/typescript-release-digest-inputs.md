### Fixed

- **release:** Keep unit-only TypeScript SDK edits out of release decisions
  Hosted and pre-tag checks include tracked SDK inputs by default, including new
  paths and automatically packed files such as LICENSE. Known unit tests,
  examples and unpublished notes
  no longer force SDK version bumps. The entire nativehost fixture tree remains
  included because Windows publication runs it and records it in receipts.
  Adopting the narrower input set changes existing digests and needs a new SDK
  version once. Recoveries of pre-change entries also refuse the new hash;
  published policy entries are unchanged.
