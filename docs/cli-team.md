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

- `show` -- Show the team's enrollment, claim ranking and child-run settings
- `set` -- Change the team's enrollment, claim ranking and child-run settings

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing team settings set`

Change the team's enrollment, claim ranking and child-run settings

Only team administrators change team settings. Changes are audited. Pass --members-can-add-machines, --claim-ranking, --max-children-per-root or --high-trust, separately or together. False requires administration for personal enrollment and ends personal machines that members without administration enrolled; setting it back to true does not revive them. Shared runner registration, token minting and revocation always require administration.

--max-children-per-root takes an integer from 1 to 1000 (default 50) and limits descendants across every depth, excluding retries. --claim-ranking takes distinct tiers from here, personal, github-actions, self-hosted and cloud, best first. A tier left out cannot take runs that select no runner. none means first come among every tier and default restores the default ranking (here,personal,github-actions,self-hosted, then cloud on Sparkwing Cloud). Cloud takes part only while the team has credits or a saved card; a self-hosted controller refuses it.

--high-trust true lets verified runs on plain-machine and persistent runners write the verified cache that fresh pods, Sparkwing Cloud and remote readers restore. Off (the default), those runs write only their own host's cache, because an earlier job on the same host could have tampered with what they archive. Turn it on only when every job those runners take is trusted.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. (required) |
| `--members-can-add-machines BOOL` | members can add machines Values: true, false. |
| `--high-trust BOOL` | high trust Values: true, false. |
| `--claim-ranking TIERS` | claim ranking |
| `--max-children-per-root COUNT` | Maximum descendants per root run (1 to 1000, default 50; retries excluded) |

## `sparkwing team settings show`

Show the team's enrollment, claim ranking and child-run settings

Show the team's members_can_add_machines setting and claim ranking. members_can_add_machines defaults to true and allows members to enroll only their own personal machines. The claim ranking orders who takes a run that selects no runner: here, personal, github-actions, self-hosted, cloud by default; none means first come. The child-run ceiling defaults to 50 descendants per root, excluding retries. high_trust defaults to false; see team settings set. Plain prints the boolean, comma-separated ranking, child-run ceiling and high_trust.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. (required) |
