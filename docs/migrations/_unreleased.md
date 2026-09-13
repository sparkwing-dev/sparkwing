# Migrating to the next release

One change: the retired `dashboard` noun lost the parsing that named its
replacement.

## Retired `dashboard` noun

- **Before:** `sparkwing dashboard start` failed with an error naming
  `sparkwing serve start`.
- **After:** it fails as an unknown subcommand, the way any name the CLI does
  not define fails.
- **Why:** [v0.49.0](v0.49.0.md#serve-command) removed the command and its guide
  carries the full command map. The parsing that recognized the old spelling
  existed to carry readers across that release, and has.
- **Gotchas:** a script still calling `sparkwing dashboard` already failed; it
  now fails without naming `serve`. The v0.49.0 guide is where the map lives.
