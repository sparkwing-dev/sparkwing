<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing version

Every `sparkwing version` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing version`

Inspect versions (CLI, SDK, sparks)

Reports the installed CLI version + build provenance, the
latest published release on GitHub (with a short network
fetch -- bounded by ~3s, fail-soft when offline), and the
.sparkwing/go.mod SDK pin + any sparks-* libraries declared
alongside it.

CLI comparison uses the same read-only release metadata and provenance
checks as 'sparkwing update --check', within one ~3s network budget.
cli_status and cli_reason distinguish a verified comparison from a local
build or missing metadata. SDK pins retain their semver comparison.

--offline skips the network fetch entirely; -o json emits the
structured report; -o plain prints semver lines (CLI then
latest) for shell pipelines.

### Subcommands

- `hold` -- Show, set, or clear the operator ceiling on CLI upgrades

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |
| `--offline` | Skip the network fetch for latest release |
| `--changelog` | Print the changelog for the installed release |

### Examples

```sh
# Human-readable card
sparkwing version

# Agent-readable record
sparkwing version -o json

# CLI semver only (scripts)
sparkwing version -o plain | head -n1

# Local-only (no network)
sparkwing version --offline

# Changelog for the installed release
sparkwing version --changelog

# Update the CLI binary
sparkwing update --cli

# Bump the SDK pin in this project
sparkwing update --sdk
```

## `sparkwing version hold`

Show, set, or clear the operator ceiling on CLI upgrades

A version hold is an operator-set ceiling that the tool enforces:
once set, 'sparkwing update' and 'sparkwing update --cli'
refuse to install anything beyond it, so an agent cannot perform a
major upgrade against operator instruction.

The ceiling shape controls its reach:

  vMAJOR.MINOR       caps a whole minor series -- every patch of that
                     minor is allowed, the next minor is refused
                     (v9.8 allows v9.8.7 and excludes v9.9.0, for example).
  vMAJOR.MINOR.PATCH exact ceiling -- nothing above that patch installs.

With no flags, prints the current hold and where it is set. The hold
persists in the user config (XDG_CONFIG_HOME or ~/.config/sparkwing/
version-hold); the SPARKWING_VERSION_HOLD environment variable
overrides the file for a shell or a whole fleet. Releases beyond the
hold still show in 'sparkwing version' so the operator sees what is
being deferred.

### Flags

| Flag | Description |
|---|---|
| `--set VERSION` | Set the ceiling (vMAJOR.MINOR or vMAJOR.MINOR.PATCH) |
| `--clear` | Remove the hold so upgrades are unrestricted |

### Examples

```sh
# Show the current hold
sparkwing version hold

# Hold the minor series at v9.8
sparkwing version hold --set v9.8

# Pin an exact ceiling
sparkwing version hold --set v9.8.7

# Lift the hold
sparkwing version hold --clear
```
