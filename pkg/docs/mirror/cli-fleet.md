<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing fleet

Every `sparkwing fleet` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing fleet`

Configure foreground assisted execution

Local fleet configuration and one-time helper provisioning. Running a
pipeline with assistance still uses sparkwing run PIPELINE --sw-fleet.

Fleet runs transmit an immutable snapshot containing every tracked file and
every non-ignored untracked file to the executor that wins a node. Review
'git status' and ignore local secret files before starting a fleet run. Normal
output reports only the source digest, file count, and total bytes, never file
names. The snapshot commit has no parent and does not transmit repository
history.

### Subcommands

- `init` -- Create an owner-only foreground fleet policy
- `agents` -- Provision helpers for foreground coordinators

## `sparkwing fleet agents`

Provision helpers for foreground coordinators

Creates local verifier-backed credentials and trusted executor enrollments. Raw credentials print once and never enter fleet.yaml.

### Subcommands

- `enroll` -- Provision one helper membership

## `sparkwing fleet agents enroll`

Provision one helper membership

Atomically mints a runner credential in the local Sparkwing state
store and binds its verifier to the trusted executor envelope. The raw
credential prints once in an agent.yaml membership snippet on stdout. The
trusted policy is added to fleet.yaml in the same command. Credential verifier
and binding data remain in Sparkwing's private local state; fleet.yaml stores
no token material or token identifier.

Atomically merge stdout into the helper's owner-only agent.yaml (0600 on Unix;
a protected user ACL on Windows). Direct shell redirection can truncate an
existing multi-coordinator file before validation and is not a safe merge.

Use one credential per coordinator membership.

### Flags

| Flag | Description |
|---|---|
| `--name NAME` | Executor name (required) |
| `--location WHERE` | Controller-owned placement (local\|cloud) (required) |
| `--capability LABEL` | Trusted capability (repeatable) |
| `--base-priority N` | Base scheduling priority (0-100) (default: 50) |
| `--priority-ceiling N` | Highest effective priority (0-100) (default: 100) |
| `--max-concurrent N` | Trusted concurrent slot ceiling (default: 1) |
| `--budget-cores N` | CPU contribution ceiling (0 = uncapped) (default: 0) |
| `--budget-memory-bytes N` | Memory contribution ceiling in bytes (0 = uncapped) (default: 0) |
| `--ttl DURATION` | Credential lifetime (0 = never expires) (default: 0) |

### Examples

```sh
# Provision a laptop helper
sparkwing fleet agents enroll --name desk --location local --capability toolchain=go --max-concurrent 2
```

## `sparkwing fleet init`

Create an owner-only foreground fleet policy

Creates fleet.yaml without replacing an existing policy. The listener is
fixed for the life of each foreground run. HTTPS public URLs assume a local
Tailscale Serve or reverse proxy and therefore require a literal loopback
listener. Plain HTTP is accepted only at a literal IP that the local Tailscale
client confirms belongs to this machine. Tailscale supplies transport, not
Sparkwing authorization: only explicitly enrolled helpers receive credentials,
and no peer discovery occurs.

### Flags

| Flag | Description |
|---|---|
| `--tailnet` | Use this machine's Tailscale IPv4 address on port 4346 |
| `--listen HOST:PORT` | Fixed private listener address |
| `--public-url URL` | Helper-reachable coordinator origin |
| `--allow-tailnet-http` | Allow HTTP at a verified literal local Tailscale IP |

### Examples

```sh
# Direct Tailscale transport
sparkwing fleet init --tailnet

# Tailscale Serve or a local proxy
sparkwing fleet init --listen 127.0.0.1:4346 --public-url https://runner.example.com

# Advanced direct Tailscale transport
sparkwing fleet init --listen 100.64.1.2:4346 --public-url http://100.64.1.2:4346 --allow-tailnet-http
```
