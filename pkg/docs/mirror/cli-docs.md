<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing docs

Every `sparkwing docs` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing docs`

Embedded user docs (offline)

The sparkwing docs are shipped inside the binary. `sparkwing docs read --topic getting-started` returns the
raw markdown to stdout; `sparkwing docs all` dumps every
doc in one shot for an agent that wants the full corpus in
context. The docs match the binary version exactly -- no risk of
the website explaining a flag your CLI doesn't have.

Discovery: `sparkwing docs list -o json` returns slug + title +
summary for every topic. `sparkwing docs search --query pull_request`
returns the matching sections -- topic, heading, line range -- so a
narrow question does not cost a whole page.

When one page leaves you a lookup short, `sparkwing docs guides`
lists task-sized sets of topics; `sparkwing docs read --guide authoring`
returns the whole set in one call.

### Subcommands

- `list` -- Enumerate every doc topic
- `read` -- Print one doc's raw markdown to stdout
- `guides` -- List the task-sized doc sets (--guide on `docs read`)
- `all` -- Concatenate every doc to stdout (full corpus dump)
- `search` -- Find the section that answers a question
- `migrations` -- Per-version migration guides (agent-friendly)
- `versions` -- List doc versions known to this CLI (and sparkwing.dev with --web)
- `cache` -- Inspect or clear the on-disk cache used by --web

### Examples

```sh
# List all topics (table)
sparkwing docs list

# List all topics (agent-readable)
sparkwing docs list -o json

# Read one topic
sparkwing docs read --topic pipelines

# Read one topic at a specific version (online)
sparkwing docs read --topic pipelines --version v0.3.0 --web

# Slurp the whole corpus into context
sparkwing docs all

# Find docs that mention warm pool
sparkwing docs search --query "warm pool"

# List migration guides this CLI knows
sparkwing docs migrations list

# Pipe every guide up to v0.4.0 into context
sparkwing docs migrations between --to v0.4.0

# List every version available online
sparkwing docs versions --web
```

## `sparkwing docs all`

Concatenate every doc to stdout (full corpus dump)

Prints every embedded doc to stdout, separated by short ASCII
headers. The "give me everything" path for an agent that wants
the full corpus in context with one Bash invocation.

### Examples

```sh
# Slurp every doc into context
sparkwing docs all
```

## `sparkwing docs cache`

Inspect or clear the on-disk cache used by --web

--web fetches are cached to $XDG_CACHE_HOME/sparkwing/web/ (or
~/.cache/sparkwing/web/). The cache mirrors the URL path, so you
can `cat` the cached files directly when debugging.

Use `cache info` to see size / counts; use `cache clear` to wipe it.

### Subcommands

- `info` -- Print cache dir, total size, per-resource breakdown
- `clear` -- Remove every cached file

### Examples

```sh
# How big is the cache?
sparkwing docs cache info

# Force-refresh on next --web call
sparkwing docs cache clear
```

## `sparkwing docs cache clear`

Remove every cached file

Deletes every file under the cache directory. Safe: the
implementation refuses to remove paths that don't resolve inside
the cache dir, so a stray symlink in the cache can't escape.

Useful when a cached versions.json or index.json has gone stale
faster than the 24h TTL window, or when debugging --web behavior.

### Examples

```sh
# Wipe the cache
sparkwing docs cache clear
```

## `sparkwing docs cache info`

Print cache dir, total size, per-resource breakdown

Walks the cache and prints a summary: total size, file counts
broken down by doc / migration / index, and the freshness state of
the cached versions.json (24h TTL).

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json (default: pretty) |

### Examples

```sh
# Human-readable
sparkwing docs cache info

# Agent-readable
sparkwing docs cache info -o json
```

## `sparkwing docs guides`

List the task-sized doc sets (--guide on `docs read`)

A guide is a named set of topics that answer one task
together, for the case where reading a single page leaves you one
lookup short. `sparkwing docs read --guide authoring` returns the
whole set in one call.

Guides carry narrative topics only. The generated references
(sdk-reference, cli-reference) are lookup tables rather than pages to
read end to end; reach those with `sparkwing docs search`.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |

### Examples

```sh
# What sets exist
sparkwing docs guides

# Read the authoring set
sparkwing docs read --guide authoring

# Agent-readable
sparkwing docs guides -o json
```

## `sparkwing docs list`

Enumerate every doc topic

Walks the docs corpus and prints one row per topic with its
slug, first-H1 title, and first-paragraph summary. By default reads
the binary's embedded copy (hermetic, version-locked); pass --web
to fetch from sparkwing.dev for another version.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
| `--web` | Fetch from sparkwing.dev instead of the embedded corpus |
| `--version vX.Y.Z` | Doc version (e.g. v0.4.0, 'latest'). Defaults to this CLI's embedded version. |
| `--no-cache` | With --web, bypass the on-disk cache for this invocation |

### Examples

```sh
# Human-readable table
sparkwing docs list

# Agent-readable
sparkwing docs list -o json

# Slug-per-line for shell loops
sparkwing docs list -o plain

# List the v0.3.0 corpus from sparkwing.dev
sparkwing docs list --web --version v0.3.0
```

## `sparkwing docs migrations`

Per-version migration guides (agent-friendly)

Surface the migration guides shipped under docs/migrations/.
Each released sparkwing version that introduces breaking changes
gets a guide; `sparkwing docs migrations between` concatenates
every guide in a version range into one blob you can pipe straight
into an agent context.

The same files are also reachable as regular docs (e.g.
`sparkwing docs read --topic migrations/v0.4.0`); this
subcommand is the ergonomics layer with semver-aware filtering and
range output.

### Subcommands

- `list` -- Table of every embedded migration guide
- `read` -- Print one migration guide's markdown to stdout
- `between` -- Concatenate every guide in a version range into one blob

### Examples

```sh
# List embedded migration guides
sparkwing docs migrations list

# Read one guide
sparkwing docs migrations read --version v0.4.0

# Every guide upgrading from v0.3.0 to v0.4.0
sparkwing docs migrations between --from v0.3.0 --to v0.4.0

# Every guide this CLI knows (one-shot agent context)
sparkwing docs migrations between
```

## `sparkwing docs migrations between`

Concatenate every guide in a version range into one blob

Returns every migration guide whose version is in (--from, --to],
in ascending version order, separated by markdown horizontal rules.
The output starts with a "Migration: vA -> vB" header so an agent
knows the range up-front.

This is the agent-killer command: one invocation produces the full
migration context for an N-version jump in a form ready to pipe.

--from defaults to v0.0.0 (every guide up through --to).
--to defaults to the highest version this CLI knows about.

### Flags

| Flag | Description |
|---|---|
| `--from vX.Y.Z` | Exclusive lower bound (default v0.0.0) |
| `--to vA.B.C` | Inclusive upper bound (default = latest embedded version) |
| `-o, --output FORMAT` | Output format: markdown \| plain (default: markdown) |
| `--web` | Fetch every guide in the range from sparkwing.dev |
| `--no-cache` | With --web, bypass the on-disk cache for this invocation |

### Examples

```sh
# Every guide for a v0.3.0 -> v0.4.0 jump
sparkwing docs migrations between --from v0.3.0 --to v0.4.0

# Every guide up to a target version
sparkwing docs migrations between --to v0.4.0

# Every guide this CLI knows (one-shot agent context)
sparkwing docs migrations between

# Full range from sparkwing.dev (includes versions not yet embedded)
sparkwing docs migrations between --web
```

## `sparkwing docs migrations list`

Table of every embedded migration guide

Lists each migration guide bundled with this binary in
descending semver order, with date and one-line summary parsed
from docs/migrations/README.md. Use --output json for an
agent-readable array of {version, date, summary, slug, bytes}.

When the CLI's own version is older than the newest embedded
guide a one-line stderr note suggests rebuilding.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
| `--web` | Fetch the index from sparkwing.dev/migrations/index.json instead of the embed |
| `--no-cache` | With --web, bypass the on-disk cache for this invocation |

### Examples

```sh
# Human-readable table
sparkwing docs migrations list

# Agent-readable
sparkwing docs migrations list -o json

# Version-per-line for shell loops
sparkwing docs migrations list -o plain

# Online (every release on sparkwing.dev)
sparkwing docs migrations list --web
```

## `sparkwing docs migrations read`

Print one migration guide's markdown to stdout

Outputs the markdown body for a single migration guide. Default
output is the raw markdown so an agent can pipe straight into
its context. Cross-doc markdown links to other topics are
rewritten into `sparkwing docs read --topic <slug>` form
(same transform as `sparkwing docs read`).

### Arguments

- `[vX.Y.Z]` (optional) -- Migration guide version, when --version is not supplied

### Flags

| Flag | Description |
|---|---|
| `--version vX.Y.Z` | Migration guide version (e.g. v0.4.0). Positional fallback accepted. |
| `-o, --output FORMAT` | Output format: markdown \| plain (default: markdown) |
| `--web` | Fetch from sparkwing.dev instead of the embedded corpus |
| `--no-cache` | With --web, bypass the on-disk cache for this invocation |

### Examples

```sh
# Read the v0.4.0 guide
sparkwing docs migrations read --version v0.4.0

# Positional shortcut
sparkwing docs migrations read v0.4.0

# Read v0.5.0 from sparkwing.dev (not yet embedded)
sparkwing docs migrations read --version v0.5.0 --web
```

## `sparkwing docs read`

Print one doc's raw markdown to stdout

Prints the raw markdown body for the named topic. The slug is
the filename under /docs/ minus .md (run `sparkwing docs list` to
see them all). Subdirs use slash-separated paths (e.g.
design/remote-retry).

Default source is the binary's embedded corpus. Use --web to fetch
from sparkwing.dev, optionally pinned to --version vX.Y.Z or
--version latest.

### Flags

| Flag | Description |
|---|---|
| `--topic NAME` | Doc slug (e.g. getting-started, pipelines, mcp) |
| `--guide NAME` | Read a task-sized set of topics instead of one (`sparkwing docs guides`) |
| `--web` | Fetch from sparkwing.dev instead of the embedded corpus |
| `--version vX.Y.Z` | Doc version (e.g. v0.4.0, 'latest'). Defaults to this CLI's embedded version. |
| `--no-cache` | With --web, bypass the on-disk cache for this invocation |

### Examples

```sh
# Read the getting-started page
sparkwing docs read --topic getting-started

# Everything needed to write a pipeline, one call
sparkwing docs read --guide authoring

# Pipe through a pager
sparkwing docs read --topic pipelines | less

# Read v0.3.0's pipelines page online
sparkwing docs read --topic pipelines --version v0.3.0 --web

# Always fetch the freshest version
sparkwing docs read --topic pipelines --version latest --web
```

## `sparkwing docs search`

Find the section that answers a question

Returns the doc sections containing every space-separated
token in --query (case-insensitive), best first: a heading hit outranks
a body hit, and a shorter section outranks a longer one holding the same
match. Each result names its topic, heading, and line range.

Sections rather than whole topics because the reference pages run to
tens of thousands of tokens, and the question is usually narrow -- what
a `pull_request` trigger looks like, what fields `ApprovalConfig` has.
Add --body to print the matching sections in full.

--topics restores the old behavior, listing whole matching topics in the
same shape as `sparkwing docs list`.

### Flags

| Flag | Description |
|---|---|
| `-q, --query TEXT` | Search terms (every token must match) (required) |
| `--body` | Print each matching section in full instead of a snippet |
| `--topics` | List whole matching topics instead of sections |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |

### Examples

```sh
# Where a PR trigger is defined
sparkwing docs search --query pull_request

# Read the matching sections in full
sparkwing docs search -q ApprovalConfig --body

# JSON for agents (topic, heading, line range, body)
sparkwing docs search -q approval -o json

# Whole topics, as before
sparkwing docs search -q "warm pool" --topics
```

## `sparkwing docs versions`

List doc versions known to this CLI (and sparkwing.dev with --web)

Reports each doc version the source knows about. Default
output is hermetic: only the binary's embedded version (plus every
migration-guide version shipped in the embed) appears, with no
network calls.

With --web, fetches sparkwing.dev/versions.json and merges in every
release available online -- useful for discovering newer versions
this CLI can render via --web on the read / list verbs.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty) |
| `--web` | Merge in sparkwing.dev/versions.json (network) |
| `--no-cache` | With --web, bypass the on-disk cache for this invocation |

### Examples

```sh
# Embedded only (default)
sparkwing docs versions

# Every version available online
sparkwing docs versions --web

# Agent-readable JSON
sparkwing docs versions --web -o json
```
