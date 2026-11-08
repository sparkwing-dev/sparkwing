<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing run

Every `sparkwing run` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing run`

Invoke a pipeline

Runs locally by default through the local runner. Foreground commands observe persisted output; standalone and foreground publication keep their CLI host. --sw-route cloud --profile NAME sends planning and jobs directly to Cloud, including without a terminal. --sw-detached returns after intake; otherwise the CLI follows logs. Explicit Cloud routing never falls back locally. --profile requires --sw-route cloud. Repository local restrictions still apply.

### Burst and overflow

Set route: cloud and profile: NAME in machine run.routing to opt in. Repository defaults.run and pipelines[].run may only restrict routing to local and declare secret names. Machine configuration authorizes untracked source_files. The CLI hosts the Cloud-owned run and streams its logs. Ctrl-C requests cancellation. The default preference is here for 5s, then your registered personal machines for 5s, then Cloud. Other team runners cannot claim burst jobs. Prefer entries accept target and a non-negative hold duration. A controller that is unreachable before intake falls back to a local run and reports it. Authorization failures and a lost intake response remain failures because an accepted run keeps one owner. See [local execution](local-execution.md#burst-and-overflow) for configuration and secret handling.

Publication is one-way run/results/logs with local provenance; a publication failure never changes the local run verdict.

--agent NAME records the coding agent that starts the run on your behalf, such as claude-code:land-agent, and --agent none records none. Without the flag, SPARKWING_AGENT applies, then a detected coding-agent environment (Claude Code, Codex, Gemini CLI). The label is sealed with the request, kept by retries and publication, and shown in run views; it never grants or limits authority.

Cache-from reads only authorized verified-default-branch memo/artifacts/depcache; no shared writes or remote binary fetch.

-o applies to foreground and detached runs, their help and their errors; it beats SPARKWING_LOG_FORMAT, then terminal detection.

--sw-only keeps matching jobs and their dependencies. Other declared jobs are skipped. Recovery jobs and jobs added by generators retain their normal rules. --sw-start-at and --sw-stop-at select a window in each job containing the named steps. A job containing neither bound runs all its steps. Unknown step names and ranges selecting no steps in a job fail before work starts. Bounds follow dependency paths, not declaration order; omit both to run parallel steps. --sw-only cannot be combined with either step bound. All three selections are sealed at intake and survive resume and retry. Child pipelines resolve their own job and step selections because their jobs and steps have separate names.

--sw-allow-unadmitted selects standalone CI with an exclusive in-process local controller. Give it a private SPARKWING_HOME. Its admission queue and concurrency rules remain active; it does not join shared machine admission. The CLI joins controller shutdown before returning. Detached runs and Cloud routing cannot use this mode.

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
| `--sw-route ROUTE` | Select local execution or direct Cloud execution for this invocation. Values: local, cloud. |
| `--profile NAME` | Cloud connection profile; requires --sw-route cloud. |
| `--agent NAME` | Coding agent starting this run, such as claude-code:land-agent; none records no agent. Display-only. |
| `--sw-cache-from PROFILE` | Read the authorized remote cache; an empty value disables the machine default. |
| `--sw-pause-before JOB` | Pause before the selected job. Repeatable; comma-separated values are accepted. |
| `--sw-pause-after JOB` | Pause after the selected job. Repeatable; comma-separated values are accepted. |
| `--sw-pause-on-failure` | Keep failed jobs paused for inspection. |
| `--sw-ref REF` | Build the pipeline and run against this Git ref. |
| `--sw-pipeline-ref REF` | Load the pipeline from this Git ref. |
| `--sw-detached` | Submit work to the selected controller and return before it finishes. |
| `--sw-workers N` | Positive foreground worker count; refused for detached runs. Must be at least 1. |
| `--sw-idempotency-key KEY` | Deduplicate detached submissions using this key. |
| `--sw-request-id ID` | Request identifier for a detached submission. |
| `-v, --sw-verbose` | Include detailed run output. |
| `--sw-start-at STEP` | Start execution at the selected step. |
| `--sw-stop-at STEP` | Stop execution after the selected step. |
| `--sw-only GLOB` | Select jobs matching this pattern. |
| `--sw-no-memo` | Execute jobs without reusing memoized results. |
| `--sw-priority VALUE` | Scheduling priority for this run. |
| `--sw-no-update` | Skip automatic updates for this invocation. |
| `--sw-no-bincache` | Build without reusing the local pipeline binary cache. |
| `--sw-hash-all-files` | Include all files when computing the local binary cache key. |
| `--sw-allow-unadmitted` | Use an exclusive in-process local controller for CI admission. |
| `--sw-dry-run` | Preview using declared dry-run bodies and steps marked safe. |
| `--sw-allow-risk LABEL[,LABEL...]` | Authorize the listed pipeline risks. A run narrowed with --sw-only, --sw-start-at or --sw-stop-at needs only the risks of the steps it selects. Repeatable; comma-separated values are accepted. |
| `--sw-index PATH` | Capture a Git index for local staged-content checks. |
| `--sw-run-handle-file PATH` | Write the submitted run handle to this file once at intake; it names the run and does not track its status. |
