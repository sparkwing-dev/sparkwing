<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing version

Every `sparkwing version` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing version`

Show + update versions (CLI, SDK, sparks)

Reports the installed CLI version + build provenance, the
latest published release on GitHub (with a short network
fetch -- bounded by ~3s, fail-soft when offline), and the
.sparkwing/go.mod SDK pin + any sparks-* libraries declared
alongside it.

Behind-by-version is computed via semver compare for both the
CLI itself and the SDK pin so an agent reading -o json can
trigger an upgrade without parsing prose.

--offline skips the network fetch entirely; -o json emits the
structured report; -o plain prints semver lines (CLI then
latest) for shell pipelines.

### Subcommands

- `update` -- Self-update CLI binary or bump SDK pin (requires --cli or --sdk)
- `hold` -- Show, set, or clear the operator ceiling on CLI upgrades

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
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
sparkwing version update --cli

# Bump the SDK pin in this project
sparkwing version update --sdk
```

## `sparkwing version hold`

Show, set, or clear the operator ceiling on CLI upgrades

A version hold is an operator-set ceiling that the tool enforces:
once set, 'sparkwing version update --cli' (and 'sparkwing update')
refuse to install anything beyond it, so an agent cannot perform a
major upgrade against operator instruction.

The ceiling shape controls its reach:

  vMAJOR.MINOR       caps a whole minor series -- every patch of that
                     minor is allowed, the next minor is refused
                     (e.g. v0.15 allows v0.15.9 but refuses v0.16.0).
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
| `--set VERSION` | Set the ceiling (e.g. v0.15 or v0.15.4) |
| `--clear` | Remove the hold so upgrades are unrestricted |

### Examples

```sh
# Show the current hold
sparkwing version hold

# Hold the minor series at v0.15
sparkwing version hold --set v0.15

# Pin an exact ceiling
sparkwing version hold --set v0.15.4

# Lift the hold
sparkwing version hold --clear
```

## `sparkwing version update`

Self-update the CLI binary (--cli) or bump this project's SDK pin (--sdk)

Two targets, one verb:

  --cli   Replace the running sparkwing binary with the target
          release. Resolves the version pointer from GitHub Releases,
          downloads + checksum-verifies the tarball, and atomically
          installs it. macOS arm64 binaries are ad-hoc-codesigned
          to avoid SIGKILL on first run.

  --sdk   Bump the SDK pin in this project's .sparkwing/go.mod via
          'go get github.com/sparkwing-dev/sparkwing@<version>',
          then 'go mod tidy'. Doesn't touch the running binary.

Exactly one of --cli or --sdk must be set; they conflict with
each other so a typo can't update the wrong half. --version
applies to whichever target is selected.

### Flags

| Flag | Description |
|---|---|
| `--cli` | Self-update the sparkwing CLI binary |
| `--sdk` | Bump the SDK pin in this project's .sparkwing/go.mod |
| `--version TAG` | Target release tag (e.g. v0.17.0). Omit for latest. |
| `--force` | Allow downgrading to an older release (--cli only) |
| `--override-hold` | Cross an operator version hold (--cli only) |

### Examples

```sh
# Update the CLI to latest
sparkwing version update --cli

# Pin the CLI to a specific release
sparkwing version update --cli --version v0.44.0

# Downgrade the CLI
sparkwing version update --cli --version v0.40.0 --force

# Bump the SDK in this project to latest
sparkwing version update --sdk

# Pin the SDK to a specific release
sparkwing version update --sdk --version v0.44.0
```
