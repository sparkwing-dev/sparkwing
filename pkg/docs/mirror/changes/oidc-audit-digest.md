### Security

- **controller (Breaking):** Schema 40 replaces retained OIDC audience and subject text with keyed digests
  The schema baseline advances to version 40. Startup scrubs historical rows
  before serving, using the external secrets
  keyring and a one-time audited append-only exception. Audit consumers must use
  `audience_digest`, `subject_digest` and `digest_key_id`. Retention stays unchanged.
  See the [migration guide](docs/migrations/_unreleased.md#upgrade-the-store-in-place-to-schema-baseline-40).
