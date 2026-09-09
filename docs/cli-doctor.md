<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing doctor

Every `sparkwing doctor` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing doctor`

Inspect and repair abandoned local state

Inspects local state and repairs entries whose owners have stopped.
--dry-run reports proposed repairs. The command preserves live processes,
active daemon state, and cluster-scoped records.

Repairs cover home permissions, abandoned local run records, abandoned
box-slot locks, ended local concurrency records, and orphaned run directories.
Run-record repair requires a reachable daemon so held runs remain protected.
A held box-slot lock is reported with guidance to update the pipeline SDK.

Run-directory removal requires a local store with recorded runs and profiles
that all use that store. Directories written within the grace period remain.
Unaccounted directories are reported for inspection.

On POSIX systems, permission repair removes group, other, and special bits
while retaining existing owner access. The walk preserves symlinks without
following them. Windows access permissions are reported as unverified.

The report includes daemon reachability, repeated admission rejections,
version mismatches, quarantined ledgers, and capacity measurement problems.
It names the reset command for excessive learned demand floors.

Standalone stores are listed with run counts and the oldest run's age.
Inspect their records before deleting a store directory.

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
