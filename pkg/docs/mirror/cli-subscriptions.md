<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing subscriptions

Every `sparkwing subscriptions` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing subscriptions`

Manage the team's run event subscriptions

### Subcommands

- `create` -- Subscribe a URL to run events
- `delete` -- Delete a run event subscription
- `list` -- List run event subscriptions
- `rotate-secret` -- Replace a subscription's signing secret
- `show` -- Show a run event subscription

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing subscriptions create`

Subscribe a URL to run events

Create a team run event subscription through the team-admin route with an idempotency key. Sparkwing posts each matching run.started and run.finished event to --url as CloudEvents 1.0 JSON signed with Standard Webhooks headers.

A controller accepts only an https URL whose host is a public name or address; the local controller also accepts http and loopback receivers. Omitted --event selects run.finished when --status is given and both events otherwise; --status applies only to run.finished, and every other omitted filter matches all values. A team holds at most 20 subscriptions.

The signing secret is printed once and never shown again; rotate-secret replaces it. JSON emits the subscription and its secret; plain emits the ID, then the secret.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--url URL` | Receiver URL; it must not carry a username, password or fragment. (required) |
| `--event EVENT` | Events to receive; omitted, run.finished when --status is given and both otherwise. Values: run.started, run.finished. Repeatable; comma-separated values are accepted. |
| `--status STATUS` | Receive run.finished only for these final statuses; omitted, every status. Values: success, failed, cancelled, dry_run. Repeatable; comma-separated values are accepted. |
| `--pipeline NAME` | Receive events only for these exact pipeline names; omitted, every pipeline. Repeatable; comma-separated values are accepted. |
| `--repo KEY` | Receive events only for these repository keys, as run JSON reports them; omitted, every repository. Repeatable; comma-separated values are accepted. |
| `--description TEXT` | Note shown with the subscription, at most 200 characters. |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |

## `sparkwing subscriptions delete`

Delete a run event subscription

Delete a subscription and its pending deliveries through the team-admin route. JSON retains whether it was deleted or already absent and exits 0 either way; pretty and plain emit the deleted ID, and report a subscription the team does not hold as not found with a non-zero exit.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--subscription ID` | Subscription ID, as subscriptions list shows it. (required) |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |

## `sparkwing subscriptions list`

List run event subscriptions

List the team's run event subscriptions without their secrets; only team administrators may read them, because a URL can carry a credential. JSON emits one record per subscription; plain emits IDs.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |

## `sparkwing subscriptions rotate-secret`

Replace a subscription's signing secret

Replace a subscription's signing secret and print the new one once. Signing switches at once; deliveries the receiver rejects meanwhile are retried for 24 hours. JSON emits the subscription and its secret; plain emits the secret.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--subscription ID` | Subscription ID, as subscriptions list shows it. (required) |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |

## `sparkwing subscriptions show`

Show a run event subscription

Show one subscription without its secret. JSON emits the subscription; plain emits its URL.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--subscription ID` | Subscription ID, as subscriptions list shows it. (required) |
| `--profile NAME` | Controller connection profile from your config. Optional profiles use the saved default, then local; --profile= selects local. |
