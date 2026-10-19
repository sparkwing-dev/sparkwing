<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing billing

Every `sparkwing billing` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing billing`

Inspect Cloud standing and request a higher tier

### Subcommands

- `standing` -- Show the team's Cloud standing and latest tier request
- `request-tier` -- Ask the operators to grant High standing

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing billing request-tier`

Ask the operators to grant High standing

Ask the Sparkwing operators to grant High standing. Only team owners request it, and a team has at most one open request. The message reaches the operator with the team's standing and payment history. The operator approves by granting standing or declines with a reason; see the outcome with sparkwing billing standing. JSON returns the stored request; plain prints its ID.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. (required) |
| `--tier TIER` | tier Values: high. (required) |
| `--message TEXT` | message (required) |

## `sparkwing billing standing`

Show the team's Cloud standing and latest tier request

Report the team's Cloud standing, the tier its payment history earns, the highest tier prepaid funds can reach and its latest standing request with the operator's decision. Prepaid funds raise standing at most one tier above the history tier. Plain prints the standing tier.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. (required) |
