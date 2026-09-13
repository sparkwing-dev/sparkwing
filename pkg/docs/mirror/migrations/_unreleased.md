# Migrating to the next release

Two changes. One flag now refuses where it used to be ignored, and the retired
`dashboard` noun lost the parsing that named its replacement.

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
