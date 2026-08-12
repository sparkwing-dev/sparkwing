<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing update

Every `sparkwing update` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing update`

Self-update the CLI binary

Downloads, verifies (signature + digest), and atomically
installs the latest (or a specific) sparkwing release from GitHub
Releases.

By default the command fetches the latest version pointer, pulls
the matching binary for the current OS/arch, verifies the ed25519
signature over SHA256SUMS with the public key compiled into this
binary, checks the download against the signed digest, replaces the
running binary via an atomic rename, then re-hashes the installed
file and requires it to equal the verified digest. macOS binaries
are ad-hoc-codesigned by the release before hashing, so nothing is
mutated after verification. A signature, digest, download, or install
failure is terminal -- there is no 'go install' fallback.

--check is the read-only probe: it reports the installed version
and the latest published release, exits 0 when already current,
and exits 1 when a newer release exists (useful for CI/notifications).

Downgrades are blocked by default. Pass --force to install an older
release (e.g. bisecting a regression).

For SDK (go.mod) bumps, use 'sparkwing version update --sdk'.

### Flags

| Flag | Description |
|---|---|
| `--check` | Report installed vs latest; exit 1 if a newer release exists (read-only) |
| `--force` | Allow downgrading to an older release |
| `--override-hold` | Cross an operator version hold |
| `--version TAG` | Target release tag (e.g. v0.17.0). Default: latest. |

### Examples

```sh
# Check for a newer release (read-only)
sparkwing update --check

# Update to latest
sparkwing update

# Pin to a specific release
sparkwing update --version v0.44.0

# Downgrade to an older release
sparkwing update --version v0.40.0 --force
```
