### Fixed

- **cli (Breaking):** New local homes create their own private secrets key on first need
  Generated and refreshed systemd and launchd units pin the selected key file.
  The default home's existing config key wins over home-local keys. Explicit
  key-file selections stay in use. Failed opens name their file or inline
  source and its repair, including through the secret-list API.
  Redirected homes no longer borrow the shared config key. See the
  [migration guide](docs/migrations/_unreleased.md#local-secrets-keys-follow-the-state-home).
