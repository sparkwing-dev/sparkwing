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
- `new` -- Scaffold a new Go or TypeScript pipeline
- `plan` -- Render the runtime-resolved DAG without dispatching any jobs
- `sparks` -- Manage sparks libraries declared in .sparkwing/sparkwing.yaml
- `upgrade` -- Upgrade repository SDK pins
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
| `--secrets` | Print only the secrets the pipeline declares, each marked required or optional. |

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

## `sparkwing pipeline hooks status`

Report declared, installed, and missing sparkwing hooks

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

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
| `--rules` | Print the lint rules with what each forbids and why, without analyzing any source. |
| `--dir DIR` | Analyze Go pipeline source in this directory instead of .sparkwing/jobs, or .sparkwing when there is no jobs directory. |

## `sparkwing pipeline list`

Enumerate every pipeline with metadata

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--all` | Include entries that are hidden by default. |
| `--search TEXT` | Text to match against record names and summaries; log text is not searched. |

## `sparkwing pipeline new`

Scaffold a new Go or TypeScript pipeline

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--pipeline NAME` | Pipeline name. (required) |
| `--template SHAPE` | Go shape to scaffold, minimal by default; typescript creates a minimal TypeScript pipeline. |
| `--on EVENT` | Trigger events to declare, comma separated, replacing the shape's default trigger; manual declares no trigger. |
| `--hidden` | Mark the new pipeline hidden in sparkwing.yaml so pipeline list omits it unless --all is given. |
| `--short TEXT` | One-line description to write into the generated pipeline source instead of a placeholder. |

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
| `--start-at STEP` | Preview the run as if it started at this step, marking steps that are neither this step nor downstream of it as skipped. |
| `--stop-at STEP` | Preview the run as if it stopped after this step, marking steps that are neither this step nor upstream of it as skipped. |

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
| `--source PATH` | Go module path of the spark library to add to the project's spark list. (required) |
| `--version VER` | Version of the library to declare, latest by default. |
| `--spark NAME` | Name to record for the library, the last element of --source by default. |

## `sparkwing pipeline sparks catalog`

List the blocks a spark library offers

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--library MODULE` | Full Go module path of the library whose blocks to list, sparks-core by default, at the version the project declares or latest. |
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
| `--no-resolve` | Show declared versions only, skipping the module lookup that resolves each to a concrete version. |

## `sparkwing pipeline sparks remove`

Remove a library from the sparks: block

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--spark NAME` | Name or source module path of the library to remove from the project's spark list. (required) |

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

--resume reattaches only a here run; --runner must be absent or here. The existing sealed spec is authoritative; reject new args, reason, agent, source flags and detach on resume.

Without --resume, one pipeline positional is required. Runner accepts here, cloud, a runner name, or label=<label>.

--agent NAME records the coding agent that starts the run on your behalf, such as claude-code:land-agent, and --agent none records none. Without the flag, SPARKWING_AGENT applies, then a detected coding-agent environment (Claude Code, Codex, Gemini CLI). The label is sealed with the request, kept by retries and publication, and shown in run views; it never grants or limits authority.

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
| `--profile NAME` | Controller connection profile from your config. Must be supplied explicitly. (required) |
| `--detach` | Return once the controller accepts the run instead of following its logs to the end; refused with --runner here. |
| `--working-tree` | Run an uploaded snapshot of this checkout, with uncommitted edits, deletions and untracked files that .gitignore does not exclude, instead of a pushed commit; the upload is refused if it holds secret-shaped files or values, submodules, unsafe symlinks, more than 100,000 files or more than 500 MiB. |
| `--trusted` | Run HEAD on this machine's own runner so a gate pass can count for CI (sw.FromProvenRunAtCommit); refused unless that runner is enrolled against the profile's controller and scoped to repositories, the checkout is clean and HEAD is the head of the same branch on origin. Submits no environment, and refuses --runner, --working-tree, --ref and --pipeline-ref. The controller still decides whether the pass counts. |
| `--runner RUNNER` | Where the run executes: here in this CLI, cloud on Sparkwing Cloud, a self-hosted runner by name, or matching self-hosted runners with label=<label>; without it, the team's routing decides. |
| `--reason TEXT` | Text the run records to explain why it ran in this CLI; required with --runner here and refused with any other runner. |
| `--agent NAME` | Coding agent starting this run, such as claude-code:land-agent; none records no agent. Display-only. |
| `--resume RUN` | Host an interrupted --runner here run again in this CLI, using the credential this machine saved and the run's original pipeline and arguments. |
| `--ref REF` | Git revision of this checkout to run, resolved locally, HEAD by default. |
| `--pipeline-ref REF` | Git revision to load the pipeline definitions from when it differs from the revision being run; it must match any source pin in the repository's configuration. |

## `sparkwing pipeline upgrade`

Upgrade repository SDK pins

Bump the current repository's Go or TypeScript SDK pin to the signed latest permitted release, or --version. Resolve native lockfiles and verify compilation/type checking plus describe/plan with the exact verified engine. A pipeline that fails that check against the new pin still gets the pin: the command reports the failure, names the migration guides and exits non-zero. Refuse revoked targets with their signed reason. --dry-run reports pin edits without changing files or installed services.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--version TAG` | SDK release tag to pin the repository to instead of the latest permitted release. |
| `--dry-run` | Show what would change without changing anything. |
