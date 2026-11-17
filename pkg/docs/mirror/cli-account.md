<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing account

Every `sparkwing account` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing account`

Manage your signed-in account

### Subcommands

- `delete` -- Preview and delete your account and sole-member teams

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing account delete`

Preview and delete your account and sole-member teams

Preview account deletion before confirming. Deletion requires a sign-in from the last 10 minutes. Sign out of the dashboard and sign in again when prompted, then supply the new direct account session with --session-stdin, never the profile token. The profile selects the controller. Without --confirm, show the account ID and teams deleted with it. Repeat the account ID with --confirm and every team slug with repeated --confirm-team flags. The server refuses a changed team list independently of database collation. Missing --confirm-team flags name the required list. Plain preview includes blocked_team=SLUG lines and fails when blocked. Teams with other members block deletion when you are their only owner: transfer ownership or delete them first. Sole-member teams and their data are deleted. Shared-team history stays with its actor relabeled. All account sessions and tokens are revoked immediately, including runner and CI tokens you created in shared teams. Arrange replacements to avoid breaking those integrations. RDS automated backups have a seven-day window, but older accounts can remain in encrypted operator archives beyond it. The preview quotes the dated archive notice from the account-deletion-retention documentation. Financial records, retained creator IDs and audit history remain (audit default 400 days). OIDC audit rows currently retain plain audience and subject strings, which can include personal text; deletion does not scrub them. Self-hosted backup expiry follows the operator policy. Prefer the dashboard Account page to avoid copying a session.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Controller connection profile from your config. Must be supplied explicitly. (required) |
| `--session-stdin` | Read an existing direct account session from stdin (required) |
| `--confirm ACCOUNT_ID` | Account ID from the preview |
| `--confirm-team SLUG` | Each team listed in the preview, repeated once per team Repeatable; comma-separated values are accepted. |
