### Added

- **web:** Re-encrypt secrets from the operator console
  Verify stored values, type a confirmation, then re-encrypt and verify again
  with before and after counts retained during later verification.
  Refused writes report that nothing changed; network and server failures
  request verification before retrying. Requires a recent operator sign-in.
