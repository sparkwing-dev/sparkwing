<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing billing

Every `sparkwing billing` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing billing`

Inspect the Cloud balance and standing, contact operators and set automatic refill

### Subcommands

- `balance` -- Show the team's prepaid credit balance
- `standing` -- Show the team's Cloud standing and latest tier request
- `request-tier` -- Contact operators for High standing, higher limits or larger payments
- `refill` -- Show or change the team's automatic refill

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing billing balance`

Show the team's prepaid credit balance

Show the team's prepaid Cloud balance in credits and dollars.

Report the team's prepaid Cloud balance in credits and its dollar value, as the billing page shows it. Both round toward zero, so the balance is never shown larger than it is, and usage can leave it negative. Pretty output also says when a hold or an empty balance stops Cloud work. JSON returns team, credits, prepaid_cents, balance_micro, frozen and any hold_cause; plain prints the credits.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Controller connection profile from your config. Must be supplied explicitly. (required) |

### Examples

```sh
# Show the balance
sparkwing billing balance --profile prod

# Print only the credits
sparkwing billing balance --profile prod -o plain
```

## `sparkwing billing refill`

Show or change the team's automatic refill

Show automatic credit refill settings, or change them with the refill flags. Changes require team ownership.

Show the team's automatic refill: whether it is on, the threshold and amount, the team's weekly and monthly limits, the refill caps its payment history allows, refills in the last 7 and 30 days, the last failure and the limit that stops the next refill. Any change flag updates the settings first, keeping each one not given; only team owners change them. When the balance falls below the threshold the saved card is charged the amount, between $10 and $1,000 at every standing tier. Contact us through sparkwing billing request-tier for larger payments. Turning refill on needs a saved card. A weekly or monthly limit of 0 is none. A refill a limit stops is not made, and Cloud work stops when the credits run out. JSON returns the refill state; plain prints on or off.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Controller connection profile from your config. Must be supplied explicitly. (required) |
| `--enable` | Turn automatic refill on; the team needs a saved card. |
| `--disable` | Turn automatic refill off, keeping its settings. |
| `--threshold DOLLARS` | Refill when the balance falls below this many dollars. |
| `--amount DOLLARS` | Dollars added by each refill, $10 to $1,000. |
| `--week-limit DOLLARS` | Most dollars refilled in any 7 days; 0 is no limit. |
| `--month-limit DOLLARS` | Most dollars refilled in any 30 days; 0 is no limit. |

## `sparkwing billing request-tier`

Contact operators for High standing, higher limits or larger payments

Ask the operators for higher Cloud standing or payment limits. Only team owners can send a request.

Contact the Sparkwing operators for High standing, higher limits or payments above $1,000. Describe what you need in the message, including if the team already has High standing. Only team owners request it, and a team has at most one open request. The message reaches the operator with the team's standing and payment history. The operator approves by granting standing or declines with a reason; see the outcome with sparkwing billing standing. JSON returns the stored request; plain prints its ID.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Controller connection profile from your config. Must be supplied explicitly. (required) |
| `--tier TIER` | Standing tier to request. Values: high. (required) |
| `--message TEXT` | What you need, sent to the operators with the team's standing and payment history. (required) |

## `sparkwing billing standing`

Show the team's Cloud standing and latest tier request

Show the team's Cloud standing and the outcome of its latest standing request.

Report the team's Cloud standing, the tier its payment history earns, the highest tier prepaid funds can reach and its latest standing request with the operator's decision. Prepaid funds raise standing at most one tier above the history tier. Plain prints the standing tier.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Controller connection profile from your config. Must be supplied explicitly. (required) |
