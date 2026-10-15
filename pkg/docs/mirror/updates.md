# Updating Sparkwing

The machine update command selects the installed CLI. Repository SDK pins are
separate. See the [upgrade grammar](cli-upgrade.md) for that selection.

## Preview a machine update

```sh
sparkwing update --dry-run
sparkwing update --dry-run --version v0.49.0 --output plain
```

A preview reads release metadata and the destination binary's embedded identity.
It does not install software, start a controller or change managed services.
Output defaults to JSON when piped and pretty on a terminal. Plain output is
the comparison status.

| Status | Exit | Meaning |
| --- | --- | --- |
| `current` | 0 | The installed release matches the selected release. |
| `ahead` | 0 | The installed release is newer than the selected release. |
| `update_available` | 1 | The selected release is newer. |
| `unknown` | 2 | The comparison could not establish release identity. |

The JSON record includes `installed`, `available`, `status` and any established
policy restriction in `blocked_reason`. Availability does not establish that
installation will succeed. An unreadable version hold or missing provenance
cannot produce a `current` result.

## Machine installation

The CLI coordinates verified release installation with the local controller's
managed-unit manager. Builds without that integration return `unavailable`
before replacing the installed binary. Preview remains available.

An integrated update verifies release signatures, version identity and hashes
before and after atomic replacement. It then refreshes managed schedule units
and the installed controller service through one operation. The JSON success
receipt contains `managed_units: "refreshed"`. Plain output is the installed
version. A current CLI still refreshes its managed units so repeating an update
can recover from a previous refresh failure.

If refresh fails after replacement, the CLI remains installed. The command fails
without a success receipt and names the version, destination and retry command.
A download or verification failure leaves managed units untouched.

`--force` permits a CLI downgrade. `--override-hold` crosses an operator version
hold. Neither bypasses signature verification. `--cli`, `--sdk` and `--check`
are removed flags. Use `--dry-run` for previews.

See [Verified self-update](security.md#verified-self-update) for release trust
and [CLI update reference](cli-update.md) for flags.
