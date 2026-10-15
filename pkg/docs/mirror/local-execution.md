# Local execution

`sparkwing run` hosts a pipeline on the invoking machine. `pipeline trigger`
submits it to a controller. Both placements use the shared run and job hosts,
sealed inputs and matching SDK engine.

This guide describes the rewrite contract. The CLI's local and initial-trigger
paths still need their final host, source, storage and lifecycle bindings.
Native execution, detachment and standalone behavior require acceptance evidence.
See [rewrite integration](architecture.md) for that boundary.

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

## Local execution

The local controller owns one home, its SQLite store, admission, queue and
browser dashboard. It starts on demand. The CLI hosts a foreground run and
sends state operations through the controller API. SDK processes declare and
execute work through protocol v2. They do not open the controller's database.

The controller is the only writer of the shared store. Reads, cancellation,
retry and doctor use its API too. The controller's route policy and wire
contract also apply to `api.sock`. A transport retries only operations the
registered route marks safe to replay. A lost one-time award is never replayed
automatically.

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
These adapters require the native local-services binding described above.

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

## Remote execution

```bash
sparkwing pipeline trigger deploy --profile prod --detach -- --environment staging
sparkwing runs status --run RUN_ID --profile prod
sparkwing runs logs --run RUN_ID --profile prod --follow
```

The selected controller owns the run's records. Its intake derives the caller's
authority. A runner claims trigger and job units through one ordered queue. The
launcher creates Jobs for awarded plan, eval and work units. Both use the shared
matcher and hosts. Inline jobs stay with the run host.

The working-tree option submits an immutable source snapshot. Secret-shaped
files require explicit review through the allowed-file option. Keep credentials
in ignored files or outside the repository and inspect `git status` before
submission. Final signed source-bundle upload and initial-trigger intake still
need their controller and storage bindings. The option's presence in help is not
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
