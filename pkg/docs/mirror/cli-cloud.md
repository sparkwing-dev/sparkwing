<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing cloud

Every `sparkwing cloud` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing cloud`

Connect this machine to a sparkwing controller

One command joins a controller: 'connect' verifies the
controller answers, mints a user token when you hand it an admin credential,
and writes the profile that every other command selects with --profile.
'status' reports what that connection authenticates as. 'disconnect' removes
the profile and revokes its token.

Nothing here edits profiles.yaml by hand. Enroll this machine as a runner with
'sparkwing cluster runners add'.

### Subcommands

- `connect` -- Write the profile that reaches a controller
- `status` -- Report the connection, its principal, and the probes
- `disconnect` -- Revoke the connection's token and drop the profile

### Examples

```sh
# Connect with a one-time admin token
sparkwing cloud connect --controller https://api.sparkwing.example --admin-token-stdin

# Report the connection
sparkwing cloud status --profile api-sparkwing-example
```

## `sparkwing cloud connect`

Write the profile that reaches a controller

Verifies the controller answers its health route, then writes
a profile carrying the controller URL and a token.

--admin-token-stdin reads an admin credential from stdin and mints a user
token with it, carrying runs.read, runs.write, triggers.read, logs.read and
approvals.write. The admin credential is never stored; only the minted token
reaches profiles.yaml. --token-stdin stores a token you already hold. Neither
flag connects to a controller serving unauthenticated.

--name defaults to the controller host with every character outside a-z0-9
turned into a dash, so https://api.sparkwing.example becomes
api-sparkwing-example. An existing profile of that name is never replaced
without --force, because the token it holds stays live until it is revoked.

--set-default writes defaults.profile into this repository's
.sparkwing/sparkwing.yaml, so runs in this checkout select the connection with
no flag. The name resolves against the project's own profiles: block first and
profiles.yaml second, so the token stays out of the checkout.

The command closes with the dashboard URL the controller announces and the
probes 'sparkwing configure profiles test' runs.

### Flags

| Flag | Description |
|---|---|
| `--controller URL` | Controller base URL (required) |
| `--name NAME` | Profile name (default: derived from the controller host) |
| `--admin-token-stdin` | Read an admin token from stdin and mint a user token with it |
| `--token-stdin` | Read an already-minted user token from stdin |
| `--scope CSV` | Comma-separated scopes for the minted token (default: runs.read,runs.write,runs.control,triggers.read,logs.read,approvals.write) |
| `--set-default` | Set defaults.profile in this project's .sparkwing/sparkwing.yaml |
| `--force` | Replace an existing profile of that name |

### Examples

```sh
# Connect with a one-time admin token
sparkwing cloud connect --controller https://api.sparkwing.example --admin-token-stdin

# Connect and make it this repository's default
sparkwing cloud connect --controller https://api.sparkwing.example --name prod --admin-token-stdin --set-default

# Store a token someone minted for you
sparkwing cloud connect --controller https://api.sparkwing.example --token-stdin
```

## `sparkwing cloud disconnect`

Revoke the connection's token and drop the profile

Revokes the profile's token on its controller, then removes
the profile. A revoke this credential is not allowed to make leaves the token
live and names the prefix and the command that finishes the job, so a
connection is never dropped silently.

The profile's own token revokes only when it carries admin;
--admin-token-stdin supplies one that does. A prefix the controller reports as
anything but a user token is refused, naming what it found. --keep-token drops
the profile and touches no credential.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Profile name to disconnect (required) |
| `--admin-token-stdin` | Read an admin token from stdin and revoke with it |
| `--keep-token` | Remove the profile without revoking its token |

### Examples

```sh
# Disconnect and revoke with an admin token
sparkwing cloud disconnect --name prod --admin-token-stdin

# Drop the profile and leave the token alone
sparkwing cloud disconnect --name prod --keep-token
```

## `sparkwing cloud status`

Report the connection, its principal, and the probes

Prints the selected profile, its controller, the principal
and scopes the controller reports for its token, the announced dashboard URL,
and the controller, auth, logs and gitcache probes. Exits non-zero when a probe
fails.

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Profile naming the connection to report |
| `-o, --output FORMAT` | Output format: json\|table |

### Examples

```sh
# Report the connection
sparkwing cloud status --profile prod

# Machine-readable status
sparkwing cloud status --profile prod -o json
```
