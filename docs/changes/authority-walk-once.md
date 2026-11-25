### Changed

- **controller:** Read a credential's authority ancestry twice per write transaction instead of on every check
  A write transaction now validates the ancestry, locks it, then verifies each ancestor once; later checks in the same transaction reuse that verification until the transaction writes. A lease renewal reads the ancestry 15 times instead of 47 and takes about half the store time. A read-only snapshot reads each ancestor once however many checks it makes, so a local-host snapshot read takes about 30% less store time. Authority and membership locks taken after a credit ledger lock are now refused, as team locks already were.
