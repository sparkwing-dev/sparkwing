<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing fleet

Every `sparkwing fleet` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing fleet`

Configure foreground assisted execution

Local fleet configuration. Running a pipeline with assistance uses
sparkwing run PIPELINE --sw-fleet, and fleet.yaml names the helpers it trusts.

Fleet runs transmit an immutable snapshot containing every tracked file and
every non-ignored untracked file to the executor that wins a node. Review
'git status' and ignore local secret files before starting a fleet run. Normal
output reports only the source digest, file count, and total bytes, never file
names. The snapshot commit has no parent and does not transmit repository
history.

### Subcommands

- `init` -- Create an owner-only foreground fleet policy

## `sparkwing fleet init`

Create an owner-only foreground fleet policy

Creates fleet.yaml without replacing an existing policy. The listener is
fixed for the life of each foreground run. HTTPS public URLs assume a local
Tailscale Serve or reverse proxy and therefore require a literal loopback
listener. Plain HTTP is accepted only at a literal IP that the local Tailscale
client confirms belongs to this machine. Tailscale supplies transport, not
Sparkwing authorization: only explicitly enrolled helpers receive credentials,
and no peer discovery occurs.

SPARKWING_HOME does not move fleet.yaml; it is the state, cache and
logs root, and the fleet policy is machine-wide. A write from a
command running under a home of its own is refused rather than sent
to the machine's policy: set SPARKWING_FLEET_CONFIG to a path inside
that home to keep it there.

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
