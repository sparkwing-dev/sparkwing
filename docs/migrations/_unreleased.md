# Migrating to the next release

One rename, mechanical: the two installer scripts swapped names so the public
one is the file its URL implies. One flag now refuses where it used to be
ignored.

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

## `sparks update --name`

- **Before:** `sparkwing pipeline sparks update --name LIB` checked that LIB
  was declared and then re-resolved every library in the manifest, so pinning
  one library forward bumped the rest in the same overlay write.
- **After:** the flag is refused. `sparkwing pipeline sparks update` with no
  flag re-resolves the whole manifest, which is what the command has always
  done.
- **Why:** resolution rebuilds the overlay modfile from the whole manifest in
  one pass, so a filtered update would drop the other libraries' resolved
  versions. The flag's documented purpose was never implemented.
- **Gotchas:** a script or CI step passing `--name` now exits non-zero. Drop
  the flag to update everything, or give a library an exact `version:` in
  `.sparkwing/sparkwing.yaml` to hold it still while the others move.
