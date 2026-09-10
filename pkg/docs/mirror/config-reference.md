<!-- GENERATED from the sparkwing.yaml schema structs (pkg/pipelines, pkg/projectconfig) by internal/configref. Do not edit by hand; regenerate with `bash bin/gen-config-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# Config reference

Reference tables for selected `.sparkwing/sparkwing.yaml` structs, generated from the Go structs the config parser enforces. `Required` reflects whether the field may be omitted. See [storage backends](backends.md) for profiles and [spark libraries](sparks.md) for library configuration.

## Top level

| Field | Type | Required | Description |
|---|---|---|---|
| `defaults` | `Defaults` | no | Defaults carries the per-pipeline fields each pipeline inherits unless it declares its own. See Defaults for the per-field merge semantics. |
| `profiles` | `map[string]*profile.Profile` | no | Profiles maps profile name to its surface bundle. The same shape as ~/.config/sparkwing/profiles.yaml's profiles map; project profiles get referenced from inside the project (pipeline.profile, defaults.profile), user profiles from the CLI (--profile). |
| `pipelines` | `[]pipelines.Pipeline` | no |  |
| `sparks` | `[]sparks.Library` | no |  |

## `defaults`

| Field | Type | Required | Description |
|---|---|---|---|
| `profile` | `string` | no | Profile names the project profile (from Config.Profiles) that applies when neither --profile nor pipeline.profile is set. Empty means "no default" -- a pipeline without its own profile: still runs (against the sqlite-only test/dev shape). Wholesale-replaced by pipeline.profile when set. |
| `args` | `map[string]string` | no | Args supplies per-arg default values for every pipeline. Each key is layered under pipeline.args (pipeline wins per-key), and the merged map sits in the priority chain between schema.Computed and the explicit operator CLI flag. |
| `guards` | `pipelines.Guards` | no | Guards apply to every pipeline. Wholesale-replaced by a pipeline that declares its own non-empty guards block. |
| `requires` | `[]string` | no | Requires are runner labels every pipeline's jobs must satisfy in addition to their own Job.Requires(). Wholesale- replaced by pipeline.requires when set. |

## Pipeline entry (a `pipelines:` list item)

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | `string` | **yes** | Name is the invocable name (`sparkwing run <name>`); must equal the string passed to the SDK's Register call. It must match `^[A-Za-z0-9][A-Za-z0-9._-]*$`. |
| `entrypoint` | `string` | **yes** | Entrypoint is the Go pipeline struct type that implements this entry (equals the struct name). Required. |
| `description` | `string` | no | Description is the one-line summary surfaced by `pipeline list`. |
| `on` | `Triggers` | no | On declares the triggers that auto-fire this pipeline. Absent means manual-only (a command invoked by name). |
| `hidden` | `bool` | no | Hidden omits the entry from default `pipeline list` output; it stays invocable by exact name and shows under `list --all`. |
| `guards` | `Guards` | no | Guards gate dispatch on the resolved profile, args, and git branch. Reject fires before any step runs when any token matches; Require fires when not every token matches. Token vocabulary: `profile:local`, `profile:controller`, `profile:name=<name>`, `arg:<flag>=<value>`, `git:branch=<name>`, `git:branch=default`. A literal branch matches the checked-out head. The default token matches only when the dispatch supplies default-branch metadata. `arg:` tokens read the merged argument set the run executes with, so a value supplied by defaults.args or this entry's own args: block is guarded exactly like one typed on the command line. See pkg/pipelines/guards.go. |
| `args` | `map[string]string` | no | Args supplies per-arg default values. Higher priority than schema Default and Computed; lower than an explicit operator CLI flag. Keyed by CLI flag name (kebab-case, matching what the SDK's WithArgs[T] field tags resolve to). |
| `profile` | `string` | no | Profile names the project profile (from sparkwing.yaml's profiles map) this pipeline uses. Empty means "fall back to the project's defaults.profile selector". The CLI's --profile flag (which targets ~/.config/sparkwing/profiles.yaml) overrides this when present. |
| `requires` | `[]string` | no | Requires are runner-label requirements all jobs in this pipeline must satisfy in addition to their own Job.Requires(). Wholesale replaces defaults.requires when non-empty. The reserved label "local" keeps fleet helpers from claiming the node; --sw-local-only instead selects local storage backends. |

## `guards`

| Field | Type | Required | Description |
|---|---|---|---|
| `require` | `[]string` | no |  |
| `reject` | `[]string` | no |  |

## Triggers (`on:`)

| Field | Type | Required | Description |
|---|---|---|---|
| `push` | `PushTrigger` | no | Push fires on a git push the controller receives via webhook. |
| `pull_request` | `PullRequestTrigger` | no | PullRequest fires on a GitHub pull_request event the controller receives via webhook. The run checks out the PR head; base ref and PR number reach the pipeline on RunContext.Trigger.PullRequest. |
| `schedule` | `ScheduleTriggers` | no | Schedule fires the pipeline on one or more cron cadences. It accepts one mapping or a list of mappings; every entry declares where it fires, so a bare cron string is refused. |
| `webhook` | `WebhookTrigger` | no | Webhook exposes a custom HTTP path that fires the pipeline. |
| `pre_commit` | `PreHookTrigger` | no | PreHook fires from the installed git pre-commit hook. |
| `pre_push` | `PostHookTrigger` | no | PostHook fires from the installed git pre-push hook. |
| `post_commit` | `PostCommitHookTrigger` | no | PostCommitHook fires from the installed git post-commit hook, after the commit is recorded. It never blocks or aborts the commit. |

## `on.push`

| Field | Type | Required | Description |
|---|---|---|---|
| `branches` | `[]string` | no | Branches records the intended push branch globs. It does not gate webhook dispatch. |
| `paths` | `[]string` | no | Paths records the intended changed-path globs. It does not gate webhook dispatch. |

## `on.pull_request`

| Field | Type | Required | Description |
|---|---|---|---|
| `actions` | `[]string` | no | Actions records the intended pull_request actions. The controller applies its opened, synchronize, and reopened set independently. |
| `branches` | `[]string` | no | Branches records the intended pull-request base branch globs. It does not gate webhook dispatch. |

## `on.schedule`

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | `string` | no | Name distinguishes several cadences on one pipeline and appears in `sparkwing crons list` as <repo>/<pipeline>/<name>. Required once a pipeline declares more than one entry; a lone entry is named "default". It matches `^[a-z0-9][a-z0-9-]*$` and is at most 40 characters. |
| `cron` | `string` | **yes** | Cron is a five-field cron expression (minute hour day-of-month month day-of-week) with the usual lists, ranges, steps, month and day names, and the @hourly/@daily/@weekly/@monthly/@yearly aliases. |
| `where` | `string` | **yes** | Where says which side fires this entry: "local" fires from a host that armed it with `sparkwing crons install`, "controller" from a controller it was installed on. One side per entry, and there is no default, so nothing fires somewhere you did not say it should. Declare one entry per side to fire from both. |
| `tz` | `string` | no | TZ is the IANA zone the expression is read in, such as America/Denver. Default UTC. The word "local" means the zone of the host that runs the schedule. |
| `overlap` | `string` | no | Overlap decides what happens when the cadence comes due while the previous scheduled run is still running: "skip" (default) records the fire as skipped, "queue" launches it anyway and lets admission order it. |
| `catch_up` | `string` | no | CatchUp is how long after its due minute a fire may still happen when the host was asleep or the timer was late, as a Go duration such as 1h or 30m. Default 1h; values under 2m are rejected. A due minute older than the window is recorded as missed. |
| `args` | `map[string]string` | no | Args supplies argument values for this cadence's runs, keyed by CLI flag name exactly like `args:` on the pipeline. They sit above pipeline.args and below a host's own override for the schedule, so guards' `arg:` tokens read them and the fire records what it ran with. |

## `on.webhook`

| Field | Type | Required | Description |
|---|---|---|---|
| `path` | `string` | **yes** | Path is the HTTP path the controller exposes to fire the pipeline (e.g. /review). |

