### Fixed

- **cli (Breaking):** New local homes create their own private secrets key on first need
  Generated and refreshed systemd and launchd units pin the selected key file.
  The default home's existing config key wins over home-local keys. Explicit
  key-file selections stay in use. A wrong or missing key names its file or
  inline source and its repair, including through the local secret-list API.
  A standalone controller logs that detail and returns a generic error, and
  a damaged sealed value is never blamed on the key.
  Redirected homes no longer borrow the shared config key. See the
  [migration guide](docs/migrations/_unreleased.md#local-secrets-keys-follow-the-state-home).
