# C4: surface and configuration contracts

## Submitted environment and standalone CI

`procenv.Submit` captures only present user names matching the machine's
`run.submit_env_allow`; credentials and engine-owned names are dropped. Values
of at least 12 bytes and embedded proxy credentials are registered before
inspection or subprocess startup. Empty values and native bytes are retained.
`runspec.VerifyEnvironment` binds byte-preserving private values to EnvSnapshot's
sorted names and digest. `procenv.WithSubmitted` supplies the verified values to
the remote base for installation, planning, attempts and evaluation. Step tools
and cleanup inherit the attempt environment. Submitted values override ambient
runner values of the same name.

`--sw-allow-unadmitted` selects decision 91's exclusive in-process local
controller, rather than the shared machine admission service. The same runtime
owns the home, store, queue, admission, concurrency and workers. It keeps the
election lock and startup reconciliation and joins shutdown on command return.
It refuses detached execution and Cloud routing. A home already owned by a
controller is refused without opening its store or stopping that owner. CI
should provide a private SPARKWING_HOME. No resource or concurrency policy is
silently bypassed.

## Retained run build policy

Trigger and Cloud overflow inspection use the acquired SDK entry through the
shared SDK planner. There is no second CLI preparation with default policy.
`Source.NoBincache` and `HashAllFiles` govern that installation, hosting and retries.
Local and Cloud relative priority use the same queue resolver immediately before
intake. Overflow preparation resolves its rank after uploading the source bundle.

## Run target selection

`run --target` is removed. Pipeline names select deployment shapes, as recorded
in the v0.6.0 migration. The registry, help and current run documentation carry
no target flag. Author arguments named target remain opaque after `--`.

## Overflow account controls

`sparkwing overflow show --profile NAME` reads the authenticated account's
allowance and current usage. `overflow set --profile NAME` requires exactly
one of `--minutes N` or `--uncapped`. Zero prevents new Cloud overflow, and
uncapped removes the saved cap. The existing team tier and billing ladder remain
in force. Explicit profiles keep this Cloud account setting separate from local
execution settings.

JSON preserves `monthly_minutes` (including null), `used_seconds` and UTC
`period_start`. Plain emits only the allowance in minutes or `uncapped`.
Pretty labels the allowance, usage and period. Validation and controller errors
emit no success receipt. The command adds no new routes, grants or settings.

<!-- design:begin -->

## Amendment 1: environment, presence and managed units

[Amendments 1](amendments-1.md) records the cross-area consumers.

- `procenv.Git` is the shared acquisition process kind. It uses the filtered base
  and network/CA settings, drops inherited Git repository/index/config overrides,
  SSH command/agent/askpass controls, preserves GIT_SSL_CAINFO/GIT_SSL_CAPATH,
  and receives selected private configuration
  through source-owned argv. Source authority may use approved owner credentials
  under its repository allow-list. It never copies ambient credentials into env.
  Install keeps the ordinary credential-free base. No arbitrary os.Environ fallback
  is permitted in source acquisition.
- Local, child and schedule execution retain captured snapshots rather than a
  daemon's current environment. W3/W4/W8 consume procenv, startup/body splitting,
  guard results and shared deadlines. Startup settings cannot change declarations.
  Execution and inactivity budgets remain distinct.
- `procenv.Runtime.GateIndex` is an optional string path, projected only from
  a verified local snapshot. The path remains in private snapshot storage,
  never a wire field; remote ambient input cannot bind a hook index. CLI intake
  copies `--sw-index` (or the hook index) into private environment storage after
  Git normalizes it to a self-contained index. Its content hash names the file.
  Each snapshot load checks its location, ownership, permissions and bytes before
  process startup. Retries retain the index and environment identity. Cloud routing
  refuses `--sw-index` because its path is local. Source inputs remain unchanged.
- Network proxy settings are the existing passed-and-masked exception. Register
  credential components for URL and scheme-less proxy forms, including GOPROXY and
  PIP_INDEX_URL. A mask of only the whole string is insufficient when tools print
  just the embedded credential. Other credential-shaped input stays excluded.
  One procenv endpoint parser splits GOPROXY on commas and pipes, pip extra indexes
  on whitespace, and npm registries and single proxy/index settings as one endpoint.
  It parses each authority through net/url and registers only raw and decoded
  userinfo components; host, path and public list members remain public. The
  separate remote-input whole-value masking rule still applies.
- Schedule inference chooses batch only when no explicit admission class wins.
  It is a default, not a request override above pipeline code. The final guard
  grammar uses where:local/controller. Shared parsing and YAML alias traversal
  belong to W6, with each YAML graph node validated once per relevant context.
- Concurrency optional scope/capacity/cost/on_limit remain absent on the wire until
  ResolveConcurrency. Defaults are global/1/1/queue. Group is required. Explicit
  zero capacity/cost fails. Resolved winners persist separately from declarations.
- Source.Location stores immutable acquisition location. Secret argument
  classifications may grow at the second seal but cannot shrink. Display never
  exposes raw arguments, source credentials or secret-bearing hashes.
- `limits.Record` is implemented at the existing 128 KiB limit. It bounds already
  masked UTF-8 using a head, an elision note and a tail. Storage still refuses
  oversized raw records. No new cap, deadline or environment variable is added.

LocalRuntime declares AwaitReady, Status, SetStay and Drain beside Run. A runtime
is bound to one home, owns one store and uses the shared controller, queue and UI.
Readiness includes startup reconciliation. Drain joins shutdown. The four existing
activity counts determine idle exit, with stay as an explicit override.

The CLI's daemon status, stop and restart adapters consume `LocalLifecycle`
through the same local-services binding as dashboard, doctor and update.
Status projects only `LocalStatus`. It never opens the store or probes the old
daemon transport. Restart preserves a stopped controller, skips the same exact
version unless forced, joins Stop, confirms stopped status, then calls
EnsureStarted with the captured home/executable/address and preceding stay value.
An answering controller after Stop or an unready/wrong-version start is an error.
No success receipt is emitted after a lifecycle, cancellation or output failure.
The existing 2/20/30-second status/restart/stop observation budgets remain.
Native local-services construction and readiness remain W8 integration points.

`daemon events` reads the retained local journal with the final clisurface
grammar. It selects the newest 50 matching records by default and accepts
positive limits. Opaque cursors bind the journal directory, normalized filters,
initial since cutoff and exact unread prefix. Newer appends keep continuation stable.
Missing, inserted, reordered or edited unread records fail with a restart hint. The cursor
contains hashes, cutoff and count, never record payloads. It grants no authority.
JSON emits event rows followed by a page record with next_cursor and truncated,
including empty and final pages. Plain/pretty continuation goes to stderr.
`--explain --run` renders the complete related timeline without page metadata.
It conflicts with list filters and pagination. Offset and zero-limit paths are
deleted. Journal producer lifecycle remains W8-owned.

Managed controller units call foreground `wingd run --stay --home <absolute-home>`.
Managed timers call `__tick --home <absolute-home>`. Public dashboard start stays a
lifecycle client. ManagedUnit retains Home and update re-renders both kinds before
restarting installed services. These hidden home arguments replace environment
plumbing and do not restore a public --home flag or removed command alias.

Runner enrollment writes runner.repos and a private credential file under
runner.credentials_dir. Startup reads that directory. Failed enrollment revokes
newly minted authority and removes only files it created. W2/W6/W8/W9 share this
contract. Cloud Job scratch remains the fixed aggregate 20 GiB including DinD,
enforced by Kubernetes accounting/eviction. Real node/network evidence and full
teardown remain W8 and deployment acceptance obligations.

Local Go preparation resolves project sparks through the existing resolver before
source installation and intake. Exact versions need no version lookup. Tracking
versions fail with the existing `--sw-no-update` hint when resolution fails.
That invocation flag skips resolution and keeps the current module pins and
overlay. The selected engine still installs and verifies the SDK through C3.
Foreground output uses the sealed output selection. It emits `run_start` after
intake and `run_finish` with run ID, pipeline and actual status only after the
host returns its committed terminal verdict. JSON uses event/attrs records.
Plain and pretty output include the same run identity and status. Failed or
cancelled outcomes keep their nonzero exit. Output failures remain errors.

Status: process environments, limits, settings and both YAML schemas are implemented.
Source location validation, Git environments, concurrency resolution, shared
guard parsing and enrollment rollback still need W6 integration under
A1-07, A1-11, A1-12, A1-47 and A1-49.
Project-file readers use the strict schema. `Config.Registry` projects validated
project declarations into dispatch metadata for the existing CLI and hosts.
Settings winners remain owned by `settings.ResolvePlan` and the store seals.
`runspec.Options.StartAt`, `StopAt` and `Only` retain the CLI's step-window
selections as `Setting[string]`. Intake sets them through the shared settings
table. Plan resolution preserves their values and origins, and `SealSpec`
refuses a changed selection. Retry carries them with the original request.
W3 consumes these sealed values when validating and executing the plan.
CLI grammar, host execution and local lifecycle have separate owners.
The acceptance suite runs each configuration boundary under its scenario's
`runspec-config` cell. Placement cells require the T1 driver and real hosts.

## Ownership

The internal launcher grammar requires `--docker-image` beside `--image` and
does not accept `--deadline`. Both images are digest-pinned by W8's validator.
`fetch-source` and `run-node` permit an absent `--job` for a plan award. W8
validates the private award's kind and binding before source or execution.

W9 fills the library-inflation selector omitted from the initial registry.
`pipeline sparks inflate --library MODULE` takes a required sparks-core name or
full Go module path, matching the library selector used by catalog. There is no
`--module` alias. Plain output is the destination. JSON retains module, version,
destination and replacement. The shared parser governs help and execution.

C4 owns process environment construction, shared limits and deadline arithmetic,
configuration precedence, both YAML file shapes, command grammar, local controller
lifecycle and the common Cloud Job builder. The store owns sealing and authority;
C4 returns values to its transactions. The SDK owns declarations and applies body
env after planning; C4 decides which env belongs at process startup.

Examples use `--example`, with `--body` requiring that selector. Plain lists
emit names, and a selected plain body emits source only. Sparks add requires
`--source`; remove requires `--spark`. Their JSON receipts retain action,
spark, source, version and project-file path. Plain receipts emit the spark name.
Repository updates select the current checkout unless `--all-repos` is set.
Apply is the default; `--dry-run` previews, including read-only SDK pin inspection
with `--in-place`. Fleet application requires `--yes` before reading the fleet.
Every baseline plan must succeed before a repository SDK update can bump or
commit. The subprocess adapter uses `pipeline plan --pipeline NAME`.

| Concern | Authoritative source |
| --- | --- |
| Env classes, names and process policies | `internal/procenv` |
| Typed bounds and deadline helpers | `internal/limits` |
| Precedence, per-setting signatures and source table | `internal/settings` |
| Project YAML schema | `pkg/projectconfig.Config` |
| Machine YAML schema | `internal/userconfig.Config` |
| Commands, flags, parsing constraints, exit/output rules | `internal/clisurface` |
| On-demand local controller and managed units | `internal/wingd.LocalLifecycle` |
| Cloud plan/eval/work Job construction | `internal/runners/launcher.BuildJob` |

These packages use the final import root `github.com/sparkwing-dev/sparkwing`.
The schemas are the tagged Go types plus their validation contracts. They are not
an additional copy of the accepted run spec. Machine section reads validate the complete file before decoding a section.
Project connections and legacy file migrations are refused or removed. YAML
validation checks each alias target once per destination schema type, retaining
cycle and unknown-field refusals before typed decoding.

## Invariants

### Process environment

- Every process boundary goes through `procenv.Build` and its kind/class policy.
  Engine-owned names cannot be supplied by a pipeline. Unknown user-owned
  `SPARKWING_*` names remain valid, subject to credential filtering.
- Local runs use their captured snapshot, children inherit that snapshot, and
  schedules capture at install. A daemon's ambient env never replaces it. Private
  snapshots use `fssecure`; the run spec carries names, digest and snapshot identity.
  Capture and verification hash the same sorted, byte-preserving assignments,
  including native bytes that are not valid UTF-8.
- Remote input is the registry's minimal/network/CA base plus `run.submit_env_allow`.
  Credential-class and credential-shaped names are dropped even when explicitly
  allowed. `envredact.CredentialName` remains the shared shape classifier; procenv
  owns the decision to exclude it. Values at least 12 characters long are masked
  before child execution. Proxy credentials must be registered with the masker,
  including scheme-less HTTP proxies, dependency proxy lists, and both escaped
  and decoded userinfo.
- No capability, bootstrap, controller credential or protocol handle travels in
  env. Protocol v2 carries the node bearer in hello and Windows handles in argv.
  Removed names remain deny entries only; they are never compatibility inputs.
- `SplitJobEnv` puts marked assignments and NODE_OPTIONS/GODEBUG/GOMAXPROCS into
  startup env. Plan has no job startup env. Attempt/eval receives startup env
  before spawn; a replan changed by it fails as `plan_drift`. The SDK applies all
  other job assignments after planning. Tools and cleanups inherit that body env.
- Runtime identity comes from the claim. Public names use JOB, not NODE. Ratified
  source fields and CI=true are included; unknown source facts stay absent.

### Limits and settings

- `limits.Table()` is the shared number source. Durations use `time.Duration`,
  byte sizes `int64`, counts `int`. Runtime timing and size values are not mutable
  package variables. Single-use internal constants may remain with their owner.
- Remote owner execution defaults to six hours, adjustable up or down by the
  operator. Local runs have no execution deadline. Queue wait defaults to 24 hours
  and queued jobs also end at a finite owner deadline. Approval zero remains
  indefinite for local and controller-owned runs; a remote host's execution
  deadline bounds its waits. There is no total wall-time cap.
- `Child` reserves a named margin inside the actual remaining parent budget.
  `Renew(ttl)` is ttl/3; `CheckRenewal` requires two renewals strictly before expiry.
  `JobDeadline` adds JobSlack to the attempt deadline and caps at owner expiry.
  Credential issuance also caps at parent capability expiry. Store time alone
  judges lease liveness.
- Retry >10, memo TTL >30 days, output >16 MiB and catch-up >24 hours fail rather
  than clamp. Fan-out is at most 256 generated jobs. An OOM is ordinary job failure.
  Errors are masked then bounded to 8 KiB head/tail; annotation/summary is 128 KiB.
  Store rechecks refuse oversize data rather than cutting raw structured bytes.
- The source order is flag, equivalent env, sealed run, pipeline YAML, YAML
  defaults, pipeline code, machine file, shipped default. Each setting accepts
  only the sources in `settings.Table`. Operator precedence is named flag, team DB,
  global DB, limits profile, default. Operator results are ceilings, never author
  preferences. A refusal names both nonsecret values and origins.
- Intake resolves request/machine/env inputs once. Acceptance resolves checkout
  and code inputs and reapplies request overrides without rewriting the request.
  A host consuming accepted values never reads its own preferences again.
- Admission inference uses the sealed request origin at acceptance. Schedule and
  retry origins default to batch. Explicit request and code classes take precedence.
  `settings.Context.Origin` carries this fact to the resolver and never becomes an
  intake override.
- Nil means unset. Explicit zero, false, an empty list or an empty map keeps its
  documented meaning. Invalid settings fail with source/file/key/line information;
  they never warn-and-default. Defaults apply to pipelines with no YAML entry.
- Args merge per key. Schedule host args override schedule args before request
  sealing. Pipeline requires replaces defaults, then job requirements add; run
  requirements and allowed pools remain inherited. Guards replace as one block.
- Request-rate/ring/stream/download overload guards are per replica. Run-rate and
  usage quotas are deployment-wide store facts with shared atomic reservations.
  `max_runs_per_hour` supplies the sole per-principal guard's DB override.
- Retention keeps run and metric policies separate. Newest successful run/output
  pins survive retention; log classes still expire. Financial rows never cascade.
- Machine resource budgets and local cache byte sizes are parsed by userconfig.
  Runtime and CLI consumers call those parsers directly.
- Runner enrollment writes `runner.repos` and `runner.credentials_dir`. Its
  `agent-token` file is private and read by the runner configuration loader.
  Configuration replacement uses a new credential directory so a failed write
  leaves the preceding credential intact.

### Settings across the seal points

`ResolveRequest` returns `runspec.Options` with resolved winners stored directly
as `runspec.Setting[T]`. `ResolveIntake` validates captured request origins and
resolves them against trusted controller inputs. `Values` embeds those native
options. Startup, argument and guard inputs keep their separate source metadata.
Explicit false, zero and empty collections stay present. Nested resources, pause controls and checkout fields
retain separate origins. `OriginLayers` maps every C4 source to C1, including the
three operator sources. File, key and line survive the conversion. A captured
winner keeps its original origin when another host consumes the sealed request.

`RunnerSelection` chooses any, name, label or here for one run.
The CLI retains `pipeline trigger --runner here --reason TEXT` as
`Request.Reason`, which participates in intake hashing and survives replay and retry.
`RunnerPreferences` resolves the machine's runner configuration at startup.
Dry-run, no-memo, risk permissions, pause controls, debug, workers, output and
publication/cache profiles all have request fields. Startup credentials and
machine runner settings stay outside the request projection.

C1 runs `nodeproto.ValidatePlan`, `settings.ResolvePlan` and secret preflight in
its acceptance transaction. Structural validation cannot accept a plan by itself.
`PlanInputs` takes the server-owned sealed request, `runspec.CheckoutDeclarations`
and the decoded `PlanDoc`. The declaration projection retains separate pipeline
and defaults layers, explicit presence and source/file/key/line origins. Its
`CheckoutLayer` contains author fields only. It cannot carry startup controls or
credentials, substitute a new machine file/environment, or submit resolved winners
as resolution inputs. Args retain per-key origins as well as empty-map presence.
`GuardDeclarations` contains predicates before evaluation.

`ProjectCheckoutDeclarations` validates the project schema and captures the two
layers without merging them. `ValidateCheckoutDeclarations` checks the version,
field/source membership in `Table`, and every nested origin. Layer identity comes
from the enclosing Pipeline/Defaults field. An origin cannot claim flag, env,
run, machine or operator precedence. Fields unsupported by that source are refused.
Code-only resources, priority, claim wait, admission class and job defaults still
come from `PlanDoc`; their availability in the restricted type grants no YAML
source permission.

C1 and the controller wire carry this same projection into `SealInput.Declarations`.
`Spec.Declarations` retains it separately from `Resolved.Settings`, and its contents
participate in the seal hash. Resumption and retry preserve those declaration
layers unchanged. `ResolvePlan` returns
`runspec.ExecutionSettings`, whose `Jobs` map contains exactly the declared job
IDs. C1 persists it in `Resolved.Settings` at seal point two. Timeout, no-progress
timeout, approval wait, retry/backoff, memo TTL, resources and placement winners
retain their origins. Executors consume those winners.

`ResolveExpansion` takes the accepted spec, including `Spec.Declarations` and
the original sealed request/ceilings, plus generated declarations. It cannot
re-read checkout files or host preferences. C1 uses
it after the same structural validation and before secret preflight and commit.
Each generated node stores its `JobExecutionSettings` beside its declarative
`JobSpec` and spec hash. Initial and generated declarations stay unchanged for
code-reconstruction drift checks. Expansions do not rewrite the run's seals.

`Values.Options.Checkout` and the checkout resolver use
`runspec.Setting[runspec.CheckoutPolicy]` for full history, depth, tags, submodules
and LFS. The policy retains each field's origin. `runspec.Source` and project YAML's source string
retain acquisition identity. They never stand in for checkout policy.
`source.ForRequest` supplies the policy available for initial planning acquisition.
`source.ForExecution` consumes the policy finalized at acceptance.

`ProjectCeilings` preserves operator origins, allowed admission classes and allowed
checkout policies, plus C4's fixed retry and memo bounds. The `AllowedSources`
interpretation is a set of complete checkout-policy tuples. Every tuple field must
be present, including explicit false. An absent field is invalid, not a wildcard.
A resolved policy must equal one tuple. An unset set is unrestricted and an
explicit empty set permits none. This constrains checkout behavior only, not
repository/ref access. It adds no default restriction.

Cores stay `float64` through both seals. Memory stays bytes. Platform unit
conversion belongs at the executor platform boundary. Cloud scratch stays the
fixed limit and has no author field.

### YAML schema

Both decoders reject unknown and duplicate keys, explicit null, extra documents
and malformed values. Empty collections are explicit. Errors never echo tokens.

Project sections are `defaults`, `pipelines`, `sparks`. Defaults contain args,
guards, requires and prefers. Pipeline entries also retain name, entrypoint,
description, hidden, source, profile and on. Guards use when/unless with
`where:local|controller`, `arg:key=value`, `git:branch=name|default`. An unresolved
default branch fails closed. Project-level profiles and `on.webhook` are unknown
keys, including a profile name also present in the machine file.

Events cover push branches/paths, pull_request actions/branches, schedule entries,
and empty pre_commit/pre_push/post_commit hooks. Schedule where is local or
controller, timezone defaults UTC (`local` means host zone), overlap is skip or
queue (default skip), catch-up defaults one hour and must be between two minutes
and 24 hours. One unnamed schedule is `default`; multiple schedules require
unique names matching the existing schedule-name grammar. The install profile
selects the controller separately from `where`.

Machine sections are profiles, repos, admission, runner, cache, machine, debug and
run. Profiles contain only controller URL and optional token in an owner-only
file. The runner uses credentials_dir, repos and keep_free; agent, gitcache,
allow_repos, local_reserve and its unused profile are unknown keys. A runner's
pool authority cannot come from machine labels. Max concurrent defaults to one;
lease, poll and heartbeat explicit zero fail. `accept_working_tree` requires a
repository allow-list. Spawn policy retains the existing return-to-queue mode.

Admission retains classic/off/auto/jev/custom, budget, custom class weights,
backfill delay, aging, interactive burst and the existing Jev predictor fields.
The rewrite changes lifecycle ownership, not admission behavior. Repo settings
retain registered paths, fallback_paths, auto_register and include_worktrees.
Cache max_bytes/max_entries zero disables the individual local cache bound.
Machine box_id falls back to hostname only when unset; unreadable config fails.
Debug pause_timeout is a positive duration. Run holds submit_env_allow and the
read-only cache_from profile default. Logs/drop_policy, run.dispatch_wait and
profile backend/rerun_image settings are unknown keys.

### CLI

`clisurface.Registry()` lists the whole target grammar for sparkwing,
sparkwing-runner and sparkwing-controller, including hidden process entrypoints.
GlobalFlags and CommonRules apply to every row. Unknown engine flags fail;
pipeline arguments start after `--`. Only run and pipeline trigger take a
pipeline positional. Output selection is validated before side effects; lists
are NDJSON and diagnostics use the C2 error object on stderr.

`Flag.AllowEmpty` records an allowed empty scalar. `--sw-cache-from=` clears
the machine cache profile without losing flag presence.

`__complete --word` accepts an empty value to select root completions. Shell
completion requests use `--kind` and `--word`, with explicit plain output.

`clisurface.ResolvePipeline` captures the invoked name and registered SDK
entrypoint from validated project configuration. An unconfigured pipeline uses
the same name for both. A configured entrypoint that is missing from describe
cannot fall back to an SDK registration named after the alias. Help, completion,
arguments, secrets and risks all use the selected entrypoint's schema while
displaying the invoked name. Hosts carry the pair into the sealed source and
resolved entrypoint. Protocol v2 plan requests select the entrypoint in
`Request.Pipeline` and retain the invoked name in `RunContext.Pipeline`.

`pipeline plan --pipeline NAME -- <pipeline args>` accepts opaque pipeline flags
after the separator while retaining the kind-named selector. It resolves the
configured entrypoint for SDK requests and keeps the invoked name in RunContext.
Plan previews redact classified arguments and structured planning failures.

- `run` is local. `--publish P` uploads the record/results/logs with local
  provenance under trust_local_builds; publication failure cannot fail the run.
  `--sw-cache-from P` or run.cache_from reads only authorized verified-default-
  branch memo/artifacts/depcache, never writes, and never fetches pipeline binaries.
- `pipeline trigger --resume RUN` reattaches that here run with its sealed spec;
  it neither creates another run nor grants other run authority.
- `--sw-workers` remains a positive local flag; detached execution refuses it.
  SPARKWING_WORKERS is removed. Pause selection is request data from --sw-pause-*.
- Schedules is a hard cut; managed units call hidden `__tick`. No crons alias,
  cluster worker, debug rerun, runs bounce/consumer or separate cache service exists.
- Operator CLI verbs use only deployment-admin operational scopes. Console-only
  trust/grants/freezes/refunds/account deletion stay console-only. In-pod admin is
  the break-glass surface. C2 owns authorization, not this grammar table.

### Local controller and Cloud Jobs

The local runtime is the same controller code in local mode and the sole
state.db owner. It owns admission, queue/consumer, reaper and dashboard. Local
runs still execute in the CLI; standalone in-process CI has exclusive store
ownership. Started local orphans fail instead of being requeued.

Runs, queued triggers, held leases and dashboard connections independently hold
the controller alive. Health probes and installed schedules do not. `--stay`
pins it until stopped. Linux/macOS install a login service with restart on
failure and no socket activation. Update re-renders all managed units and
restarts an installed controller. Doctor reports skew, incompatible stores and
removed timer verbs. Windows supports stay and daemon schedules; install is
refused. Kernel UID or client SID establishes local identity; absent evidence
refuses access. Private files use fssecure on every supported OS.

`wingd.APIConn.PeerIdentity()` carries `LocalPeerIdentity` from the verified
transport to the shared controller. The connection retains its own snapshot
and returns a caller-owned UID copy. `LocalSubject` checks the peer against
the controller account before the API listener yields the connection.
This replaces the UID-only accessor. `DialSocket` and `ListenSocket` own the
platform transport. Windows uses owner-only local named pipes and checks the
client's identification-token SID before application traffic. Unix uses kernel
UIDs. Missing identity refuses access. Listener shutdown joins pending transport
work and retains authenticated connections for drain. See
[the transport contract](../../../internal/wingd/LOCAL-TRANSPORT.md) for framing
and native acceptance requirements.

Every plan/eval/work Cloud Job runs root with privileged DinD, including jobs
from run-host dispatch. Before a runnable object exists, CloudBoundary must
reserve exactly one Job on a team-dedicated node and establish enforced default-
deny egress, allowed destinations, DaemonSet-port inventory and the escape probe.
The builder cannot silently fall back to a shared node. DinD is not credential
isolation. Scratch is fixed at 20 GiB. Bootstrap is an owner-only Secret file,
source init uses it, and run-node exchanges it once for an in-memory capability.
No launcher root credential is passed to a Job. Teardown releases the node only
after all containers stop, and deletes the bootstrap Secret on every path.
Nodes retain their reservation's award LeaseRef. CloudJobBinding.LeaseRef owns
the award-fence projection, including the eval unit's lease generation. Both Job
construction and restart reconstruction require the reservation award to equal
the binding. Reservation-only reconciliation
uses C3's exact award, NodeUID and ReservationID retirement reference, with no
Job reference. Expired unit ownership cannot retain vacant capacity. Only the
reservation's own award under the launcher's ancestry can authorize retirement,
after Job absence and container vacancy are established. Optional ancestor expiry
constrains execution when present and adds no deadline when absent.

## Acceptance obligations

These are target acceptance scenarios for T1 and implementers, not claims that
stubbed functions have been exercised. The four executable contract-data suites
check registry integrity, grammar, typed bounds, precedence order and seal phases.

| Scenario | Required proof | Frozen source |
| --- | --- | --- |
| C4-env-placements | Consistent construction within local-snapshot or remote-allow-list policy; claim-derived runtime identity on every placement; install gets base only | §7.1-7.4 |
| C4-env-credential | No inherited engine-owned or credential-shaped names, even unknown allow-listed names; composed runtime names present; >=12-character remote values masked | §1.7, §7.1 |
| C4-env-phases | Body env cannot alter replan; marked/fixed startup env reaches attempt only and drift fails; cleanup inherits body env | §7.1, A35 |
| C4-settings-sources | Each Table row exercises every allowed source pair, explicit zero/empty, invalid source/file/key, and request/code conflict | §7.5 |
| C4-settings-seals | No checkout reads at intake; request override survives accept; resumed host ignores local preferences | §3.1, §7.5 |
| C4-config-strict | Both schemas reject removed/unknown/duplicate/null keys, extra docs and ambiguous profiles; defaults apply without entry | §7.5, §9.4 |
| C4-deadlines | Remote operator can raise/lower six-hour default; local unbounded; queue bounded by owner; approvals zero indefinite where allowed | §7.6 |
| C4-limits | Boundaries ±1 for retry/memo/output/annotation/catch-up/fan-out; UTF-8 mask-before-bound; failed store recheck | §7.6 |
| C4-renewal | Two renewals fit lease; expired parent/margin fails; Job and claim cannot outlive owner or capability parent | §2.4, §5.1, §7.6 |
| C4-cli | Every command parses kind flags, output and unknown args; root profile before trigger works; removed surfaces fail | §9.2-9.3 |
| C4-here-resume | First claim obeys ten-minute claim_by; later reclaim succeeds after that window while run/credential is live; wrong run, dead parent or expired run fails through C2 | §3.4, §5.1, §9.3 |
| C4-publish-cache | No remote access by default; cache only authorized trusted reads; binary local; publish nonfatal and locally provenanced | §3.1, §8.5 |
| C4-idle | Each activity hold prevents exit, health probe does not; stay pins; CLI-hosted local and standalone ownership | §2.1 |
| C4-managed-units | Linux/macOS login/restart; update rewrites __tick + restarts controller; doctor detects drift; uninstall removes service | §2.1, §9.3 |
| C4-windows | SID refusal/verification and owner DACL; stay/daemon schedules work, login-service install refuses | §5.6 |
| C4-cloud-pod | All unit kinds root/DinD; no shared-node launch; enforcement probe failure refuses; 20Gi scratch/deadline/Secret teardown | §2.6, §5.4-5.5 |
| C4-cloud-exchange | Bootstrap source-only then one exchange, immutable row-derived binding, root ancestry; no env/argv credential | §5.1, §5.4 |
| C4-recovery | Application retry keeps Job/claim; OOM ordinary failure; local orphan fails | §3.3-3.4, §7.6 |

Implementation must generate the env, limits, precedence and command references
from these tables and make their existing drift checks consume that source.
Target contract docs remain separate from live product help until runtime wiring
lands. There is no behavioral changelog claim in this contract-only change.

## Cross-area assumptions

| This contract | Other area/type | Required agreement |
| --- | --- | --- |
| settings.ProjectCheckoutDeclarations/ValidateCheckoutDeclarations | C1 CheckoutDeclarations/Spec.Declarations; C3 wire PlanSubmission | Preserve separate author-only layers in transport and seal; re-resolve using server-owned intake overrides/ceilings |
| settings.ResolveRequest/ProjectOrigin/ProjectCeilings | C1 runspec.Options/Ceilings | Preserve presence and every original source/file/key/line; store owns both immutable seals |
| settings.ResolvePlan/ResolveExpansion | C1 Resolved.Settings/NodeRow.Execution; C3 NodeInput.Execution | Persist job-ID winners separately from declarations/hashes; execute winners and reconstruct code from declarations |
| settings.Checkout | C3 SourceSpec; C1 Options.Checkout; source.ForExecution | Seal typed checkout policy at acceptance separately from acquisition identity |
| procenv.Base/Runtime | C1 runspec env snapshot identity; C3 RunHost/NodeHost | Captured env identity survives retry/children; source facts and granted runner identity supplied explicitly |
| procenv.Assignment/JobEnv | C3 plan env declaration and SDK Startup option | Preserve Startup marker; fixed interpreter names always startup; SDK applies Body after planning |
| limits.Table/Child/JobDeadline | C1 AcceptPlan/TransitionNode; C2 capability issuance; C3 lease.Keeper/ValidatePlan | Same caps, refusal semantics and deadlines; store clock decides liveness |
| settings.Error; clisurface.Errors | C2 apierr error object | Translate once to shared codes/details; no independent envelope |
| clisurface.Registry | C2 route policies and capability grants | CLI metadata never grants authority; here resume and operator split use corresponding route bindings |
| userconfig.Connection/RunnerConfig | C2 credential release and root capability | Files supply credentials; repo strings/labels do not create authority; pool grants are server facts |
| launcher.CloudJobBinding/CloudJobAward | C1 awarded row/attempt identity; C2 Award/Exchange | These are builder input projections, not alternate credential models; generation/attempt and eval source fence match |
| launcher.BuildJob | C3 source.Acquire/NodeHost; C2 bootstrap grant tables | Source-only bootstrap, exact SHA, exchange before SDK, unit-specific fixed grants, same root ancestry |
| wingd.LocalSubject/LocalRuntime | C2 local capability subject/policy; C1 store owner | Kernel UID/SID supplies local identity; same controller policies; sole state.db owner |
| All acceptance rows above | T1 scenario catalog | Launch coverage includes Go/TS, Windows, here, Actions, remote cache and DinD |

## Final log flush

`limits.LogSealGrace` preserves the existing sixty-second final-attempt window.
C2 and C1 derive its deadline from persisted attempt completion. An unsealed
final stream may flush only for the recorded writer and fence, with live
credential ancestry and no newer attempt or generation. This does not extend
credential expiry or give eval units a job-attempt grace exception.

## Authentication and billing bounds

C2 selects a five-second auth-cache bound; C4 owns `limits.AuthCacheTTL`.
Every reuse also requires a fresh, identical store generation vector for the
credential, all ancestors and team membership. The TTL cannot authorize reuse
on its own. Binding liveness and credential expiry remain per-request checks.

`limits.WorkBillingMinimum` and `HostBillingMinimum` are the numerical owners
for the separate job and host minimums. Both are sixty seconds. Plan and eval
remain per-second with zero minimum; customer-runner waits are excluded.
Test/live payment selection uses C2 `PaymentConfig` and private credential
files with the same callback verification and C1 settlement transaction.

## Version policy and upgrades

`limits.ControllerCompatibilityMonths` owns the twelve-calendar-month controller
HTTP API promise. `limits.VersionIndexRefresh` owns the five-minute signed-index
refresh interval. The latter bounds routine refresh, not fallback lifetime.
A previously verified policy remains usable during network or verification
failure. Without one, first-use resolution fails as `unavailable` with retry
guidance. A support-window end alone never prevents an old engine from running.

`internal/engine` owns exact Go/TypeScript pin resolution, signed GitHub release
verification and machine caching. Its version-index notices reach both run output
and the dashboard. The signed minimum and individual revocations carry reasons
and announcement/enforcement dates. See [the engine contract](c3-engine-version.md)
for the JSON shape, trust rules and rollback checks.

`sparkwing upgrade` bumps repository SDK pins and native lockfiles, then verifies
with the matching engine before applying changes. `--version` selects an exact
permitted release and `--dry-run` previews edits. `sparkwing update` retains the
installed machine-software role. Runners prepare verified updates, drain existing
claims while renewing them, preserve credentials/configuration and refresh their
managed services and timers before restart. Schedules survive without reinstall.

## Open questions for the lead

1. **Startup wording.** §7.1 says startup env is "refused in the plan process too"
   but also says it reaches attempt replan and not the first plan. The latter
   precise rule and A35 govern: attempt-only startup env, declaration reads
   undefined and drift refused. C3 must match this reading.
2. **Local external credentials.** Local base is described as captured env minus
   engine-owned names, which could retain user GITHUB_TOKEN/AWS credentials;
   §1.7 and §7.1 also forbid credentials in Build output. This contract follows
   the literal exclusion, including tool-owned credentials, and records the
   discrepancy before implementation. Credential-bearing network proxy URLs
   remain passed and masked as the existing network-base contract requires;
   confirm that this is the intended exception to the env credential wording.
3. **Runner allow-list spelling.** §2.5/§9.1 explicitly choose --repos while §9.2
   generally requires singular repeatables. The registry keeps --repos as that
   explicit exception. Entity selection still uses --repo.
4. **Underspecified spellings.** Machine remote-cache default is run.cache_from;
   runner file credentials use runner.credentials_dir. Bulk actions use
   --all-repos/--all-jobs/--all-pipelines/--all-entries so --all retains its single
   hidden-entry meaning; docs uses --full. Resume may omit the pipeline positional
   and always uses the sealed spec. Explicit --sw-output beats -o, then env, then
   terminal detection. These fill gaps without retaining aliases; ratify before
   implementing parsers.
5. **Scratch accounting.** The fixed 20Gi budget is interpreted as aggregate Job
   scratch including DinD storage, not 20Gi per container. Kubernetes enforcement
   must make that true without requiring pipeline opt-in.
6. **Network evidence.** The design does not supply a destination list or freshness
   interval for enforcement probes. CloudBoundary requires evidence for the target
   node/namespace and current policy revision; no invented timeout is added.


## Verification

- Full repository `GOWORK=off GOFLAGS=-buildvcs=false go build ./...`: passed.
- `go vet` on every new or changed package: passed.
- Registry tests for procenv, limits, clisurface and settings: passed.
- Independent fresh Astra source review: findings corrected (worker flag,
  settings source scope, egress scope and unknown credential-shaped names).
- No Sparkwing binary runs, live state access, git writes, commits or external
  file writes. Runtime acceptance remains implementation work; stub signatures
  are not execution evidence.

## Exported contract API

Each row names a target type, function or registry. Constants live in the typed
family files and complete tables; temporary Old implementations are excluded.

### internal/procenv

| Export | Purpose |
| --- | --- |
| [`Class`](../../../internal/procenv/contracts.go#L7) | Class distinguishes variable authority from the process allowed to consume it. |
| [`Kind`](../../../internal/procenv/contracts.go#L20) | Kind identifies the process boundary at which Build applies policy. |
| [`Var`](../../../internal/procenv/contracts.go#L42) | Var is a registry row; Prefix matches a family, and engine ownership forbids inheritance. |
| [`Base`](../../../internal/procenv/contracts.go#L80) | Base makes inherited input explicit; local children reuse Captured, never the daemon's ambient environment. |
| [`RunnerInfo`](../../../internal/procenv/contracts.go#L89) | RunnerInfo carries the claim's granted identity rather than the host process's ambient identity. |
| [`Runtime`](../../../internal/procenv/contracts.go#L95) | Runtime supplies sealed values and an attempt's already-built environment for tools and cleanups. |
| [`Assignment`](../../../internal/procenv/contracts.go#L106) | Assignment preserves the author's startup marker until host and SDK split job environment delivery. |
| [`JobEnv`](../../../internal/procenv/contracts.go#L112) | JobEnv separates pre-start interpreter settings from values applied by the SDK after planning. |
| [`SplitJobEnv`](../../../internal/procenv/contracts.go#L117) | SplitJobEnv validates names and rejects engine-owned or credential transport names. |
| [`Build`](../../../internal/procenv/contracts.go#L125) | Build excludes credential-class values from both local snapshots and remote inputs under the shared process policy. |
| [`Lookup`](../../../internal/procenv/contracts.go#L128) | Lookup uses exact rows before prefixes, case-insensitively on Windows; unknown names remain user-owned. |
| [`Validate`](../../../internal/procenv/contracts.go#L131) | Validate refuses invalid names and engine-owned assignments without exposing secret values in its errors. |
| [`Registry`](../../../internal/procenv/registry.go#L5) | Registry is the single classification table for environment generation and reference documentation. |

### internal/limits

| Export | Purpose |
| --- | --- |
| [`ErrNoTime`](../../../internal/limits/deadline.go#L11) | ErrNoTime means the parent's remaining budget cannot leave the requested settlement margin. |
| [`Child`](../../../internal/limits/deadline.go#L17) | Child derives a deadline, leaving margin before parent; a zero parent means unbounded. |
| [`ChildContext`](../../../internal/limits/deadline.go#L22) | ChildContext applies Child to ctx.Deadline, retaining cancellation when the result is unbounded. |
| [`Renew`](../../../internal/limits/deadline.go#L27) | Renew returns ttl/3; callers validate positive TTLs before constructing a lease keeper. |
| [`CheckRenewal`](../../../internal/limits/deadline.go#L30) | CheckRenewal refuses nonpositive values or a cadence that cannot renew twice strictly before expiry. |
| [`JobDeadline`](../../../internal/limits/deadline.go#L35) | JobDeadline adds JobSlack to attemptDeadline and caps the result at ownerDeadline. |
| [`Error`](../../../internal/limits/deadline.go#L40) | Error bounds already-masked UTF-8 error text with a head and tail within ErrorBytes. |
| [`Record`](../../../internal/limits/deadline.go#L43) | Record bounds already-masked UTF-8 summary or annotation text within AnnotationBytes with an elision note. |
| [`Failure`](../../../internal/limits/deadline.go#L46) | Failure refuses an oversized already-masked failure record rather than producing malformed structured bytes. |
| [`Class`](../../../internal/limits/table.go#L4) | Class distinguishes author refusals, operator ceilings and internal coordination budgets. |
| [`Limit`](../../../internal/limits/table.go#L14) | Limit carries a typed value (duration, int64 bytes, or int count) with its documented enforcement. |
| [`Table`](../../../internal/limits/table.go#L22) | Table returns independent rows for every shared constant; documentation renders the typed values. |

### internal/settings

| Export | Purpose |
| --- | --- |
| [`Source`](../../../internal/settings/contract.go) | preserves the origin needed to explain a refusal without leaking values. |
| [`Precedence`](../../../internal/settings/contract.go) | gives closer explicit sources priority; ceilings remain independent. |
| [`OperatorPrecedence`](../../../internal/settings/contract.go) | keeps a named flag authoritative, including explicit zero. |
| [`Origin`](../../../internal/settings/contract.go) | keeps source, file, key and line when a value moves into the sealed run spec. |
| [`Phase`](../../../internal/settings/contract.go) | prevents project and code settings from being frozen before planning. |
| [`Rule`](../../../internal/settings/contract.go) | is the precedence-document and conformance-test source for one setting. |
| [`Values`](../../../internal/settings/contract.go) | Embeds native run settings and retains raw startup inputs separately. |
| [`KeyInput`](../../../internal/settings/contract.go) | is consumed in the parent process; diagnostics may show only presence. |
| [`RetentionValues`](../../../internal/settings/contract.go) | keeps metric and run retention separate from fixed blob/log rules. |
| [`OperatorValues`](../../../internal/settings/contract.go) | uses pointers because zero disables a documented guard; it never means missing. |
| [`OperatorInputs`](../../../internal/settings/contract.go) | keeps deployment authority out of author preferences. |
| [`Context`](../../../internal/settings/contract.go) | carries facts used for defaults without reading process-global state. |
| [`Inputs`](../../../internal/settings/contract.go) | has a fixed field for each source, rather than caller-ordered layers. |
| [`Error`](../../../internal/settings/contract.go) | names invalid or conflicting input without echoing secrets. |
| [`ResolveIntake`](../../../internal/settings/intake.go) | Resolves captured request settings against trusted controller policy. |
| [`ResolveRequest`](../../../internal/settings/contract.go) | resolves only intake inputs; project and code fields remain unset and their winning request overrides remain distinguishable from defaults. |
| [`PlanInputs`](../../../internal/settings/contract.go) | permits only captured request inputs and checkout-derived declarations. |
| [`ResolvePlan`](../../../internal/settings/contract.go) | returns every job winner keyed by declared job ID after C3 validation. |
| [`ExpansionInputs`](../../../internal/settings/contract.go) | prevents generated jobs from consulting new host preferences. |
| [`ResolveExpansion`](../../../internal/settings/contract.go) | resolves generated declarations against the accepted run settings and original ceilings. |
| [`ProjectCheckoutDeclarations`](../../../internal/settings/contract.go) | Captures validated project pipeline/default layers with presence and file positions. |
| [`ValidateCheckoutDeclarations`](../../../internal/settings/contract.go) | Rejects wrong format, unsupported field sources and forged origin layers. |
| [`ProjectCeilings`](../../../internal/settings/contract.go) | preserves operator origins and adds C4's fixed retry/memo bounds. |
| [`ProjectOrigin`](../../../internal/settings/contract.go) | retains file, key and line and rejects unknown source values. |
| [`OriginLayers`](../../../internal/settings/contract.go) | is the complete C4-to-C1 mapping; operator sources remain distinguishable. |
| [`ControllerCeilings`](../../../internal/settings/contract.go) | resolves each operator key with OperatorPrecedence. |
| [`ConfigPath`](../../../internal/settings/resolvers.go) | resolves config_path with its Table rule, preserving origin for refusal. |
| [`Home`](../../../internal/settings/resolvers.go) | resolves home with its Table rule, preserving origin for refusal. |
| [`Profile`](../../../internal/settings/resolvers.go) | resolves profile with its Table rule, preserving origin for refusal. |
| [`Args`](../../../internal/settings/resolvers.go) | resolves args with its Table rule, preserving origin for refusal. |
| [`Guards`](../../../internal/settings/resolvers.go) | resolves guards with its Table rule, preserving origin for refusal. |
| [`Requires`](../../../internal/settings/resolvers.go) | resolves requires with its Table rule, preserving origin for refusal. |
| [`Prefers`](../../../internal/settings/resolvers.go) | resolves prefers with its Table rule, preserving origin for refusal. |
| [`Workers`](../../../internal/settings/resolvers.go) | resolves workers with its Table rule, preserving origin for refusal. |
| [`Output`](../../../internal/settings/resolvers.go) | resolves output with its Table rule, preserving origin for refusal. |
| [`AdmissionClass`](../../../internal/settings/resolvers.go) | resolves admission_class with its Table rule, preserving origin for refusal. |
| [`Priority`](../../../internal/settings/resolvers.go) | resolves priority with its Table rule, preserving origin for refusal. |
| [`AdmissionPolicy`](../../../internal/settings/resolvers.go) | resolves admission with its Table rule, preserving origin for refusal. |
| [`Toolchain`](../../../internal/settings/resolvers.go) | resolves toolchain with its Table rule, preserving origin for refusal. |
| [`PauseTimeout`](../../../internal/settings/resolvers.go) | resolves debug.pause_timeout with its Table rule, preserving origin for refusal. |
| [`BoxID`](../../../internal/settings/resolvers.go) | resolves machine.box_id with its Table rule, preserving origin for refusal. |
| [`BinaryCache`](../../../internal/settings/resolvers.go) | resolves cache with its Table rule, preserving origin for refusal. |
| [`SecretsKey`](../../../internal/settings/resolvers.go) | resolves secrets_key with its Table rule, preserving origin for refusal. |
| [`CatchUp`](../../../internal/settings/resolvers.go) | resolves schedule.catch_up with its Table rule, preserving origin for refusal. |
| [`RunnerPreferences`](../../../internal/settings/resolvers.go) | resolves machine runner configuration independently of run selection. |
| [`OwnerExecution`](../../../internal/settings/resolvers.go) | resolves owner_execution with its Table rule, preserving origin for refusal. |
| [`JobTimeout`](../../../internal/settings/resolvers.go) | resolves job.timeout with its Table rule, preserving origin for refusal. |
| [`ApprovalWait`](../../../internal/settings/resolvers.go) | resolves approval.wait with its Table rule, preserving origin for refusal. |
| [`Retry`](../../../internal/settings/resolvers.go) | resolves job.retry with its Table rule, preserving origin for refusal. |
| [`MemoTTL`](../../../internal/settings/resolvers.go) | resolves job.memo_ttl with its Table rule, preserving origin for refusal. |
| [`ClaimWait`](../../../internal/settings/resolvers.go) | resolves claim_wait with its Table rule, preserving origin for refusal. |
| [`Retention`](../../../internal/settings/resolvers.go) | resolves retention with its Table rule, preserving origin for refusal. |
| [`IdlePoll`](../../../internal/settings/resolvers.go) | resolves idle_poll with its Table rule, preserving origin for refusal. |
| [`ControllerConnection`](../../../internal/settings/resolvers.go) | resolves connection with its Table rule, preserving origin for refusal. |
| [`RepoLocation`](../../../internal/settings/resolvers.go) | resolves repo_location with its Table rule, preserving origin for refusal. |
| [`PauseTargets`](../../../internal/settings/resolvers.go) | resolves debug.pause with its Table rule, preserving origin for refusal. |
| [`Debug`](../../../internal/settings/resolvers.go) | resolves debug with its Table rule, preserving origin for refusal. |
| [`Publish`](../../../internal/settings/resolvers.go) | resolves publish with its Table rule, preserving origin for refusal. |
| [`CacheFrom`](../../../internal/settings/resolvers.go) | resolves cache_from with its Table rule, preserving origin for refusal. |
| [`ResourceLimits`](../../../internal/settings/resolvers.go) | resolves resources with its Table rule, preserving origin for refusal. |
| [`Checkout`](../../../internal/settings/resolvers.go) | resolves the C3 checkout policy without changing acquisition identity or SHA. |
| [`TokenTTL`](../../../internal/settings/resolvers.go) | resolves token.ttl with its Table rule, preserving origin for refusal. |
| [`SubmitEnvAllow`](../../../internal/settings/resolvers.go) | resolves run.submit_env_allow with its Table rule, preserving origin for refusal. |
| [`RunnerSelection`](../../../internal/settings/resolvers.go) | resolves the route requested for this run, including here. |
| [`DryRun`](../../../internal/settings/resolvers.go) | resolves explicit false independently of an absent request flag. |
| [`NoMemo`](../../../internal/settings/resolvers.go) | resolves result-reuse policy without changing dependency cache policy. |
| [`AllowRisk`](../../../internal/settings/resolvers.go) | preserves an explicit empty risk list at intake. |
| [`NoProgressTimeout`](../../../internal/settings/resolvers.go) | resolves a job's idle bound independently of execution timeout. |
| [`RetryBackoff`](../../../internal/settings/resolvers.go) | resolves retry spacing without changing the retry count. |
| [`Table`](../../../internal/settings/table.go) | is complete for surviving settings from the source inventory and the approved additions. |

The restricted declaration types live in `internal/runspec` to avoid a settings
import cycle. `CheckoutDeclarations` carries the version, file and two layers.
`CheckoutLayer` holds only args, guards, requirements, preferences and pipeline profile with presence and origins. `GuardDeclarations`
holds unevaluated predicates. `CheckoutDeclarationsVersion` names format 1.

### internal/clisurface

| Export | Purpose |
| --- | --- |
| [`Authority`](../../../internal/clisurface/grammar.go) | Authority separates team credentials from deployment and in-pod powers. |
| [`Flag`](../../../internal/clisurface/grammar.go) | Flag is parser and help metadata; Argument empty means a boolean switch. |
| [`Command`](../../../internal/clisurface/grammar.go) | Command separates parser metadata from handlers; Flags adds to GlobalFlags. |
| [`GlobalFlags`](../../../internal/clisurface/grammar.go) | GlobalFlags is inherited everywhere; profile selection is command-local, but an accepted --profile may appear before the verb. |
| [`ExitCode`](../../../internal/clisurface/grammar.go) | ExitCode keeps usage failures distinguishable from incomplete observations. |
| [`ErrorFormat`](../../../internal/clisurface/grammar.go) | ErrorFormat delegates JSON encoding and codes to internal/apierr; the CLI must not create another error envelope or leak pipeline secrets in hints. |
| [`Errors`](../../../internal/clisurface/grammar.go) | Errors keeps diagnostics on stderr even when stdout contains records. |
| [`CommonRules`](../../../internal/clisurface/grammar.go) | CommonRules fixes shared meanings so a new command cannot reinterpret a flag. |
| [`Validate`](../../../internal/clisurface/grammar.go) | Validate catches grammar drift before a parser or help generator consumes it. |
| [`Registry`](../../../internal/clisurface/registry.go#L4) | the final tree is independent of runtime dispatch so help and parsers share one contract. |
| [`PipelineIdentity`](../../../internal/clisurface/pipeline.go) | Captures the invoked name separately from the registered SDK entrypoint. |
| [`ResolvePipeline`](../../../internal/clisurface/pipeline.go) | Resolves the configured pair without process globals or later configuration reads. |

### pkg/projectconfig

| Export | Purpose |
| --- | --- |
| [`Config`](../../../pkg/projectconfig/contract.go#L5) | Config excludes connection credentials because a checkout cannot choose where a user's secrets are sent. |
| [`Defaults`](../../../pkg/projectconfig/contract.go#L12) | Defaults applies even when a pipeline has no entry in the project file. |
| [`Pipeline`](../../../pkg/projectconfig/contract.go#L20) | Pipeline keeps author declarations separate from the host's sealed run spec. |
| [`Guards`](../../../pkg/projectconfig/contract.go#L36) | Guards replaces the complete lower-priority block, including when empty. |
| [`Events`](../../../pkg/projectconfig/contract.go#L42) | Events contains only events with an intake implementation. |
| [`PushEvent`](../../../pkg/projectconfig/contract.go#L52) | PushEvent filters an authenticated push; it cannot establish repository trust. |
| [`PullRequestEvent`](../../../pkg/projectconfig/contract.go#L58) | PullRequestEvent filters intake without changing the fork-PR refusal. |
| [`Schedule`](../../../pkg/projectconfig/contract.go#L65) | Schedule identifies a durable schedule separately from any run it starts. |
| [`Hook`](../../../pkg/projectconfig/contract.go#L76) | Hook requires an empty mapping so misspelled hook settings cannot be ignored. |
| [`Spark`](../../../pkg/projectconfig/contract.go#L79) | Spark pins a library independently of the controller connection. |
| [`Parse`](../../../pkg/projectconfig/projectconfig.go#L22) | Parse validates checkout declarations and preserves explicit empty values. |
| [`Load`](../../../pkg/projectconfig/projectconfig.go#L35) | Load returns nil when the project file is absent. |
| [`Discover`](../../../pkg/projectconfig/projectconfig.go#L51) | Discover searches upward for checkout declarations without reading machine settings. |
| [`Validate`](../../../pkg/projectconfig/validate.go#L20) | Validate validates typed checkout declarations for seal-point consumers. |
| [`Config.Registry`](../../../pkg/projectconfig/registry.go) | Projects validated declarations into dispatch metadata with explicit empty guards and requirements preserved. |

### internal/userconfig

| Export | Purpose |
| --- | --- |
| [`Config`](../../../internal/userconfig/contract.go#L6) | Config owns the complete machine file so every reader rejects unknown keys. |
| [`Profile`](../../../internal/userconfig/contract.go#L18) | Profile is only a named controller connection; storage uses that controller. |
| [`Connection`](../../../internal/userconfig/contract.go#L24) | Connection keeps the user's credential in an owner-only file, never env. |
| [`ReposConfig`](../../../internal/userconfig/contract.go#L30) | ReposConfig is the only fallback source for resolving a local child checkout. |
| [`RepoPath`](../../../internal/userconfig/contract.go#L38) | RepoPath cannot grant controller authority merely by naming a checkout. |
| [`AdmissionConfig`](../../../internal/userconfig/contract.go#L43) | AdmissionConfig preserves machine admission independently of placement. |
| [`AdmissionCustom`](../../../internal/userconfig/contract.go#L51) | AdmissionCustom records explicit zero separately from the policy defaults. |
| [`AdmissionBurst`](../../../internal/userconfig/contract.go#L59) | AdmissionBurst retains the existing admission policy's measured burst bound. |
| [`AdmissionJev`](../../../internal/userconfig/contract.go#L66) | AdmissionJev names the existing admission predictor and its failure bounds. |
| [`RunnerConfig`](../../../internal/userconfig/contract.go#L77) | RunnerConfig supplies machine claim preferences; pools and billing facts come from its root capability. |
| [`CacheConfig`](../../../internal/userconfig/contract.go#L95) | CacheConfig bounds only the local binary cache; explicit zero disables a bound. |
| [`MachineConfig`](../../../internal/userconfig/contract.go#L101) | MachineConfig preserves an explicit box identity when the hostname changes. |
| [`DebugConfig`](../../../internal/userconfig/contract.go#L106) | DebugConfig bounds pauses without putting debug controls into child env. |
| [`RunConfig`](../../../internal/userconfig/contract.go#L112) | RunConfig grants no authority: allowed env still passes procenv filtering, and cache reads still require the user's repo-bound data.read capability. |
| [`Decode`](../../../internal/userconfig/validate.go#L15) | Decode validates the complete machine schema without filling defaults. |
| [`ReadConfig`](../../../internal/userconfig/userconfig.go#L71) | ReadConfig reads and validates an owner-only machine file. |
| [`ValidateConfig`](../../../internal/userconfig/validate.go#L28) | ValidateConfig validates typed machine settings without echoing credentials. |

### internal/wingd

| Export | Purpose |
| --- | --- |
| [`DialSocket`](../../../internal/wingd/socket_transport.go) | Connects to the platform's private controller transport under the caller's context and timeout. |
| [`ListenSocket`](../../../internal/wingd/socket_transport.go) | Binds that transport and yields only authenticated APIConn connections. |
| [`LocalActivity`](../../../internal/wingd/lifecycle.go) | LocalActivity counts live state, not historical runs or configured schedules. |
| [`LocalPeerIdentity`](../../../internal/wingd/lifecycle.go) | LocalPeerIdentity comes from the transport's kernel credentials, never request input. |
| [`LocalSubject`](../../../internal/wingd/lifecycle.go) | LocalSubject checks the peer against the controller's OS account before minting local authority. |
| [`LocalStartOptions`](../../../internal/wingd/lifecycle.go) | LocalStartOptions carries spawn plumbing in arguments rather than environment variables. |
| [`LocalRuntime`](../../../internal/wingd/lifecycle.go) | LocalRuntime uses the same pkg/controller implementation, route policy and state owner. |
| [`LocalStatus`](../../../internal/wingd/lifecycle.go) | LocalStatus exposes version skew and activity without opening a second writing store handle. |
| [`ManagedUnitKind`](../../../internal/wingd/lifecycle.go) | ManagedUnitKind gives schedules and the controller one upgrade path. |
| [`ManagedUnit`](../../../internal/wingd/lifecycle.go) | ManagedUnit records rendered argv for doctor to detect removed verbs as well as stale versions. |
| [`ManagedUnitManager`](../../../internal/wingd/lifecycle.go) | ManagedUnitManager is shared by update, dashboard installation and schedule installation. |
| [`LocalDoctorFinding`](../../../internal/wingd/lifecycle.go) | LocalDoctorFinding makes incompatible stores, version skew and removed unit verbs actionable. |
| [`LocalLifecycle`](../../../internal/wingd/lifecycle.go) | LocalLifecycle starts on demand and shares the runtime used by explicit dashboard commands. |
| [`NewLocalLifecycle`](../../../internal/wingd/lifecycle.go) | NewLocalLifecycle keeps service management outside controller request handling. |

### internal/runners/launcher

| Export | Purpose |
| --- | --- |
| [`CloudUnitKind`](../../../internal/runners/launcher/job_types.go) | CloudUnitKind selects host work without weakening the common Cloud isolation contract. |
| [`CloudJobConfig`](../../../internal/runners/launcher/job_types.go) | CloudJobConfig accepts operator-owned images and ceilings, never pipeline-supplied pod fragments. |
| [`CloudJobBinding`](../../../internal/runners/launcher/job_types.go) | CloudJobBinding is derived from an awarded row, not the launcher's requested team or fence. |
| [`CloudJobAward`](../../../internal/runners/launcher/job_types.go) | CloudJobAward carries only a single-use bootstrap, never the launcher's root credential. |
| [`CloudNodeReservation`](../../../internal/runners/launcher/job_types.go) | CloudNodeReservation is an exclusive placement fact held until the Job and DinD have stopped. |
| [`CloudNetworkEvidence`](../../../internal/runners/launcher/job_types.go) | CloudNetworkEvidence comes from enforcement probes, not the presence of NetworkPolicy YAML. |
| [`CloudBoundary`](../../../internal/runners/launcher/job_types.go) | CloudBoundary reserves one Job per team-dedicated node and verifies enforced network isolation. |
| [`CloudJobResources`](../../../internal/runners/launcher/job_types.go) | CloudJobResources couples Secret cleanup and node ownership with the Job it authorizes. |
| [`BuildJob`](../../../internal/runners/launcher/job.go) | BuildJob is the sole builder for plan, eval and work, including run-host-dispatched work. |

## File inventory

New contract and test files:

- `internal/clisurface/grammar.go`
- `internal/clisurface/contract_test.go`
- `internal/clisurface/registry.go`
- `internal/limits/billing.go`
- `internal/limits/claim.go`
- `internal/limits/cron.go`
- `internal/limits/deadline.go`
- `internal/limits/identity.go`
- `internal/limits/node.go`
- `internal/limits/operator.go`
- `internal/limits/run.go`
- `internal/limits/storage.go`
- `internal/limits/table.go`
- `internal/limits/table_test.go`
- `internal/limits/wire.go`
- `internal/procenv/contracts.go`
- `internal/procenv/contracts_test.go`
- `internal/procenv/registry.go`
- `internal/runners/launcher/job_types.go`
- `internal/settings/contract.go`
- `internal/settings/contract_test.go`
- `internal/settings/resolvers.go`
- `internal/settings/table.go`
- `internal/userconfig/contract.go`
- `internal/wingd/lifecycle.go`
- `pkg/projectconfig/contract.go`

This document is the new contract report. Existing files changed only for colliding
`Config`, `Load`, `Parse`, `Discover` and `BuildJob` identifiers and their callers:

- `cmd/sparkwing/action_new_test.go`
- `cmd/sparkwing/hooks.go`
- `cmd/sparkwing/main.go`
- `cmd/sparkwing/profile_flag.go`
- `internal/crons/crons.go`
- `internal/doccheck/yamlcheck.go`
- `internal/orchestrator/hosted_run.go`
- `internal/orchestrator/main.go`
- `internal/orchestrator/pod_node_host.go`
- `internal/orchestrator/profile_run.go`
- `internal/orchestrator/trigger_host.go`
- `internal/runners/launcher/job.go`
- `internal/runners/launcher/job_test.go`
- `internal/runners/launcher/launcher.go`
- `pkg/controller/github_app_automation.go`
- `pkg/controller/github_app_webhook.go`
- `pkg/localws/pipelines.go`
- `pkg/projectconfig/pin_test.go`
- `pkg/projectconfig/projectconfig.go`
- `pkg/projectconfig/projectconfig_test.go`
- `pkg/projectconfig/source.go`

<!-- design:end -->


## W9 runner composition integration amendments

Invariant: a changed canary cannot authorize work, and probe cleanup cannot delete
a Pod outside the exact namespace, reservation and observed instance UID.
`FuzzProductionBoundaryRefusesChangedCanary` and
`FuzzProductionBoundaryCleanupReservationFence` exercise these boundaries.

`cluster.productionLauncher` loads the standard Kubernetes context and composes
`launcher.NewLauncher` with `launcher.NewProductionBoundary`. Claim demand uses
the linker version, plan/eval/node kinds and the operator's resource ceilings.
Launcher authority remains confined to the controller transport.

`KubernetesBoundary.StopProbes` is an optional reservation-scoped recovery callback.
Release calls it after verifying the full node/reservation/namespace fence and
before requiring container vacancy. It may delete only reservation-owned probe
Pods and control policies, with UID preconditions. This permits recovery from a
cancelled or crashed probe without treating an active workload as vacant. Clear
still runs after vacancy. The production implementation uses the same probe
cleanup for both callbacks. Workload emptyDir and container-state cleanup belongs
to Kubernetes after Pod removal. Arbitrary host changes made through privileged
DinD remain an accepted residual and require node reprovisioning.

The production probe uses a readiness-confirmed listener and a control policy
allowing ingress plus egress only to the listener. A separate witness Pod must
connect to that listener before egress-denial evidence can be accepted. Its
measurement Pod uses exact workload labels, root, privilege and host PID access
for a node-network escape canary. Both pod and node contexts must reach the
controller and refuse observed cluster API, DaemonSet TCP and kubelet endpoints.
DaemonSet UDP ports are inventoried. DNS ports remain available. The policy,
endpoint and node-reservation projections must stay unchanged through cleanup.
Every probe and Pod lifetime is bounded by the persisted award deadline.

Only the controller is a known required destination in this composition. Extra
source/blob/provider destinations, UDP enforcement, native image and CNI behavior,
and kubelet reclamation require deployment acceptance. These callback tests use
the Kubernetes API fake and do not establish native cluster security evidence.
No new flag, environment variable, policy destination configuration or workload
limit is introduced. The chart grants namespaced Pod create/get/delete and
NetworkPolicy list/create/delete, and the runner image requires nc and nsenter.

## Runner integration amendment: Cloud endpoint classification

DESIGN: endpoint classification. A required Cloud destination cannot also be
classified as forbidden. The production boundary refuses a controller endpoint
that overlaps the Kubernetes API, an inventoried non-DNS DaemonSet endpoint or
a kubelet endpoint. TCP probes cannot distinguish services sharing one address
and port, so silently preferring the required class would produce false escape
proof. The integration lead authorized this rule within the existing Cloud
boundary contract. It adds no grant, flag or configuration field.

`FuzzProductionBoundaryRefusesConflictingDestinations` varies API, DaemonSet and
kubelet collisions. Canary admission tests apply standard API readiness defaults
to a cloned response. The authored probe includes those exact scalar defaults.

## W9 native QA surface corrections (2026-10-16)

Team `tokens create --type` accepts only `member`. `runner` remains an inventory
filter and is issued only through `runners add`. Profile verification uses
public health followed by authenticated identity, rather than team run reads.
Canonical controller refusals survive the CLI envelope. Configuration field
errors map to `bad_request`, and operator-ceiling errors to `limit_exceeded`.

The controller executable accepts flags and its explicit `admin` command.
`help`, `--help` and `-h` print usage without opening state or credentials.
Unexpected positional arguments refuse with usage before startup.

`SPARKWING_HOME` isolates runtime state but does not relocate machine config.
An isolated `profiles add` also supplies `SPARKWING_CONFIG` to the write guard.

## Integrated working-tree trigger composition

`pipeline trigger --working-tree` captures the working tree with the existing
secret-file checks, materializes its immutable private checkout, resolves the
pipeline alias and entrypoint from the captured configuration, and describes
that checkout with the selected engine. It leaves the user's HEAD and index
untouched. `--allow-secret-file` applies to this capture. After validating
arguments, the production client commits the bundle through shared source
storage and seals its digest into the trigger request. A source pinned to a
different revision keeps that pipeline revision while the execution snapshot
remains immutable. Snapshot scratch closes on success and failure.

## Launch burst configuration

Machine `run.routing` owns optional route, profile, ordered prefer entries,
overflow_minutes, local_secrets source selection and source_files approval.
Repository `defaults.run` and pipeline `run` use RunRestrictions, carrying only
route:local and local_secrets name declarations. Repository restrictions cannot
widen machine authorization. Repository secret names remain declarations and
cannot change machine source selection or secret classification. Untracked
repository source_files, including ignored
paths, require exact machine run.routing.source_files approval. Source-file
validation remains shared by capture. Route absence is local. Cloud requires a
machine-configured profile. Prefer absence retains here/5s, machines/5s, cloud/0s.
Empty preference lists, negative holds, invalid selectors and negative/overflowing
minutes fail validation.

Local preparation, including offline fallback, resolves configured local secret
names against the selected pipeline's complete declarations and the requesting
process's captured environment. It excludes those environment entries before
capturing the private local environment snapshot or starting SDK inspection.
Only the input-only `CreateRunRequest.LocalSecrets` map carries values to the
local controller's existing volatile registry. The request seals resolved names.
Local preparation and intake diagnostics mask the resolved values. Execution
uses the existing masked secret-release path.

`Pipeline.SourceFiles` (`source_files`) names literal repo-relative files to
include beyond tracked paths. Shared ValidateSourceFiles rejects pathspec,
traversal, Git metadata, directories and missing declared source at capture.
The captured snapshot preserves tracked working-tree edits and exclusions.

Cloud routing remains attached and captures the working tree. The CLI refuses
committed revision flags with pipeline-trigger guidance before connecting, and
preserves `--sw-run-handle-file` after retaining accepted here authority.
Foreground local routing retains its existing lifecycle. Burst source metadata
reads the invoking and default branches from the original checkout, because an
inspection clone's detached HEAD and origin HEAD are not those source facts.

Pre-intake connection setup failures use the same local fallback as the identity
probe and source upload. Unavailable and network failures announce local
execution. Authorization failures and cancellation remain failures. A failed
connection owns no transport, so fallback does not call its close function.

Generated command references retain level-three description headings. The run
reference exposes a real `burst-and-overflow` section anchor. Level-one and
level-two description headings stay escaped so command sections retain their
ownership of those levels.

Shared `source.PrepareCheckout` prepares its private host workspace after live
source authorization and before creating the owned checkout. Every remote
placement uses this path, including a fresh registered machine. CLI source
options no longer create that directory independently.
For Cloud-owned here runs, `hostSourceOptions` supplies the requesting machine's
ambient environment through `procenv.Build`. Remote policy admits the existing
minimal tool base plus `GOCACHE` and `GOMODCACHE`. Captured requester values,
arbitrary user variables and credential-shaped names remain excluded. SDK
installation and process execution use this same classified base. Local runs
retain their captured environment policy.

`runner.holder_prefix` saved by enrollment is registration display metadata.
Starting that saved configuration does not select a claim holder or execution
identity. Explicit `warm --holder-prefix` remains refused because the controller
owns holder identity. Awarded runner names, labels and fences remain authoritative.
The enrollment-to-startup regression uses real session enrollment and credential
files. The runner-loop property varies saved names while preserving the awarded
identity and claim selection.

## Cloud chart integration amendment

The full chart exposes controller-owned S3 bytes as `controller.dataStore.s3URL`,
passed through the existing `--cache-blob-store` flag. Multi-team accounting uses
`controller.bucketCeiling.store` for the same bucket. CloudFront uses the existing
domain/key-pair values and file credential. RDS uses `databaseSecret` projected
as `pg-url`. Bootstrap and encryption keys retain their fixed file projections.
`credentialsSecret` preserves each file-named key and has an SSM ExternalSecret
example. No secret value enters Helm values, argv or environment.

The runner bundle owns an optional launcher Deployment (`launcher.enabled`),
with separate `launcher.tokenSecret` and Kubernetes ServiceAccount/RBAC.
`runner.enabled=false` removes the pool and its Go-cache PVC. Every enabled
image requires repository and tag together. Job runner and Docker coordinates
also require a SHA-256 digest. AppVersion never selects a runtime image.
The launcher targets the release namespace, alongside its customer ServiceAccount,
admission exemption guard and default-deny Job egress policy. Allowed egress is
operator-supplied through `launcher.networkPolicy.egress`.

BuildJob retains the exact TeamNodeValue label and adds a NoSchedule CPU-band
Exists toleration, so the existing small-band nodes remain usable. Every Job Pod
and production boundary probe carries `karpenter.sh/do-not-disrupt=true`.
Admission validation preserves the Job template annotations as well as placement.
NodePool provisioning stays an infrastructure prerequisite: each Ready node must
carry its exact team's hashed label, and one reservation occupies the whole node.
The launcher does not induce Karpenter scale-up. Native sidecars,
PodLevelResources, admission APIs and escaped-node network enforcement must be
verified on the target cluster. A Helm render establishes no native proof.

## Burst artifact execution and physical Cloud resources

Burst execution uses the shared native artifact adapter for declaration capture
and consumer staging. Public here and personal-runner probes now publish a file
from the uploaded edited source and read it in a dependent job. Native listener
refusal leaves those probes unverified.

Cloud resource requests keep their sealed presence and origins. The physical
allocation comes from the billing class committed at award, independently of
those settings, and remains fixed across bootstrap exchange and price changes.
No default route, per-user cap, configuration key or execution limit changes.

## Burst machine admission continuation

Registered runner capacity observes the existing machine sampler, including
container limits, or the local admission queue when `local_admission` is enabled.
`contribution` caps advertised headroom and `keep_free` subtracts the reserved
share. The physical capacity and free resources remain distinct, including an
explicit zero offer. Observation errors fail before a claim instead of reporting
unknown capacity. Claim messages include OS and architecture.

When enrollment-enabled local admission has no daemon, the runner resolves its
own release's signed Sparkwing CLI through the existing engine resolver, then
calls the existing detached lifecycle and waits for readiness. An unreachable
existing owner is an error and never causes another owner to be started.
Development builds have no published matching release. Their native fixture
starts an independent local controller with the development CLI first.

`NativeHostConfig.ClaimAdmission` composes the existing `LocalAdmission` into
independently claimed jobs. `LocalAdmission.ClaimedNode` holds one job-specific
machine lease before source preparation and through process cleanup. It uses
the sealed job resources, without creating another run record. Ordinary claimed
jobs also acquire their local concurrency. Burst jobs leave logical concurrency
with the requesting owner, including when a registered runner shares its
machine. Here burst owners retain semaphores through persisted completion and
acquire compute before requesting a self-claim. While that admission waits,
the job stays ready for other ranked claimants. Another claimant's progress
cancels and joins the machine wait. The store still fences execution after the
machine lease is acquired.

Machine admission for a Cloud-owned run uses `wingwire.RemoteRunID` with the
controller address and run ID encoded as separate base64url fields. Job and
semaphore participants preserve that owner namespace. The local controller
does not query or finalize those identities in its run store. Resource release,
eviction and local queue cancellation still operate on the machine lease. The
Cloud host owns its award and run cancellation. Local run lookup failures remain
errors. No SQL schema, controller chaining, persisted credential, environment
variable, public flag or new limit is introduced.

The public personal-runner probe now uses a separate HOME, runtime home, cache
and local controller from its requester. It still uses one development binary
and a local SDK replacement, so it does not verify signed release distribution.

## Attached local output

The local run binding requires a log follower whenever hosting is attached.
It uses the public controller log reader and selected output mode while the
CLI hosts execution. Host and follower are joined before return. This preserves
masked terminal output during offline Cloud fallback. No new flag or output
format is added.

## Local profile data construction and publication

The CLI resolves --sw-cache-from against the checkout's canonical remote
repository and origin default branch before sealing. It opens a temporary
verified-default reader through the selected profile and revokes it on host
cleanup. Background hosts keep the sealed repository identity and read the
default-branch hint from the authorized local checkout, because intake clears
requester-supplied default-branch metadata. The profile server still requires verified
default-branch records. Both paths ignore inherited Git repository overrides.
Refreshing a job's attempt storage preserves the read-only cache only for the
sealed local opt-in. Background owner and independently claimed job awards
construct the same profile cache before execution; queue claim order does not
select cache availability.

--publish runs after local completion. The profile admits publish-mode history,
imports terminal local attempts, copies selected output/artifact bytes through
signed storage and streams retained logs through the shared log uploader. Local
history is exported through verified peer authentication. Publication failure
warns without changing the local verdict. The background local host publishes
on cleanup. Historical generated topology is flattened to explicit dependencies;
closure generation does not run during import. The uploaded request and resolved
spec exclude the private local environment snapshot, including names and digest.
Burst intake uses an empty environment snapshot rather than the shell name list.

The background queue reports host errors to the local controller's logger when
hosting returns. Error reporting does not wait for controller shutdown.

## Burst review integration rulings

Burst claim eligibility and requester-secret release use the same sealed claimant
set. Eligible executors are the run's here host, its requester's enrolled personal
machines and Cloud executors when cloud is listed. Name/pool entries only narrow
that eligible set. The here owner retains planning authority independently of
job placement. Authenticated origin and ownership decide eligibility. Poll labels
cannot grant it. Revoked or expired personal registrations do not hold placement.

Burst scanning inspects changed/uncommitted snapshot blobs against HEAD. It skips
values shorter than eight bytes and PEM armor patterns for this scan only.
Masking retains all its patterns. Local publication reuses the private controller's
45-second request deadline. The second interrupt cancels publication and reports
local-only history without changing the local execution verdict.

## Verified App event snapshot integration

C4 `AppFacts` adds `Event` and `Action`. They capture the provider event selected
by the verified webhook adapter. `PullRequest.Merged` is an optional boolean that
preserves the distinction between an open PR and a closed, unmerged PR.
`PullRequest.Label` preserves the label that selected a labeled event. These
facts are sealed with the repository, ref, SHA and default branch. Re-requests
retain the original event context and use the new signed delivery for replay.

The request's Git ref is the full provider ref, including `refs/heads/`,
`refs/tags/` or `refs/pull/`. DefaultBranch remains the provider's branch name.
Public trigger environment columns and invocation-map default-branch fields are
replaced by typed sealed facts. No arbitrary environment values are added to the
request. Runtime environment snapshots retain their existing names/digest rule.

Automation discovery reads the current default branch's declarations but cannot
replace the default branch reported by the signed event. A later default-branch
change does not remove the original push's default-branch trust.

## Invocation and detached trace integration

Local binary preparation retains `--sw-no-bincache` and
`--sw-hash-all-files` in `runspec.Source.NoBincache` and `HashAllFiles`.
Initial inspection and every retained-source acquisition use the same policy,
including detached hosting and retries. The source adapter passes both fields
to the verified selected engine's `SDKInstallRequest`. Uncached Go builds keep
debug information and publish no binary-cache entry. Hash-all is scoped to one
materialization and includes ignored module and local replacement inputs.

Local relative priority resolves against the controller's machine admission
queue immediately before intake. `front` ranks above the current highest waiter
and `back` below the lowest. An empty queue gives +1 or -1. The resulting integer
keeps flag provenance in the request and survives sealing and retry. A rank
outside the integer range fails before submission.

All Sparkwing handlers consume the original parsed `Invocation`. Execution
does not reconstruct arguments or declare another flag parser. Engine handoff
replays the original arguments from the invoking directory, so a relative
`--checkout` is applied once by the selected engine. Pipeline arguments retain
their exact spelling and separators.

Repository add, remove and info require the global `--checkout`. Mutations
select that absolute checkout, with no positional or basename fallback. Their
receipts honor the selected output. Version holds, unreadable-state recovery
and sparks lint/update also emit JSON when piped. Required-flag diagnostics
retain the registry's explanation when present.

Hook install, uninstall and status, and pipeline scaffolding consume the parsed
`clisurface.Invocation`. Hook `--all-repos` selects the registered repositories.
The removed `--fleet` and hooks `--all` spellings are refused. Hook mutations
report their resulting status in the selected output format. Their progress and
warnings go to stderr. Pipeline scaffolding emits a JSON pipeline/path receipt,
a plain pipeline name, or pretty guidance after dependency resolution succeeds.

`--sw-request-id` is retained for detached runs. It becomes optional
`runspec.Request.RequestID` and survives intake, sealing and retry. The detached
JSON receipt includes `request_id` when supplied. It is trace metadata. Only
`--sw-idempotency-key` deduplicates submissions.

## Child lineage reporting, 2026-10-17

`Lineage.RepoInherited` uses optional JSON `repo_inherited`. True records that the
child caller omitted a repository override. C1 checks the parent repository and
seals this intent before reporting it. False is omitted, preserving canonical
encodings for requests without inherited repository intent. Source authorization
continues through the current intake policy.

## Pipeline and source selection integration

SDK describe selection, CLI schema binding, initial planning and attempt replans
select the configured SDK entrypoint. Scaffolds register that entrypoint.
The invoked alias belongs in run context
and presentation. `nodeproto.DescribeDoc.Pipeline` owns metadata selection.

The dispatcher, local execution and help resolve `--sw-ref`, `--sw-pipeline-ref` and a configured
source pin through one selection boundary before inspection or building. The
execution checkout supplies project configuration. Its exact SHA is sealed in
`Git.SHA`. Local source selection seals the pipeline code SHA in `Source.Ref`.
A source ref that names the execution ref projects to the sealed `Git.SHA`
without looking up that ref again. This also preserves scheduled intake. `Source.WorkingTree` retains
local code from the working tree when no committed source was selected.
`Source.CommittedCheckout` records explicit committed execution, including when
that revision equals HEAD. Acquisition restores the execution and code checkouts
separately. SDK installation uses the code checkout, while author code runs in
the execution checkout. Retry carries these selections unchanged.

Environment provenance is sealed independently of invocation origin. Children
inherit both the parent's snapshot identity and its storage source. Local
schedule registration marks captured provenance before saving its request.

The documentation checker parses current migration destination commands in
Before/After tables and fenced After examples. Before examples remain historical.
