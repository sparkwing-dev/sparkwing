# Local execution

`sparkwing run` runs locally by default. Explicit routing can select a Cloud-owned run hosted on this machine. `pipeline trigger`
submits it to a controller. Both placements use the shared run and job hosts,
sealed inputs and matching SDK engine.

Intake and hosts derive plan placement from the sealed request. Local runs,
local triggered runs, runner-hosted runs, launcher runs, `--runner here` and
GitHub Actions use the same capability table for initial plans and expansions.

If the pipeline's `go.mod` replaces the Go SDK, Sparkwing uses the installed CLI's
engine and warns that engine pinning is off for that run. The shared installer
builds the replacement locally.

Local Go runs reuse their executable while source and captured build inputs match.
Reusing the binary skips dependency preparation and compilation. On a miss,
Sparkwing resolves imported packages with `go list -deps`, then hashes the prepared
checksums and builds the executable.
Preparation uses the build's selected toolchain, workspace and sparks overlay.

The commands bind sealed intake to the shared controller, source, private
environment, storage and host services. The CLI sends state operations over the
authenticated private API. Owner evaluation delegates a fresh SDK process from
the live owner lease. It keeps the evaluated job separate from that lease and
creates no work attempt. Each service call rechecks that job's persisted source
fence and declared output edges. A separately awarded eval process uses its own
eval lease.

The controller claims each executable local job through the shared claim matcher.
Each job process receives a fresh credential from its own store-issued node award.
The private API preserves the owner and node claim identities so the host can
check their relation before starting the process.

Native execution evidence remains with the integration suite. See [rewrite integration](architecture.md).

The old `pkg/localws` server, trigger-consumer election and `RunHosted` entrypoint
are removed with their state, admission, logging and child-dispatch backends.
They cannot serve as a fallback for missing native controller or host bindings.

## Execution commands

```bash
sparkwing run build-deploy
sparkwing run deploy -- --environment staging
sparkwing pipeline trigger build-deploy --profile prod
```

Engine flags precede `--`. Pipeline and job arguments follow it. Local `run`
uses this machine's controller unless Cloud routing is configured. It accepts no connection profile flag. A profile
selects the controller for `pipeline trigger`, run observations and management.

| Invocation | Execution and records |
| --- | --- |
| `sparkwing run build` | The CLI hosts the run. The local controller owns its records. |
| `sparkwing run build --sw-detached` | The local controller queues and hosts work independently of the submitting terminal. |
| `sparkwing pipeline trigger build --profile prod` | The selected controller queues the run for an eligible runner or launcher. |
| `sparkwing pipeline trigger build --profile prod --runner here --reason repair` | This CLI hosts the controller's named here run. |

An explicit empty `--sw-cache-from=` disables `run.cache_from` from the machine
file. The selected cache profile supplies a temporary repository reader for
verified default-branch records. The credential follows the profile's live
membership and revocation and is revoked when the host closes.

`--publish P` uploads completed local job results, artifacts and retained logs
to profile P. Publication creates history with local provenance and no execution
claim. A publication error prints a warning and preserves the local verdict.
Background local runs publish when their host closes. Published history excludes
the private shell environment snapshot, including its variable names and digest.

Local schedule registration, pins, timer installation and `__tick` use the
controller and lifecycle services. Timers carry their absolute state home into
resolution. Scheduled execution still needs the scheduler's sealed-intake binding.

## Local execution

The local controller owns one home, its SQLite store, admission, queue and
browser dashboard. It starts on demand. The CLI hosts a foreground run and
sends state operations through the controller API. SDK processes declare and
execute work through protocol v2. They do not open the controller's database.

Startup reconciles orphaned work and starts the private API before consumers.
Readiness does not wait for pipeline compilation or source preparation.
The CLI starts or waits for the controller immediately before submitting a run,
after source preparation. Other local API commands, doctor and schedule timers
use the same startup operation. A startup failure reports why the local
controller could not start before any request is submitted.

The controller is the only writer of the shared store. Reads, cancellation,
retry and doctor use its API too. The controller's route policy and wire
contract also apply to `api.sock`. A transport retries only operations the
registered route marks safe to replay. A lost one-time award is never replayed
automatically.

Run listing uses the same public details as single-run status. Filters apply
before the page limit. Continuation cursors retain the filters and the initial
run-number boundary, so runs created between pages do not displace older runs.
Each page checks current authorization and reads signed engine policy notices.

Run and job log reads return tagged JSON pages to the CLI. Each output record
names its persisted attempt or owner unit. Resume cursors retain the selection
and progress for each subject. An idle follow poll returns no repeated output.
Completion requires terminal state and final persisted segments. A cursor never
replaces the store's authorization check for those segments.

Cross-run log search uses the same persisted subjects and authorization. Run
filters apply before the run limit. A page can contain no matches and still
carry a continuation cursor. The cursor keeps the initial run-number boundary
and any unfinished subject, so later runs cannot displace older candidates.
`--max-matches` limits each subject. Zero returns all matches across pages.

### Who hosts the daemon

The local controller is wingd. These commands inspect or hold its lifecycle:

```bash
sparkwing daemon status -o json
sparkwing dashboard start --stay
sparkwing dashboard status
sparkwing doctor -o json
```

Daemon status, stop and restart use the shared local lifecycle. JSON reports
`state`, `version`, `pid`, `stay`, `installed` and the four activity counts.
Stop and restart also report `stopped`, `restarted` and `previous_version`.
Plain output is the resulting state. Store and API diagnostics come from doctor.
Doctor also sweeps stray step sessions through the controller. A successful
cleanup whose run was pruned reports an unrecorded-outcome warning and succeeds.

Restart preserves a stopped controller. An answering controller on the exact
installed version stays running unless `--force` is selected. Replacement waits
for shutdown before starting the installed executable with the same home,
dashboard address and stay policy. An incomplete drain or unready replacement
returns an error without a success receipt.

Active runs, queued triggers, held leases and open dashboard connections keep
it alive. Health checks alone do not. `--stay` keeps it up until stopped.
Linux and macOS support an installed login service through `dashboard install`.
Windows supports stay mode. Machine updates refresh managed controller and
schedule units together. See [local dashboard](native-mode.md).

Installation drains and joins the existing owner before activating the managed
service, then waits for the successor to acquire the same home and become ready.

### Running with no daemon available

`sparkwing run <pipeline> --sw-allow-unadmitted` runs the same controller runtime
in process for standalone CI. Give it a private `SPARKWING_HOME`. Its admission
queue belongs to that process; it does not join another controller's machine
admission queue. It retains run and job concurrency rules. It cannot detach or
use Cloud routing. The CLI drains and joins its controller before returning.
It must not open a home another controller owns. An unavailable or incompatible
shared controller is an error, rather than permission to open its database in a
second process. Startup reconciliation and shutdown draining belong to the
runtime in both modes.

A local run has no default execution deadline. Queue wait, job execution,
approval wait and owner liveness have separate meanings. See [limits](limits.md).

## Detaching a run so it outlives the terminal

```bash
sparkwing run nightly-report --sw-detached --sw-output json
sparkwing runs status --run RUN_ID
sparkwing runs logs --run RUN_ID --follow
sparkwing runs cancel --run RUN_ID
```

Detachment queues the sealed request with the local controller. The controller
owns consumption and process cleanup. A separate user-managed consumer is not
part of the model. A successful submission receipt identifies the allocated run.
It does not prove that execution has started.

`--sw-idempotency-key` gives repeated detached submissions one durable identity.
`--sw-request-id` is a tracing field retained through intake and retry. The
detached JSON receipt includes it as `request_id`. It does not deduplicate work. JSON receipts
exclude raw arguments, captured environment values, private bearers and seal
hashes. A log path is reported only when it exists.

### Making a retry safe

```bash
sparkwing run deploy --sw-detached --sw-idempotency-key deploy-2026-10-15
sparkwing runs retry --run RUN_ID --failed
sparkwing runs retry --run RUN_ID --all-jobs
```

Submission idempotency belongs to intake's transaction. The same key and body
return the original receipt. A conflicting body fails. An ambiguous submission
does not authorize the caller to invent another run's authority.

A retry keeps the original request, source, captured environment and accepted
plan. It records the retrier separately. `--failed` reuses authorized successful
work. `--all-jobs` executes the complete plan. Application retries within a job
keep the same claim and increase the attempt ordinal. Lease loss follows the
separate recovery rule in [scheduling](scheduling.md).

### Publishing a foreground run handle

```bash
sparkwing run build --sw-run-handle-file /tmp/build-run.json
```

The run handle identifies the run for observations and cancellation. It carries
no credential. `--sw-output` chooses pretty, JSON or plain output for foreground
and detached commands. List commands emit one JSON record per line.

## Which checkout runs

Intake seals source identity and the exact commit before execution. Acquisition
uses that identity, rather than looking up a branch head again. Repository
identity identifies the namespace. It does not grant permission to clone it.

`--sw-ref` selects the local execution revision. `--sw-pipeline-ref` selects the
pipeline's source revision. A declared pipeline source and an explicit pipeline
ref must agree. Resume and retry use the persisted selection. They do not replace
it with the caller's working tree or current machine settings.

The pinned Go or TypeScript SDK selects its matching verified engine before SDK
installation, describe, planning or execution. Each machine fetches that release
once from GitHub Releases and verifies it. Pipeline binaries are built and
cached on the executing machine. Local execution does not fetch prebuilt
pipeline binaries from a controller. See [repository upgrades](cli-upgrade.md).

Before a local run is accepted, foreground and queued hosts select Go or
TypeScript from the sealed checkout's pipeline directory. A malformed manifest
fails setup. Accepted runs retain the SDK identity in their sealed engine.

SDK describe, plan, eval and attempt processes run from the acquired checkout
root. If describe fails before secret declarations are known, the diagnostic
names the error code and withholds the details because they may contain secrets.

## Remote execution

```bash
sparkwing pipeline trigger deploy --profile prod --detach -- --environment staging
sparkwing runs status --run RUN_ID --profile prod
sparkwing runs logs --run RUN_ID --profile prod --follow
```

The command prepares one sealed request and submits it to `POST /api/v1/runs`.
Detached output includes the stable run ID, team number and replay flag. Preparation
failures submit nothing. The old trigger request and separate here-host loop are
removed.

`--ref` selects the execution commit. `--pipeline-ref` selects the pipeline
commit, subject to any configured source pin. That pipeline commit's
`package.json` determines Go versus TypeScript before source acquisition and
engine selection. A different SDK in the working tree or execution commit
cannot change the selection. A malformed selected manifest fails preparation.

GitHub numeric-ID lookup, private source access and captured environment delivery
still need their native bindings. Initial here intake refuses before submission
until its reason can survive the sealed request and replay.

The selected controller owns the run's records. Its intake derives the caller's
authority. A runner claims trigger and job units through one ordered queue. The
launcher creates Jobs for awarded plan, eval and work units. Both use the shared
matcher and hosts. Inline jobs stay with the run host.

The working-tree option submits an immutable source snapshot. Secret-shaped
files require explicit review through the allowed-file option. Keep credentials
in ignored files or outside the repository and inspect `git status` before
submission. Signed source-bundle upload still needs its controller and storage
bindings, so working-tree submission fails before intake. The option's presence in help is not
native integration evidence.

Here execution retains its one-time authority in an owner-only controller/run
record before hosting. Resume takes only the run selector and connection:

```bash
sparkwing pipeline trigger deploy --profile prod --runner here --reason repair
sparkwing pipeline trigger --resume RUN_ID --profile prod
```

The named claim supplies the original request and accepted plan. The first
interrupt asks the controller to cancel while the host keeps renewing and
draining. A second interrupt stops hosting and observation, then joins both.
Incomplete hosting or observation exits 3. See [here execution](hotfix-runner.md).

Hosting closes returned session resources before exiting, including when a
binding fails after allocating them. A cleanup failure remains an incomplete
handoff and preserves the binding or execution error.

Local and here hosting use the earliest of the caller's deadline, the award's
binding deadline, the owner deadline and the parent capability's expiry.
Expired authority stops before host binding. Binding and hosting share that
context. The host receives a copy of the awarded runner identity, so binding
cannot change its labels.

### Team runners fetch source themselves

Source acquisition clones the sealed SHA directly from the authorized Git host.
An optional source bundle seeds acquisition. No cache mirror or package proxy
sits between the runner and origin. Source credentials require live claim
authority. A repository name alone grants no source access.

### What a laptop runner trusts

A runner executes accepted repository code as its OS user. Its private
credential file keeps accidental disclosure out of configuration and output.
Code running as that same user may still read it and other files in the account.
Use a dedicated OS account, container or VM and enroll only repositories whose
code may use that account's permissions.

Runner repository restrictions, granted reservations and resource capacity
filter claims before preferences order eligible work. Labels grant no pool
membership or repository permission. See [scheduling](scheduling.md),
[Git credentials](git-credentials.md) and the [threat model](threat-model.md).

## Environment and secrets

A local run captures its environment once. Children inherit that snapshot, and
schedules capture theirs at installation. The daemon's ambient environment never
replaces accepted values. The sealed spec records names and a digest. Values
remain in private host storage.

Shared process policy filters every spawn. Remote submission passes only the
minimal base, network/CA settings and explicitly allowed names. Credential-shaped
names remain excluded even when allowed. Proxy credentials are the passed and
masked exception. Capabilities, bootstrap values and protocol handles travel in
private files, memory, pipes or argv as specified by their contracts, never env.

Job startup settings are projected before process creation. Body and cleanup
receive the same complete attempt environment. A changed replan fails as
`plan_drift`. Secret arguments stay available to execution but are redacted from
previews, receipts and diagnostics. See [environment variables](environment-variables.md).

## Authorization model

Local and remote controllers enforce the same route policy. Team authority,
repository access, job bindings and claim liveness come from authenticated server
facts. A management credential cannot substitute for the current execution
holder. Public run details are redacted projections, not executable inputs.

An awarded host retains its selected controller transport for private execution
state, source credentials and storage. These controller API calls use the award's capability.
The runner's reusable claim credential stays with the claim loop.

The controller logs each HTTP server failure once with its request ID and masked
cause. Match the response’s `X-Request-Id` to the controller log to investigate a
generic client error.
Unmatched routes log the method and sanitized path with the 404 response.
The path excludes query parameters and masks registered secrets and credential
selectors.

### The laptop boundary

The local controller authenticates its OS account. Unix sockets require private
ownership and kernel peer credentials. Windows uses the account SID. An
unauthenticated or foreign peer is refused. Browser sessions and CSRF checks
belong to the controller's authentication policy. There is no separate dashboard
store or dashboard-only credential model. Root and same-account code remain
inside the machine trust boundary. See [security](security.md#local-daemon-socket).

## Per-host concurrency

Admission arbitrates local CPU and memory. Concurrency groups coordinate declared
work through the controller. Neither is a credential boundary. Inline placement
keeps work on its run host without a reserved-label alias.

```bash
sparkwing queue list -o json
sparkwing queue priority --run RUN_ID --set front
sparkwing runs stats --sizing
```

Missing observations remain missing. They are never rendered as zero capacity.
An unavailable controller returns an error. See [admission](admission.md) and
[diagnosing admission](diagnosing-admission.md) for measurements and queue views.

### Capping sparkwing's share of the machine

Set the machine budget in the private machine file:

```yaml
admission:
  budget: 50%,enforce
```

Admission charges against that host's capacity. On Linux, an available writable
cgroup can enforce the machine budget. macOS scheduling controls are advisory.
The contribution preference controls advertised runner headroom, rather than an
OS resource sandbox. Operator ceilings refuse excessive author values instead
of silently clamping them. See [machine settings](machine-config.md).

## Windows with Git Bash

Go and TypeScript are supported on native Windows. Git Bash supplies Bash and
shell utilities, while pipelines still need native tools for the commands they
execute. Use `node.exe` to invoke JavaScript command entrypoints.

Windows process cleanup uses kill-on-close Job Objects. Unix uses owned process
sessions. Native Windows handle inheritance, cleanup and SDK execution require
Windows acceptance runs. Linux fixtures and cross-compilation do not establish
those behaviors. See [node protocol](node-protocol.md).


## Burst and overflow

Configure a Cloud connection and opt in through machine `run.routing`.
The machine file alone selects the route, profile, preferences, local secret
sources, overflow limit and untracked source files. Repository configuration
can restrict execution to this machine and declare required secret names.

```yaml
run:
  routing:
    route: cloud
    profile: work
    prefer:
      - target: here
        hold: 5s
      - target: machines
        hold: 10s
      - target: cloud
    local_secrets: [DEPLOY_TOKEN]
    source_files: [generated/fixture.json]
    overflow_minutes: 120
```

The route defaults to `local`. The example gives this machine the first chance,
then the user's registered personal machines, then Cloud. Holds apply
while an eligible preferred claimant is live. An absent claimant does not delay
later capacity. Omit `prefer` to use `here` for five seconds, `machines` for five
seconds, then `cloud`. `name=NAME` selects a registered machine by name.
Named and pool selectors can narrow the requester's eligible personal machines.
Cloud executors require a `cloud` entry. Runners outside the sealed preference
set cannot claim burst jobs or read requester secrets, even after all holds expire.
Team runners remain excluded. The attached here owner retains its planning authority.

Repository restrictions apply to machine-authorized routing. For example, keep
deployment on this machine:

```yaml
pipelines:
  - name: deploy
    entrypoint: Deploy
    run:
      route: local
```

Cloud owns the run record. The invoking CLI hosts its scheduler, streams logs
and requests cancellation on Ctrl-C. The first interrupt keeps the owner alive
until local and remote jobs finish cancelling. Secret reads remain refused
during this drain. A second interrupt stops hosting.
Nodes use the existing claim, lease and
fencing rules. The local controller supplies this machine's admission only.
Other machines register as runners with Cloud. `sparkwing runners add --runner NAME
--personal --session-stdin --profile work` reads an existing independent account
session from stdin and reserves the machine for that user's runs. The session
and its CSRF proof stay in memory. The command saves only the new runner
credential. A profile backed only by a member token cannot enroll a personal machine.
Cloud routing requires an attached terminal session. The default local route
continues to support `--sw-detached`.

Registered machines advertise measured CPU and memory headroom. Their
`contribution` setting caps that offer, and `keep_free` reserves capacity for
other work. Enrollment enables `local_admission`, which shares the machine's
admission controller with local runs. If that controller is absent, a released
runner fetches the verified matching CLI and starts it before claiming work.
Each claimed job holds its machine lease through source preparation, execution
and cleanup. A full machine offers zero headroom, so later preferences can win.
Busy local work does not prevent jobs from becoming ready for overflow.
Jobs without resource settings use the same one-core admission charge locally
and when matching a runner. The requesting machine reserves compute before claiming a job, because local
work can arrive after a capacity observation. A waiting job stays available to
other ranked claimants. If another machine advances it, the requester cancels
its compute wait. Ctrl-C also cancels the wait.
Concurrency slots count once even when a registered runner shares this machine
with the requesting CLI.
Machine admission keeps Cloud run identities separate from local records.
Cloud overflow captures the working tree. Use `pipeline trigger` for explicit
committed revisions instead of `--sw-ref` or `--sw-pipeline-ref`.

An unreachable controller before intake, including connection setup and source upload,
produces a message and a local run.
Authorization failures remain errors. After intake, a lost response or connection
cannot start a second local run because the admitted run keeps its Cloud owner.

The source bundle contains Git-tracked files with their working-tree edits and
deletions. Untracked files require their exact repository-relative paths in
machine `run.routing.source_files`. A repository `source_files` declaration
requires that machine approval, including for ignored paths. The bundle has
no parent history. Secret-shaped files are refused before upload. The local
secret scan checks changed and uncommitted blobs, skips values shorter than
eight bytes and ignores PEM armor lines. Log masking keeps its full patterns. Bundle upload uses authorized content-addressed source storage.
Each executor prepares its private source workspace on first use and builds
the pipeline from the uploaded snapshot with its selected
engine. Go installation downloads the dependencies used by the pipeline.

Jobs publish declared artifacts through the same storage path on this machine,
on a personal runner and in Cloud. A dependent job's `Consumes` declaration
restores the producer's files into its workspace, including across machines.
A missing or corrupt artifact fails the consumer.

Declared dependency caches restore before the job and save successful cold
directories through its current storage authority. This applies here, on personal
machines and in Cloud. Package-manager cache paths use the persisted job
environment. Jobs share matching dependency caches within the same repository
and ref, while every write retains its originating job attempt for authorization.
Cache transfer failures warn and leave the next job to run cold.

Cloud allocates the CPU class selected by the team's price table and the
memory that comes with that class. This also covers jobs without explicit
resource settings. The allocation stays fixed for the award if prices change
or bootstrap authority is exchanged. Team standing and your overflow allowance
still control new Cloud admissions.

```yaml
pipelines:
  - name: test
    entrypoint: Test
    source_files: [generated/fixture.json]
```

Declare secrets in the pipeline, then authorize the names to resolve locally in
machine `run.routing.local_secrets`. Repository `run.local_secrets` lists names
only and cannot select requester sources or supply values. Machine secret
classification also remains in force for local snapshots. The CLI reads those names from its captured environment at
intake. Required missing values fail before submission. Other declared names
use the team's normal secret provider. Values travel outside the sealed request
and bundle, stay in controller memory, and are released only to the live
claiming process with masking enabled. The attached here host re-supplies values
on authenticated owner heartbeats and when it resumes after a controller restart.
Team deletion, terminal completion and owner lease expiry purge those values.
The Cloud chart supports one controller replica and uses a Recreate rollout.
Run intake, plan validation and secret release reach that same process.
Multiple controller replicas require an ephemeral secret relay or verified
run affinity before they can support requester-resolved secrets.
Retry also requires a fresh submission so values can be resolved again.

Local routing and offline fallback resolve the same configured names at intake.
Those environment entries are excluded from the saved local environment
snapshot. The local controller releases their values through the same masked,
volatile secret path.

There is no per-user overflow cap by default. `overflow_minutes` narrows the
user's monthly Cloud allowance for this run. The account's saved limit can only
be tightened by the request. Read and change your saved allowance with:

```bash
sparkwing overflow show --profile work
sparkwing overflow set --profile work --minutes 120
sparkwing overflow set --profile work --minutes 0
sparkwing overflow set --profile work --uncapped
```

Zero prevents new Cloud overflow. `--uncapped` removes your saved cap.
Use `-o json` for the saved `monthly_minutes`, `used_seconds` and UTC
`period_start`. `GET /api/v1/overflow` reports `monthly_minutes`,
`used_seconds` and the UTC `period_start`. `PUT /api/v1/overflow` saves
`{"monthly_minutes":120}`, and `{"monthly_minutes":null}` removes the saved cap. These operations
use the selected account credential. Team standing tiers, owner budgets and the
billing charge ladder still apply. Once the allowance is reached, new Cloud
claims wait while local and registered-machine capacity remains eligible.
Running work finishes.
Deleting run history keeps its usage in the monthly total.

Local publication has a 45-second request deadline. During publication, a second
Ctrl-C abandons the upload and reports that the run stayed local-only. Publication
failure preserves the local execution verdict. Published history never replaces
the team's latest successful execution pin.
