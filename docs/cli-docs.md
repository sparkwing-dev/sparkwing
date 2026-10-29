<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing docs

Every `sparkwing docs` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing docs`

Embedded user docs (offline)

### Subcommands

- `list` -- Enumerate every doc topic
- `migrations` -- Per-version migration guides (agent-friendly)
- `read` -- Read one document
- `search` -- Find the section that answers a question

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing docs list`

Enumerate every doc topic

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--guides` | List the embedded reading guides and the topics each covers instead of topic pages. |
| `--versions` | List the docs versions embedded in this CLI, adding the ones published on sparkwing.dev with --web. |
| `--search TEXT` | Text to match against record names and summaries; log text is not searched. |
| `--limit N` | Maximum number of records to print, at least one; continue with --cursor when more exist. |
| `--cursor CURSOR` | Continue from next_cursor using the same filters. |
| `--web` | List topics from sparkwing.dev for the selected version instead of the docs embedded in this CLI. |
| `--version vX.Y.Z` | Docs version to list; without --web it must match this CLI's embedded version or latest. |
| `--no-cache` | Fetch from sparkwing.dev without using the local docs web cache; has no effect without --web. |

## `sparkwing docs migrations`

Per-version migration guides (agent-friendly)

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--version vX.Y.Z` | Print the migration guide for this one release. |
| `--from vX.Y.Z` | Combine every migration guide released after this version, from the beginning by default. |
| `--to vA.B.C` | Combine every migration guide up to and including this version, the newest by default. |
| `--web` | Read migration guides from sparkwing.dev instead of the copies embedded in this CLI. |
| `--no-cache` | Fetch from sparkwing.dev without using the local docs web cache; has no effect without --web. |

## `sparkwing docs read`

Read one document

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--full` | Read the full documentation topic. |
| `--section START_LINE` | First line of the documentation section. |
| `--topic NAME` | Documentation topic. |
| `--guide NAME` | Read this embedded guide instead of a topic; cannot be combined with --topic, --web, --version or --no-cache. |
| `--web` | Read the topic from sparkwing.dev instead of the copy embedded in this CLI. |
| `--version vX.Y.Z` | Docs version to read; without --web it must match this CLI's embedded version or latest. |
| `--no-cache` | Fetch from sparkwing.dev without using the local docs web cache; has no effect without --web. |

## `sparkwing docs search`

Find the section that answers a question

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--limit N` | Maximum number of records to print, at least one; continue with --cursor when more exist. |
| `--cursor CURSOR` | Continue from next_cursor using the same filters. |
| `--search TEXT` | Text to match against record names and summaries; log text is not searched. |
| `--body` | Include document bodies in search results. |
| `--topics` | Return matching topic pages instead of individual section hits. |
