<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing secrets

Every `sparkwing secrets` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing secrets`

Manage secrets (local dotenv or controller-stored)

Without --profile, reads/writes the laptop dotenv at
~/.config/sparkwing/secrets.env (masked) or
~/.config/sparkwing/config.env (--plain). Used by jobs invoked
through 'sparkwing run <pipeline>' locally.

With --profile PROF, reads/writes the named profile's controller.
Used for prod / staging secrets that the cluster needs at run
time. Pipelines pull a secret by listing it in the
sparkwing.yaml 'secrets:' block. Raw values never transit the
CLI except via 'secrets get'.

### Subcommands

- `set` -- Store (or replace) a secret value
- `get` -- Print a secret's raw value to stdout
- `list` -- List secret names + metadata (never prints values)
- `delete` -- Remove a secret

## `sparkwing secrets delete`

Remove a secret

Deletes the secret row from the controller. Pipelines that reference the name will fail to resolve until the secret is re-added.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Secret name to remove (required) |
| `--profile NAME` | Profile name (default: current default) |

### Examples

```sh
# Delete a secret
sparkwing secrets delete --name API_TOKEN --profile prod
```

## `sparkwing secrets get`

Print a secret's raw value to stdout

Prints only the raw value (no trailing newline) so it can be
piped into another command. Use 'secrets list' for metadata.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Secret name (required) |
| `--profile NAME` | Profile name (default: current default) |

### Examples

```sh
# Fetch a secret
sparkwing secrets get --name API_TOKEN --profile prod
```

## `sparkwing secrets list`

List secret names + metadata

Prints a table of name, created_at, and the principal that last updated each secret. Raw values are never printed by this command.

### Flags

| Flag | Description |
|---|---|
| `--grep PATTERN` | Filter by name substring (case-sensitive) |
| `--profile NAME` | Profile name (default: current default) |

### Examples

```sh
# List secrets on prod
sparkwing secrets list --profile prod

# Filter to API-related names
sparkwing secrets list --profile prod --grep API
```

## `sparkwing secrets set`

Store (or replace) a secret value

Uploads --value (or the contents of --file) to the controller
under --name. Replaces any existing secret with that name.
Prefer --file for long or multi-line values so the raw text
does not land in shell history.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Secret name (unique per controller) (required) |
| `--value VALUE` | Secret value (prefer --file for long values) |
| `--file PATH` | Read value from file (keeps value out of shell history) |
| `--plain` | Store as non-masked config (e.g. REGION, LOG_LEVEL) -- value will NOT be redacted in run logs. Default is masked. |
| `--profile NAME` | Profile name (default: current default) |

### Examples

```sh
# Set a masked secret (default)
sparkwing secrets set --name API_TOKEN --value abc123 --profile prod

# Set from a file
sparkwing secrets set --name TLS_CERT --file ./tls.crt --profile prod

# Set non-masked config
sparkwing secrets set --name REGION --value us-east-1 --plain --profile prod
```
