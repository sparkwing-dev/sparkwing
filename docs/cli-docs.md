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
| `--guides` | guides |
| `--versions` | versions |
| `--search TEXT` | Match records; never scan log text. |
| `--limit N` | Pagination count at least one, continued with --cursor; zero is invalid. |
| `--cursor CURSOR` | Continue from next_cursor using the same filters. |
| `--web` | web |
| `--version vX.Y.Z` | version |
| `--no-cache` | no cache |

## `sparkwing docs migrations`

Per-version migration guides (agent-friendly)

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--version vX.Y.Z` | version |
| `--from vX.Y.Z` | from |
| `--to vA.B.C` | to |
| `--web` | web |
| `--no-cache` | no cache |

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
| `--guide NAME` | guide |
| `--web` | web |
| `--version vX.Y.Z` | version |
| `--no-cache` | no cache |

## `sparkwing docs search`

Find the section that answers a question

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--limit N` | Pagination count at least one, continued with --cursor; zero is invalid. |
| `--cursor CURSOR` | Continue from next_cursor using the same filters. |
| `--search TEXT` | Match records; never scan log text. |
| `--body` | Include document bodies in search results. |
| `--topics` | topics |
