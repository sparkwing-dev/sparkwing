<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing docs

Every `sparkwing docs` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing docs`

Embedded user docs (offline)

The docs ship inside this binary and match its version.
Start with docs search --query <question> for a page of short snippets,
then docs read --topic <slug> --section <start_line> for one selected hit.
Docs list pages through topic metadata; --query narrows that index.
JSON indexes end with a typed page summary and continuation cursor.

Selected reads return JSON document records when piped. Explicit
--output plain prints the original Markdown. Use --web and --version
on list/read when comparing another published version.
docs list --guides names the task-sized topic sets, docs list --versions
the doc versions this CLI knows, and docs read --all is an explicit
exhaustive export. The --web fetch cache lives under `sparkwing cache info --docs`.

### Subcommands

- `list` -- Enumerate every doc topic
- `read` -- Read one document
- `search` -- Find the section that answers a question
- `migrations` -- Per-version migration guides (agent-friendly)

### Examples

```sh
# List topic metadata
sparkwing docs list

# List topic metadata (agent-readable)
sparkwing docs list -o json

# Read one topic
sparkwing docs read --topic pipelines

# Read one topic at a specific version (online)
sparkwing docs read --topic pipelines --version v0.3.0 --web

# Find docs that mention warm pool
sparkwing docs search --query "warm pool"

# List migration guides this CLI knows
sparkwing docs migrations

# Pipe every guide up to v0.4.0 into context
sparkwing docs migrations --to v0.4.0

# List every version available online
sparkwing docs list --versions --web
```

## `sparkwing docs list`

Enumerate every doc topic

List topic metadata in lexical slug order, at most 40 rows by default.
--query matches words in slugs, titles and summaries before pagination.
JSON ends with a kind:page record; continue with --cursor and the same
filters. --limit 0 emits every match. Bodies belong to docs read.
The embedded copy matches this binary; --web reads another version.

--guides lists the task-sized topic sets instead: each guide is a named set
of narrative topics that answer one task together, and
`sparkwing docs read --guide NAME` returns the whole set in one call.
The generated references (sdk-reference, cli-reference) are lookup tables
rather than pages to read end to end; reach those with `sparkwing docs search`.

--versions lists the binary's embedded documentation version and its
migration-guide versions; with --web it merges in the versions published on
sparkwing.dev. Use a returned version with `sparkwing docs read --web --version`.
--guides takes only --output; --versions takes --web and --no-cache.

### Flags

| Flag | Description |
|---|---|
| `--guides` | List the task-sized topic sets (`docs read --guide`) instead of topics |
| `--versions` | List the doc versions this CLI knows (and sparkwing.dev with --web) instead of topics |
| `-q, --query TEXT` | Match words in slug, title and summary |
| `--limit N` | Maximum records; 0 returns every remaining match (default: 40) |
| `--cursor CURSOR` | Continue after next_cursor with the same filters and binary version |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |
| `--web` | Fetch from sparkwing.dev instead of the embedded corpus |
| `--version vX.Y.Z` | Doc version (vX.Y.Z or latest). Defaults to this CLI's embedded version. |
| `--no-cache` | With --web, bypass the on-disk cache for this invocation |

### Examples

```sh
# Human-readable table
sparkwing docs list

# Agent-readable
sparkwing docs list -o json

# Slug-per-line for shell loops
sparkwing docs list --limit 0 -o plain

# List the v0.3.0 corpus from sparkwing.dev
sparkwing docs list --web --version v0.3.0

# What guide sets exist
sparkwing docs list --guides

# Doc versions embedded in this CLI
sparkwing docs list --versions

# Every version available online
sparkwing docs list --versions --web -o json
```

## `sparkwing docs migrations`

Per-version migration guides (agent-friendly)

With no flag, lists each migration guide bundled with this binary in
descending semver order, with date, size and one-line summary parsed from
docs/migrations/README.md; --output json is an array of
{version, date, summary, slug, bytes}. When this CLI is older than the newest
embedded guide a one-line stderr note suggests updating.

--version V (or a positional vX.Y.Z) reads that one guide as a JSON document
record when piped; --output plain prints its raw Markdown. Cross-doc links are
rewritten into `sparkwing docs read --topic <slug>` form.

--from A and --to B concatenate every guide with a version greater than A and
at most B, in ascending order, into one blob: Markdown output separates guides
with horizontal rules and names the range in its heading. --from defaults to
v0.0.0 and --to to the highest embedded version, so --from v0.0.0 alone is
every guide this CLI knows.

--web reads sparkwing.dev instead of the embedded corpus, for the list, one
guide, or a range.

### Arguments

- `[vX.Y.Z]` (optional) -- Migration guide version to read, when --version is not supplied

### Flags

| Flag | Description |
|---|---|
| `--version vX.Y.Z` | Read this one guide. Positional fallback accepted. |
| `--from vX.Y.Z` | Concatenate the guides after this version (exclusive; default v0.0.0) |
| `--to vA.B.C` | Concatenate the guides up to this version (inclusive; default = latest embedded version) |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |
| `--web` | Fetch from sparkwing.dev instead of the embedded corpus |
| `--no-cache` | With --web, bypass the on-disk cache for this invocation |

### Examples

```sh
# List embedded migration guides
sparkwing docs migrations

# Version-per-line for shell loops
sparkwing docs migrations -o plain

# Read one guide
sparkwing docs migrations --version v0.4.0

# Positional shortcut
sparkwing docs migrations v0.4.0

# Every guide upgrading from v0.3.0 to v0.4.0
sparkwing docs migrations --from v0.3.0 --to v0.4.0

# Every guide this CLI knows (one-shot agent context)
sparkwing docs migrations --from v0.0.0

# Read v0.5.0 from sparkwing.dev
sparkwing docs migrations --version v0.5.0 --web

# Every release on sparkwing.dev
sparkwing docs migrations --web
```

## `sparkwing docs read`

Read one document

Reads the named topic, or one embedded section selected by its start_line
from docs search (--section). Section selection requires --topic and
cannot combine with --guide or --web. Piped output is one JSON document record;
--output plain prints raw Markdown. The slug is
the filename under /docs/ minus .md (run `sparkwing docs list` to
see them all). Nested topics use slash-separated names.

Default source is the binary's embedded corpus. Use --web to fetch
from sparkwing.dev, optionally pinned to --version vX.Y.Z or
--version latest.

--all reads every embedded document, one JSON record per page when piped;
--output plain prints the full Markdown corpus with page headers. It takes no
other selection or source flag.

### Flags

| Flag | Description |
|---|---|
| `--all` | Read every embedded document (explicit exhaustive export) |
| `--section START_LINE` | Read one embedded section returned by search |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (pretty on a terminal, json when piped) |
| `--topic NAME` | Topic name from docs list |
| `--guide NAME` | Read a task-sized set of topics instead of one (`sparkwing docs list --guides`) |
| `--web` | Fetch from sparkwing.dev instead of the embedded corpus |
| `--version vX.Y.Z` | Doc version (vX.Y.Z or latest). Defaults to this CLI's embedded version. |
| `--no-cache` | With --web, bypass the on-disk cache for this invocation |

### Examples

```sh
# Read the getting-started page
sparkwing docs read --topic getting-started

# Everything needed to write a pipeline, one call
sparkwing docs read --guide authoring

# Pipe through a pager
sparkwing docs read --topic pipelines --output plain | less

# Read v0.3.0's pipelines page online
sparkwing docs read --topic pipelines --version v0.3.0 --web

# Always fetch the freshest version
sparkwing docs read --topic pipelines --version latest --web

# Explicit exhaustive document export
sparkwing docs read --all
```

## `sparkwing docs search`

Find the section that answers a question

Find matching sections, ranked before pagination. Every query word must
match; heading matches rank ahead of body matches. The default page has
20 hits with topic, heading, line range and a short snippet, never bodies.
JSON ends with a kind:page record; --cursor continues the same query.
--limit 0 returns all matches. Use the same binary for continuation.

Read one hit with docs read --topic <slug> --section <start_line>.
--body explicitly includes full bodies for this page. --topics lists
matching topic metadata instead of sections.

### Flags

| Flag | Description |
|---|---|
| `--limit N` | Maximum records; 0 returns every remaining match (default: 20) |
| `--cursor CURSOR` | Continue after next_cursor with the same filters and binary version |
| `-q, --query TEXT` | Search terms (every token must match) (required) |
| `--body` | Print each matching section in full instead of a snippet |
| `--topics` | List whole matching topics instead of sections |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |

### Examples

```sh
# Where a PR trigger is defined
sparkwing docs search --query pull_request

# Read the matching sections in full
sparkwing docs search -q ApprovalConfig --body

# Compact snippets for agents
sparkwing docs search -q approval -o json

# Matching topic metadata
sparkwing docs search -q "warm pool" --topics
```
