### Security

- **runner (Breaking):** Host commands refuse the System32 WSL Bash launcher with Git Bash guidance; an empty Git Bash program succeeds
- **runner (Breaking):** Windows batch commands run through cmd.exe with escaped arguments, and executable lookup searches only PATH or an explicit path
  Arguments to `.cmd`/`.bat` files are quoted and escaped the way Rust's
  standard library does after CVE-2024-24576, so cmd.exe metacharacters and
  `%VAR%` reach the batch file literally. Only arguments containing CR, LF or
  NUL are refused. Lookup no longer lets checkout-planted tools shadow PATH. See
  the [migration guide](docs/migrations/_unreleased.md#windows-batch-arguments).
