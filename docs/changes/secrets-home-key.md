### Fixed

- **cli (Breaking):** New local homes create their own private secrets key on first need
  Generated and refreshed systemd and launchd units pin the selected key file.
  Existing default-home keys and explicit key-file selections stay in use.
  Failed opens name the selected key and suggest `SPARKWING_SECRETS_KEY_FILE`.
  Redirected homes no longer borrow the shared config key. See the
  [migration guide](docs/migrations/_unreleased.md#local-secrets-keys-follow-the-state-home).
