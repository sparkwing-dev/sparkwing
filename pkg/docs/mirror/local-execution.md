# Local execution

`sparkwing run` hosts a pipeline on the invoking machine. `pipeline trigger`
submits it to a controller. Both placements use the shared run and job hosts,
sealed inputs and matching SDK engine.

Intake and hosts derive plan placement from the sealed request. Local runs,
local triggered runs, runner-hosted runs, launcher runs, `--runner here` and
GitHub Actions use the same capability table for initial plans and expansions.

If the pipeline's `go.mod` replaces the Go SDK, Sparkwing uses the installed CLI's
engine and warns that engine pinning is off for that run. The shared installer
builds the replacement locally.

The commands bind sealed intake to the shared controller, source, private
environment, storage and host services. The CLI sends state operations over the
authenticated private API. Owner evaluation delegates a fresh SDK process from
the live owner lease. It keeps the evaluated job separate from that lease and
creates no work attempt. Each service call rechecks that job's persisted source
fence and declared output edges. A separately awarded eval process uses its own
eval lease.
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
uses this machine's controller and accepts no connection profile. A profile
selects the controller for `pipeline trigger`, run observations and management.

| Invocation | Execution and records |
| --- | --- |
| `sparkwing run build` | The CLI hosts the run. The local controller owns its records. |
| `sparkwing run build --sw-detached` | The local controller queues and hosts work independently of the submitting terminal. |
| `sparkwing pipeline trigger build --profile prod` | The selected controller queues the run for an eligible runner or launcher. |
| `sparkwing pipeline trigger build --profile prod --runner here --reason repair` | This CLI hosts the controller's named here run. |

An explicit empty `--sw-cache-from=` disables `run.cache_from` from the machine
file. Publication still needs its authorized result/log publisher. A missing
publisher fails before intake. It cannot change a completed local verdict.

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

Standalone CI runs the same controller runtime in process with its own home.
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
`--sw-request-id` is a tracing field and does not deduplicate work. JSON receipts
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
