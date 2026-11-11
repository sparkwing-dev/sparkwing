### Security

- **controller (Breaking):** Reject constant-byte bootstrap administrator seeds
  Startup refuses the mounted weak file even on an initialized store, including
  after its credential was revoked. Generating a random replacement only repairs
  a fresh store. Existing installations must revoke or rotate any live weak
  bootstrap credential and remove the mounted file before restarting.
  See the [migration guide](docs/migrations/_unreleased.md#random-bootstrap-administrator-seeds).
