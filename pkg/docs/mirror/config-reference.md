<!-- GENERATED from the sparkwing.yaml schema structs (pkg/pipelines, pkg/projectconfig) by internal/configref. Do not edit by hand; regenerate with `bash bin/gen-config-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# Config reference

The complete `.sparkwing/sparkwing.yaml` schema, generated from the Go structs the strict config parser enforces. **Any key not listed here is rejected at load.** `Required` reflects whether the field may be omitted.

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
| `name` | `string` | **yes** | Name is the invocable name (`sparkwing run <name>`); must equal the string passed to the SDK's Register call. |
| `entrypoint` | `string` | **yes** | Entrypoint is the Go pipeline struct type that implements this entry (equals the struct name). Required. |
| `description` | `string` | no | Description is the one-line summary surfaced by `pipeline list`. |
| `on` | `Triggers` | no | On declares the triggers that auto-fire this pipeline. Absent means manual-only (a command invoked by name). |
| `hidden` | `bool` | no | Hidden omits the entry from default `pipeline list` output; it stays invocable by exact name and shows under `list --all`. |
| `guards` | `Guards` | no | Guards gate dispatch on the resolved profile + args. Reject fires before any step runs when any token matches; Require fires when not every token matches. Token vocabulary: `profile:local`, `profile:controller`, `profile:name=<name>`, `arg:<flag>=<value>`. See pkg/pipelines/guards.go. |
| `args` | `map[string]string` | no | Args supplies per-arg default values. Higher priority than schema Default and Computed; lower than an explicit operator CLI flag. Keyed by CLI flag name (kebab-case, matching what the SDK's WithArgs[T] field tags resolve to). |
| `profile` | `string` | no | Profile names the project profile (from sparkwing.yaml's profiles map) this pipeline uses. Empty means "fall back to the project's defaults.profile selector". The CLI's --profile flag (which targets ~/.config/sparkwing/profiles.yaml) overrides this when present. |
| `requires` | `[]string` | no | Requires are runner-label requirements all jobs in this pipeline must satisfy in addition to their own Job.Requires(). Wholesale replaces defaults.requires when non-empty. The reserved label "local" pins execution to the in-process runner (same effect as --sw-local-only). |

## `guards`

| Field | Type | Required | Description |
|---|---|---|---|
| `require` | `[]string` | no |  |
| `reject` | `[]string` | no |  |

## Triggers (`on:`)

| Field | Type | Required | Description |
|---|---|---|---|
| `push` | `PushTrigger` | no | Push fires on a git push the controller receives via webhook. |
| `schedule` | `string` | no | Schedule is a cron expression the controller evaluates. |
| `webhook` | `WebhookTrigger` | no | Webhook exposes a custom HTTP path that fires the pipeline. |
| `pre_commit` | `PreHookTrigger` | no | PreHook fires from the installed git pre-commit hook. |
| `pre_push` | `PostHookTrigger` | no | PostHook fires from the installed git pre-push hook. |

## `on.push`

| Field | Type | Required | Description |
|---|---|---|---|
| `branches` | `[]string` | no | Branches limits the trigger to pushes on these branches (glob patterns); empty matches any branch. |
| `paths` | `[]string` | no | Paths limits the trigger to pushes touching these path globs; empty matches any path. |

## `on.webhook`

| Field | Type | Required | Description |
|---|---|---|---|
| `path` | `string` | **yes** | Path is the HTTP path the controller exposes to fire the pipeline (e.g. /review). |

