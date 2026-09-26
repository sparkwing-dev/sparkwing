<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing secrets

Every `sparkwing secrets` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing secrets`

Manage secrets in this machine's local store or on a controller

Without --profile, reads and writes this machine's local secret store:
the secrets table of state.db in SPARKWING_HOME, which the sparkwing
daemon serves on its API socket and starts when needed. Local runs,
'sparkwing web' and these commands share it. Every value is sealed
under the key in ~/.config/sparkwing/secrets.key
($XDG_CONFIG_HOME/sparkwing when that variable is set), which the first
stored secret creates; SPARKWING_SECRETS_KEY (base64 of 32 bytes)
overrides the file and SPARKWING_SECRETS_KEY_FILE moves it. Set either
in the environment the daemon starts in.

With --profile PROF, reads/writes the named profile's controller.
Used for prod / staging secrets that the cluster needs at run
time. Pipelines declare a typed Secrets provider to resolve their secrets.
'secrets list' masks values; 'secrets get' prints them.

A local run reads every local secret, including an unscoped one that
is not shared, because the daemon answers this machine's own account
as its administrator. The local store no longer reads secrets.env or
config.env: the daemon imports each file once and leaves it in place.

### Subcommands

- `set` -- Store (or replace) a secret value
- `get` -- Print a secret's raw value to stdout
- `list` -- List secret names + metadata
- `delete` -- Remove a secret
- `rotate` -- Re-encrypt every stored secret under the current key

## `sparkwing secrets delete`

Remove a secret

Deletes the secret from the local secret store when --profile is omitted, or
from the named profile's controller. Pipelines that reference the name will fail to
resolve until the secret is re-added.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Secret name to remove (required) |
| `--pipeline NAME` | Remove the row owned by one pipeline; omit for the unscoped row |
| `--profile NAME` | Profile name (omit for the local store) |

### Examples

```sh
# Delete a local secret
sparkwing secrets delete --name API_TOKEN

# Delete a remote secret
sparkwing secrets delete --name API_TOKEN --profile prod
```

## `sparkwing secrets get`

Print a secret's raw value to stdout

Reads the local secret store when --profile is omitted, or the
named profile's controller. Prints only the raw value (no trailing newline)
so it can be piped into another command. Use 'secrets list' for metadata.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Secret name (required) |
| `--pipeline NAME` | Read the row owned by one pipeline, falling back to the unscoped row |
| `--profile NAME` | Profile name (omit for the local store) |

### Examples

```sh
# Fetch a local secret
sparkwing secrets get --name API_TOKEN

# Fetch a remote secret
sparkwing secrets get --name API_TOKEN --profile prod
```

## `sparkwing secrets list`

List secret names + metadata

Lists secret names and metadata from the local secret store when --profile is
omitted, or from the named profile's controller. Raw values are never printed by this
command.

### Flags

| Flag | Description |
|---|---|
| `--grep PATTERN` | Filter by name substring (case-sensitive) |
| `--profile NAME` | Profile name (omit for the local store) |

### Examples

```sh
# List local secrets
sparkwing secrets list

# List secrets on prod
sparkwing secrets list --profile prod

# Filter to API-related names
sparkwing secrets list --profile prod --grep API
```

## `sparkwing secrets rotate`

Re-encrypt every stored secret under the current key

Reads every secret the named profile's controller holds, or the local
store without --profile, and writes it back sealed under the key that
controller or this machine's daemon is running with now, in one
transaction. Run it after moving onto a new key with the old one still
configured as --secrets-previous-key-file or SPARKWING_SECRETS_PREVIOUS_KEY
(locally: set SPARKWING_SECRETS_KEY and SPARKWING_SECRETS_PREVIOUS_KEY and
run 'sparkwing daemon restart'); drop the old key once a rotation reports
nothing skipped. A value the controller was holding as plaintext comes out
encrypted too, which is how an existing install turns encryption on
without re-setting each secret by hand.

A row that opens under no configured key keeps the bytes it had and is
listed by name; the rest of the table still rotates. A controller
refuses when it has no key configured. Values never leave it: the
rotation opens and reseals them in place.

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Profile naming the controller to rotate (omit for the local store) |

### Examples

```sh
# Re-encrypt prod secrets under the current key
sparkwing secrets rotate --profile prod

# Re-encrypt local secrets after a key change
sparkwing secrets rotate
```

## `sparkwing secrets set`

Store (or replace) a secret value

Stores --value (or the contents of --file) in the local secret store
when --profile is omitted, or uploads it to the named profile's
controller. Replaces any existing secret with that name.
Prefer --file for long or multi-line values so the raw text
does not land in shell history. A local secret without --pipeline is
shared with every pipeline.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Secret name (unique per controller) (required) |
| `--value VALUE` | Secret value (prefer --file for long values) |
| `--file PATH` | Read value from file (keeps value out of shell history) |
| `--plain` | Store a configuration value visible in run logs. Values are masked by default. |
| `--pipeline NAME` | Scope the secret to one pipeline |
| `--shared` | Let every run read this unscoped secret. On a controller, without --pipeline or --shared the secret answers admin callers only; locally it is always shared. |
| `--profile NAME` | Profile name (omit for the local store) |

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
