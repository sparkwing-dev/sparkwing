<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing secrets

Every `sparkwing secrets` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing secrets`

Manage secrets in this machine's local store or on a controller

### Subcommands

- `delete` -- Remove a secret
- `get` -- Read a secret's value
- `list` -- List secret names + metadata
- `rotate` -- Re-encrypt every stored secret under the current key
- `set` -- Store (or replace) a secret value

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing secrets delete`

Remove a secret

Delete the exact pipeline or unscoped row. JSON retains whether the row was deleted or already absent; plain emits the name.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--secret NAME` | Secret name. (required) |
| `--pipeline NAME` | Pipeline name. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing secrets get`

Read a secret's value

This explicit authorized read includes the value. JSON retains metadata and value, including an empty value. Pretty and plain emit the raw bytes without adding a newline.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--secret NAME` | Secret name. (required) |
| `--pipeline NAME` | Pipeline name. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing secrets list`

List secret names + metadata

List metadata without values, including pipeline visibility and pool restrictions. Search matches names case-insensitively. JSON emits metadata records; plain emits names.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--search PATTERN` | Match records; never scan log text. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing secrets rotate`

Re-encrypt every stored secret under the current key

Retain rotated counts and skipped row names/pipelines. The previous key is still needed when any row was skipped. JSON includes the controller's replay marker.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |

## `sparkwing secrets set`

Store (or replace) a secret value

Restricted --only-on secrets require matching pool demands at seal and reserved pools at release; restricted secrets are refused for here machines.

If neither --value nor --file is supplied, read the value from stdin. Preserve empty values and trailing newlines. --variable stores an unmasked value.

Local unscoped writes are shared with local pipelines. Controller writes preserve explicit --pipeline or --shared selection. Saving a short secret prints one warning line on stderr. Read `sparkwing docs read --topic secrets` for the masking length rule. JSON returns metadata and replay without the value; plain emits the name.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--secret NAME` | Secret name. (required) |
| `--value VALUE` | value |
| `--file PATH` | file |
| `--variable` | Store an unmasked variable instead of a secret. |
| `--pipeline NAME` | Pipeline name. |
| `--shared` | shared |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. |
| `--only-on POOL` | only on |
