<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing upgrade

Every `sparkwing upgrade` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing upgrade`

Upgrade repository SDK pins

Bump the current repository's Go or TypeScript SDK pin to the signed latest permitted release, or --version. Resolve native lockfiles and verify compilation/type checking plus describe/plan with the exact verified engine before applying changes. Refuse revoked targets with their signed reason. --dry-run reports pin edits without changing files or installed services.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--version TAG` | SDK release tag to pin the repository to instead of the latest permitted release. |
| `--dry-run` | Show what would change without changing anything. |
