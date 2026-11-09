### Security

- **runner (Breaking):** Windows batch commands refuse arguments containing cmd.exe metacharacters, and executable lookup searches only PATH or an explicit path
  This prevents command injection and environment expansion through `.cmd`/`.bat`
  shims and checkout-planted tools shadowing PATH. See the
  [migration guide](docs/migrations/_unreleased.md#windows-batch-arguments).
