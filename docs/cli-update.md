<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing update

Every `sparkwing update` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing update`

Update the CLI binary

Updates the CLI binary; --cli names that target explicitly. It resolves the
latest published GitHub release unless --version names a specific release
tag. Bump this project's .sparkwing/go.mod SDK pin with
'sparkwing repos update --in-place'.

CLI updates verify Ed25519 signatures, the release digest and the version the
staged binary reports before atomic replacement. Verification failure is
terminal. --force permits a downgrade;
--override-hold crosses an operator CLI hold.

--check reads installed identity and release metadata without installing,
running Go, changing module files or writing caches. It honors --version. Exit 0 means current or ahead, 1 means an update is
available, and 2 means unknown, diverged or a check failure. Local SDK
replacements and unverified CLI provenance are reported as unknown.
A check does not verify downloadable assets or promise installation will work.

Output is pretty on a terminal and NDJSON otherwise. Checks emit one
update_check record; successful updates emit one update receipt. Progress
and failures go to stderr. Plain checks print the status word; plain updates
print the resulting version.

### Flags

| Flag | Description |
|---|---|
| `--cli` | Update the CLI binary (the only target; optional) |
| `--check` | Compare the selected target without changing it |
| `--force` | Allow a CLI downgrade |
| `--override-hold` | Cross an operator CLI version hold |
| `--version TAG` | Canonical release tag; omit for latest published release |
| `-o, --output FORMAT` | pretty \| json \| plain |

### Examples

```sh
# Check for a newer CLI release
sparkwing update --check

# Update the CLI
sparkwing update --cli

# Check a specific CLI release
sparkwing update --cli --check --version v9.8.7

# Downgrade the CLI
sparkwing update --cli --version v9.7.6 --force
```
