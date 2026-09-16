# Migrating to the next release

This release aligns pipeline-plan range selection with its documented flags.
The command replacement is mechanical and does not change `sparkwing run`.

## Pipeline plan range flags

`sparkwing pipeline plan` no longer accepts the runtime-prefixed spellings.

**Before:**

```sh
sparkwing pipeline plan --name gate \
  --sw-start-at test --sw-stop-at lint
```

**After:**

```sh
sparkwing pipeline plan --name gate \
  --start-at test --stop-at lint
```

The removed spellings fail with an error that names the replacement and this
guide. `sparkwing run` keeps its existing `--sw-start-at` and `--sw-stop-at`
flags; only `pipeline plan` uses the unprefixed preview flags.
