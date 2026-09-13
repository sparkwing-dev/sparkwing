# Migrating to the next release

Two changes. The two installer scripts swapped names so the public one is the
file its URL implies, and the retired `dashboard` noun lost the parsing that
named its replacement.

## Installer paths

- **Before:** `bash install/install.sh` installed the runner as a systemd user
  unit or a launchd agent.
- **After:** `bash install/service-install.sh` does that. `install/install.sh`
  is now the public CLI installer -- the script `https://sparkwing.dev/install.sh`
  serves, which downloads a signed release and verifies it before installing.
- **Why:** the repository had no copy of the installer adopters actually run,
  so the script the release signs and the script the site serves could not be
  compared. `install/install.sh` is that copy, and the runner installer moved
  aside to give it the name.
- **Gotchas:** the old path still runs, so a stale command installs the CLI
  rather than failing. Self-hosting instructions that pipe answers into
  `install/install.sh` need the new path.

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
