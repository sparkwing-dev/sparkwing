# Migrating to the next release

One rename, mechanical: the two installer scripts swapped names so the public
one is the file its URL implies.

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
