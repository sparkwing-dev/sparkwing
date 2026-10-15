<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing run

Every `sparkwing run` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing run`

Invoke a pipeline

Always executes locally, including detached work on the local controller. --profile is refused.

Publication is one-way run/results/logs with local provenance; a publication failure never changes the local run verdict.

Cache-from reads only authorized verified-default-branch memo/artifacts/depcache; no shared writes or remote binary fetch.

--sw-output applies to foreground and detached runs; explicit --sw-output beats -o, then SPARKWING_LOG_FORMAT, then terminal detection.

### Arguments

- `<pipeline>` (required) -- Pipeline name

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--publish PROFILE` | Copy this local run's results to the named controller profile. |
| `--sw-cache-from PROFILE` | Read the authorized remote cache; an empty value disables the machine default. |
| `--sw-pause-before JOB` | Pause before the selected job. Repeatable; comma-separated values are accepted. |
| `--sw-pause-after JOB` | Pause after the selected job. Repeatable; comma-separated values are accepted. |
| `--sw-pause-on-failure` | Keep failed jobs paused for inspection. |
| `--sw-ref REF` | Build the pipeline and run against this Git ref. |
| `--sw-pipeline-ref REF` | Load the pipeline from this Git ref. |
| `--sw-detached` | Submit work to the local controller and return before it finishes. |
| `--sw-workers N` | Positive foreground worker count; refused for detached runs. Must be at least 1. |
| `--sw-idempotency-key KEY` | Deduplicate detached submissions using this key. |
| `--sw-request-id ID` | Request identifier for a detached submission. |
| `--sw-output FORMAT` | Format for run output. Values: pretty, json, plain. |
| `-v, --sw-verbose` | Include detailed run output. |
| `--sw-start-at STEP` | Start execution at the selected step. |
| `--sw-stop-at STEP` | Stop execution after the selected step. |
| `--sw-only GLOB` | Select jobs matching this pattern. |
| `--sw-no-memo` | Execute jobs without reusing memoized results. |
| `--sw-priority VALUE` | Scheduling priority for this run. |
| `--sw-no-update` | Skip automatic updates for this invocation. |
| `--sw-no-bincache` | Build without reusing the local pipeline binary cache. |
| `--sw-hash-all-files` | Include all files when computing the local binary cache key. |
| `--sw-allow-unadmitted` | Permit execution without local resource admission. |
| `--sw-dry-run` | Preview the run without executing its jobs. |
| `--sw-allow-risk LABEL[,LABEL...]` | Authorize the listed pipeline risks. Repeatable; comma-separated values are accepted. |
| `--sw-index PATH` | Pipeline discovery index path. |
| `--sw-run-handle-file PATH` | Write the submitted run handle to this file. |
| `--target TARGET` | Select a declared pipeline target. |
