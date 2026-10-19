<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing team

Every `sparkwing team` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing team`

Manage your team

### Subcommands

- `settings` -- Manage team settings

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing team settings`

Manage team settings

### Subcommands

- `show` -- Show the team's machine enrollment setting
- `set` -- Change the team's machine enrollment setting

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing team settings set`

Change the team's machine enrollment setting

Only team administrators change team settings. Changes are audited. Pass --members-can-add-machines, --claim-ranking or both. False requires administration for new personal enrollments and keeps existing machines registered. Shared runner registration, token minting and revocation always require administration.

--claim-ranking takes distinct tiers from here, personal, github-actions, self-hosted and cloud, best first. A tier left out cannot take runs that select no runner. none means first come among every tier and default restores here,personal,github-actions,self-hosted,cloud. Cloud takes part only while the team has credits or a saved card.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. (required) |
| `--members-can-add-machines BOOL` | members can add machines Values: true, false. |
| `--claim-ranking TIERS` | claim ranking |

## `sparkwing team settings show`

Show the team's machine enrollment setting

Show the team's members_can_add_machines setting and claim ranking. members_can_add_machines defaults to true and allows members to enroll only their own personal machines. The claim ranking orders who takes a run that selects no runner: here, personal, github-actions, self-hosted, cloud by default; none means first come. Plain prints the boolean and the comma-separated ranking.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. (required) |
