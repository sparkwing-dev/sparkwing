<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing secrets

Every `sparkwing secrets` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing secrets`

Manage secrets (local dotenv or controller-stored)

Without --profile, reads/writes the laptop dotenv at
~/.config/sparkwing/secrets.env (masked) or
~/.config/sparkwing/config.env (--plain), under
$XDG_CONFIG_HOME/sparkwing when that variable is set. Used by jobs
invoked through 'sparkwing run <pipeline>' locally.

With --profile PROF, reads/writes the named profile's controller.
Used for prod / staging secrets that the cluster needs at run
time. Pipelines pull a secret by listing it in the
sparkwing.yaml 'secrets:' block. Raw values never transit the
CLI except via 'secrets get'.

### Subcommands

- `set` -- Store (or replace) a secret value
- `get` -- Print a secret's raw value to stdout
- `list` -- List secret names + metadata
- `delete` -- Remove a secret

## `sparkwing secrets delete`

Remove a secret

Deletes the secret from local files when --profile is omitted, or from the named profile's controller. Pipelines that reference the name will fail to resolve until the secret is re-added.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Secret name to remove (required) |
| `--repo SLUG` | Remove the row owned by one repository slug (controller only); omit for the unscoped row |
| `--profile NAME` | Profile name (omit for local files) |

### Examples

```sh
# Delete a local secret
sparkwing secrets delete --name API_TOKEN

# Delete a remote secret
sparkwing secrets delete --name API_TOKEN --profile prod
```

## `sparkwing secrets get`

Print a secret's raw value to stdout

Reads local secret files when --profile is omitted, or the
named profile's controller. Prints only the raw value (no trailing newline)
so it can be piped into another command. Use 'secrets list' for metadata.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Secret name (required) |
| `--repo SLUG` | Read the row owned by one repository slug (controller only); omit for the unscoped row |
| `--profile NAME` | Profile name (omit for local files) |

### Examples

```sh
# Fetch a local secret
sparkwing secrets get --name API_TOKEN

# Fetch a remote secret
sparkwing secrets get --name API_TOKEN --profile prod
```

## `sparkwing secrets list`

List secret names + metadata

Lists secret names and metadata from local files when --profile is omitted, or from the named profile's controller. Raw values are never printed by this command.

### Flags

| Flag | Description |
|---|---|
| `--grep PATTERN` | Filter by name substring (case-sensitive) |
| `--profile NAME` | Profile name (omit for local files) |

### Examples

```sh
# List local secrets
sparkwing secrets list

# List secrets on prod
sparkwing secrets list --profile prod

# Filter to API-related names
sparkwing secrets list --profile prod --grep API
```

## `sparkwing secrets set`

Store (or replace) a secret value

Stores --value (or the contents of --file) in the local secret
files when --profile is omitted, or uploads it to the named profile's
controller. Replaces any existing secret with that name.
Prefer --file for long or multi-line values so the raw text
does not land in shell history.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Secret name (unique per controller) (required) |
| `--value VALUE` | Secret value (prefer --file for long values) |
| `--file PATH` | Read value from file (keeps value out of shell history) |
| `--plain` | Store as non-masked config (e.g. REGION, LOG_LEVEL) -- value will NOT be redacted in run logs. Default is masked. |
| `--repo SLUG` | Scope the secret to one repository slug (controller only) |
| `--shared` | Let every run read this unscoped secret (controller only). Without --repo or --shared the secret answers admin callers only. |
| `--profile NAME` | Profile name (omit for local files) |

### Examples

```sh
# Set a local masked secret
sparkwing secrets set --name API_TOKEN --value abc123

# Set from a file
sparkwing secrets set --name TLS_CERT --file ./tls.crt --profile prod

# Set non-masked config
sparkwing secrets set --name REGION --value us-east-1 --plain --profile prod

# Scope a secret to one repository
sparkwing secrets set --name DEPLOY_KEY --file ./key --repo acme/web --profile prod

# Let every run read one secret
sparkwing secrets set --name NPM_TOKEN --file ./npmrc --shared --profile prod
```
