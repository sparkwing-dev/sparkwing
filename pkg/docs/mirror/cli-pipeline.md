<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing pipeline

Every `sparkwing pipeline` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing pipeline`

This repo's pipelines

### Subcommands

- `describe` -- Print one pipeline's full metadata
- `hooks` -- Install / uninstall git pre-commit + pre-push + post-commit hooks
- `lint` -- Check pipeline source for idiomatic anti-patterns (enforced gate)
- `list` -- Enumerate every pipeline with metadata
- `new` -- Scaffold a new Go pipeline
- `plan` -- Render the runtime-resolved DAG without dispatching any jobs
- `sparks` -- Manage sparks libraries declared in .sparkwing/sparkwing.yaml
- `trigger` -- Submit a pipeline run to a controller

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing pipeline describe`

Print one pipeline's full metadata

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--pipeline NAME` | Pipeline name. (required) |
| `--secrets` | secrets |

## `sparkwing pipeline hooks`

Install / uninstall git pre-commit + pre-push + post-commit hooks

### Subcommands

- `install` -- Install pre-commit / pre-push / post-commit git hooks from sparkwing.yaml triggers
- `status` -- Report declared, installed, and missing sparkwing hooks
- `uninstall` -- Remove sparkwing-managed git hooks

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing pipeline hooks install`

Install pre-commit / pre-push / post-commit git hooks from sparkwing.yaml triggers

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--no-prove` | no prove |
| `--all-repos` | all repos |

## `sparkwing pipeline hooks status`

Report declared, installed, and missing sparkwing hooks

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--all-repos` | all repos |
| `--ungated` | ungated |

## `sparkwing pipeline hooks uninstall`

Remove sparkwing-managed git hooks

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing pipeline lint`

Check pipeline source for idiomatic anti-patterns (enforced gate)

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--pipeline NAME` | Pipeline name. |
| `--rules` | rules |
| `--dir DIR` | dir |
| `--static` | static |

## `sparkwing pipeline list`

Enumerate every pipeline with metadata

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--all` | Include entries normally hidden. |
| `--search TEXT` | Match records; never scan log text. |

## `sparkwing pipeline new`

Scaffold a new Go pipeline

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--pipeline NAME` | Pipeline name. (required) |
| `--template SHAPE` | template |
| `--on EVENT` | on |
| `--hidden` | hidden |
| `--short TEXT` | Only -o output, -q quiet, -f follow, -v run verbosity, -C checkout and -h help. |

## `sparkwing pipeline plan`

Render the runtime-resolved DAG without dispatching any jobs

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--pipeline NAME` | Pipeline name. (required) |
| `--start-at STEP` | start at |
| `--stop-at STEP` | stop at |

## `sparkwing pipeline sparks`

Manage sparks libraries declared in .sparkwing/sparkwing.yaml

### Subcommands

- `add` -- Add a library to the sparks: block
- `catalog` -- List the blocks a spark library offers
- `inflate` -- Copy a spark library's source into this repo so you can edit it
- `lint` -- Validate a spark.json library manifest
- `list` -- Show declared sparks libraries and their resolved versions
- `remove` -- Remove a library from the sparks: block
- `update` -- Re-resolve every declared library

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing pipeline sparks add`

Add a library to the sparks: block

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--source PATH` | source (required) |
| `--version VER` | version |
| `--spark NAME` | spark |

## `sparkwing pipeline sparks catalog`

List the blocks a spark library offers

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--library MODULE` | library |
| `--path DIR` | Path to select. |

## `sparkwing pipeline sparks inflate`

Copy a spark library's source into this repo so you can edit it

Copy one library module into the project and replace its dependency with that local source. JSON emits the module, version, destination and replacement; plain emits the destination.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--library MODULE` | A sparks-core module name or full Go module path; list choices with pipeline sparks catalog. (required) |

## `sparkwing pipeline sparks lint`

Validate a spark.json library manifest

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--path PATH` | Path to select. |

## `sparkwing pipeline sparks list`

Show declared sparks libraries and their resolved versions

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--no-resolve` | no resolve |

## `sparkwing pipeline sparks remove`

Remove a library from the sparks: block

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--spark NAME` | spark (required) |

## `sparkwing pipeline sparks update`

Re-resolve every declared library

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing pipeline trigger`

Submit a pipeline run to a controller

Flags including --profile may precede the verb; unknown engine flags before -- are usage errors.

--resume reattaches only a here run; --runner must be absent or here. The existing sealed spec is authoritative; reject new args, reason, source flags and detach on resume.

Without --resume, one pipeline positional is required. Runner accepts here, cloud, a runner name, or label=<label>.

Against Sparkwing Cloud, a run without --runner executes on Cloud unless a repository pool route rule requires a pool, which keeps it on that pool's self-hosted runners. cloud keeps it on Cloud, a runner name or label=<label> keeps it on matching self-hosted runners, and here hosts it in this CLI.

### Arguments

- `<pipeline>` (optional) -- Pipeline name

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile. (required) |
| `--detach` | detach |
| `--working-tree` | working tree |
| `--allow-secret-file PATH` | allow secret file Repeatable; comma-separated values are accepted. |
| `--runner RUNNER` | Runner name or selection. |
| `--reason TEXT` | reason |
| `--resume RUN` | resume |
| `--ref REF` | ref |
| `--pipeline-ref REF` | pipeline ref |
