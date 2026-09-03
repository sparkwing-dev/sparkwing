<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing doctor

Every `sparkwing doctor` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing doctor`

Diagnose and safely repair local state

Checks the sparkwing home for state that is safe to
remove because the process behind it is provably gone, repairs what it
finds, and reports everything -- so it is safe to run at any time and a
healthy machine reports a clean bill. It never kills a process, never
touches the admission daemon's live state, and never touches
cluster-scoped (global) rows.

It repairs five things: permissive files and directories in the Sparkwing
home; local run rows still marked running whose process
is gone and which the daemon does not know about; leftover box-slot lock
files from older binaries (a file whose owner is still alive is reported,
never removed); local-scope concurrency rows whose run has ended; and
run directories on disk whose run row no longer exists.

That last sweep only unlinks what the local state database can account
for. It leaves a run directory alone for ten minutes after anything last
wrote to it, and it removes nothing at all unless this user's profiles
describe the home being inspected, every one of them keeps run state in
that home's own SQLite file, and that store has recorded at least one
run. Everything else it finds is reported and left in place: a missing
local row is not evidence that the run is gone.

On POSIX systems, doctor removes group, other, and special permission bits
without granting new owner access; cached executables retain any existing
owner execute bit. The walk never follows symlinks. Windows access
is governed by DACLs that this portable check cannot inspect or repair,
so doctor reports the permission audit as unverified rather than healthy.

If an older-pinned pipeline binary is still admitting outside the daemon
through a held box-slot lock, doctor reports it and points at the fix --
bump that repo's sparkwing pin -- rather than deleting live state.

Every report opens with the admission daemon's state -- serving (with its
version and protocol), none running, or unreachable. That line is always
there, because a sweep that never reached the daemon otherwise printed
the same counts a healthy machine prints. An unreachable daemon is not a
clean bill: the checks below it did not run, and the run-row repair is
skipped rather than risk finalizing a run that daemon is still holding.

It also reports (never repairs) standing problems that otherwise surface
only as opaque per-run failures: repeated admission rejections, a version
skew with the resident daemon, quarantined admission ledgers, and a
capacity profile poisoned by contention -- one whose learned demand floor
prices every run at the whole machine, named with the exact
runs stats --reset command that clears it.

Use --dry-run to report what it would repair without changing anything.

### Flags

| Flag | Description |
|---|---|
| `--dry-run` | Report what would be repaired without changing anything |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain |
| `--home DIR` | Sparkwing home to inspect (default: $SPARKWING_HOME or ~/.sparkwing) |

### Examples

```sh
# Diagnose and repair now
sparkwing doctor

# Report without changing anything
sparkwing doctor --dry-run

# Agent-readable report
sparkwing doctor -o json
```
