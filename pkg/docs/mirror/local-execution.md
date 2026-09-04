# Local Execution

Sparkwing pipelines run anywhere -- on a Kubernetes cluster, on your
laptop, or both. This is a core design advantage: your CI/CD is not a
black box in the cloud, it is a portable program you can run yourself.

## Why local execution matters

Most CI systems only run inside their own infrastructure. If GitHub
Actions is down you can't deploy; if your Jenkins server crashes,
builds stop. Your ability to ship depends on their uptime.

Sparkwing pipelines are Go programs. You can run them on any machine
with a Go toolchain; Docker only matters when a pipeline step builds
container images. This means:

- **Deploys don't stop when services go down.** GitHub down? Your
  laptop can still build, push images, and update your cluster.
- **Fast iteration.** Local Docker cache, local Go module cache, no
  upload round-trips. Edit -> build -> deploy in seconds.
- **Debuggable.** When a pipeline fails, run it locally with the same
  code and see what happens. No "push and pray."

## How it works

```bash
# Run locally -- uses your Docker, your caches, your machine
sparkwing run build-deploy

# Run locally, but record state to a remote profile's backend
sparkwing run build-deploy --profile prod

# Trigger remote execution -- the cluster runs it via the controller
sparkwing pipeline trigger build-deploy --profile prod
```

`sparkwing run` always executes on the machine you invoke it from.
`--profile` only changes *where state/cache/logs live* and which auth is
used to reach them; the work still happens locally. To hand execution to
a cluster, use `sparkwing pipeline trigger` (covered below). All three
run the same pipeline code -- the difference is where the work happens and
where its records land.

### Local execution

```
Your laptop:
  1. sparkwing run compiles the pipeline from .sparkwing/
  2. Pipeline runs whatever its code says (test, build, deploy, etc.)
  3. sparkwing run records the run through the admission daemon,
     which owns ~/.sparkwing/state.db; log files stay per-run on disk
```

Your laptop runs the pipeline directly. No sparkwing controller is
involved. Each invocation's outcome lands in the SQLite store under
`~/.sparkwing/`, which is what `sparkwing dashboard start` reads.
Run `sparkwing dashboard start` once and leave it up to watch
concurrent runs in a browser without needing any remote service.

The run does not open that store. It sends its run, node, event, and
concurrency calls to the admission daemon over the daemon's `api.sock`,
and hands each node subprocess the same socket; the child runs it
dispatches and any node replayed from it choose the same way. No process
in a hosted run holds the file open, so the store's schema is out of a
pipeline binary's contract. The CLI verbs that read the file -- `sparkwing
runs`, `sparkwing jobs`, `sparkwing doctor`, the dashboard -- are the
installed build or a peer of it and still open it directly.

A run the daemon cannot serve opens a store of its own instead, which is
why a machine with no sparkwing installed still runs pipelines. It is not
the shared file: see [running with no daemon
available](#running-with-no-daemon-available).

The daemon is restartable under a run. A state write the daemon can be
shown not to have applied -- one it answered `503`, or one on a
connection it never accepted -- is retried for up to 20 seconds, which
covers a supervisor restart and the successor rebinding the socket. A
write whose fate the client cannot establish is not repeated, and a
daemon still unreachable past that window fails the run naming it.

Local pipelines share a small admission daemon named wingd. It starts on
demand and normally needs no operator attention. `sparkwing daemon status`
reports whether it is running; JSON output includes the serving binary, its
source revision, the runs-store schema that binary understands, and the schema
this home's store holds. A daemon behind the store cannot read it and refuses
every run, so status reports it unhealthy and names both schemas; a run that
hits the same skew fails with that comparison rather than a capacity error. The
remedy is a sparkwing whose schema matches: install one, or point
`SPARKWING_WINGD_BIN` at a binary that has it and stop the daemon. Restarting
does not help when the installed build is the daemon's own build, and status
says so. A store that exists but cannot be read is reported the same way rather
than as an absent store.
`sparkwing daemon restart` replaces only an answering daemon
when its build differs from the installed Sparkwing build. Add `--force` to
replace an answering daemon that already serves the installed build. Existing
holders reconnect and reattach to their durable leases, while a deliberately
stopped daemon stays stopped. A
release-pinned pipeline can use that refreshed daemon without replacing it
with the older release build.

When you run locally against a remote profile (`sparkwing run X --profile
prod`), the run dual-writes state to both the profile's backend and the
local SQLite store. The remote is canonical; the local copy is a free
byproduct, so `sparkwing runs list` on your laptop sees the run afterward
even with no network. Set `mirror_local: false` on a profile to skip the
local copy for automated workers that fire off many runs.

See [native-mode.md](native-mode.md) for the full local-mode design.

### Submitting a run and walking away

`sparkwing run` executes in your terminal: close it and the run dies
with it. When the work outlasts the session you are willing to hold
open -- a long deploy over ssh, a nightly job kicked off by hand, an
agent that must not block -- submit it instead:

```bash
$ sparkwing runs submit nightly-report
run run-20260811-140322-1f2e3d4a submitted (nightly-report)
  logs:   ~/.sparkwing/runs/run-20260811-140322-1f2e3d4a
  follow: sparkwing runs logs --run run-20260811-140322-1f2e3d4a --follow
  cancel: sparkwing runs cancel --run run-20260811-140322-1f2e3d4a
```

The command returns immediately. By the time it prints, the run is
persisted and a resident consumer process on this machine owns it. Close
the terminal, drop the ssh connection, log out: the run keeps going, and
`sparkwing runs status --run <id>` answers from any shell afterward.

`log_path` is the directory the run's node logs land in. It follows the
same rule as the `run_start` receipt: present only when the directory
exists, never a plausible-looking path to nothing.

For scripting, `-o json` gives `{run_id, log_path, ...}` and `-o plain`
gives the bare id:

```bash
RUN=$(sparkwing runs submit -o plain build)
sparkwing runs wait --run "$RUN"
```

Everything after the pipeline name is the pipeline's own argument list,
so submit's flags go **before** it:

```bash
sparkwing runs submit --idempotency-key k deploy --env staging
```

A submit flag typed after the pipeline name is refused rather than
quietly passed through as a pipeline argument -- silently accepting
`--idempotency-key` there would leave a caller believing its retry was
deduplicated when it was not. If a pipeline genuinely declares a flag by
the same name, `--` ends submit's arguments:

```bash
sparkwing runs submit deploy -- --request-id its-own
```

#### Making a retry safe

If a submission fails ambiguously -- the connection dropped, the process
was killed after the command left your keyboard -- you cannot tell
whether the run was created. Pass `--idempotency-key` and stop caring:

```bash
sparkwing runs submit deploy --idempotency-key deploy-2026-08-11-a
```

A second submission carrying a key an earlier one used returns the
*original* run id, marked `already submitted`, and creates nothing. The
constraint is enforced by the runs store, not by a check-then-write in
the CLI, so two callers racing with one key still produce one run.

`--request-id` is a separate field with a separate job: it is recorded on
the run for tracing and **never** affects deduplication. Use a fresh
request id per attempt and a stable idempotency key per intent.

#### What submission cannot do

Flags that change what a run *does* but cannot survive detachment are
refused with the reason rather than silently ignored: `--sw-index`
(the index binding is a live path the submitting process holds open),
`--sw-ref` (nothing would clean up the worktree afterward),
`--sw-dry-run`, `--sw-start-at`, `--sw-stop-at`, `--sw-only`,
`--sw-no-cache`, `--sw-mode`, `--sw-workers`, `--sw-allow`,
`--sw-local-only`, `--sw-secrets`, and `--profile`. Run those in the
foreground with `sparkwing run`. Everything else after the pipeline name
is passed to the pipeline as its own arguments.

Submission is local-only. To hand a run to a cluster, use
`sparkwing pipeline trigger --profile <p> --detach`.

#### Publishing a foreground run handle

`sparkwing run PIPELINE --sw-run-handle-file PATH` atomically writes a JSON
handle after the run row is durable and before planning or node execution.
The handle carries `schema_version`, `run_id`, `pipeline`, `log_path`, and
`status`. Sparkwing exits before executing work when publication fails.

The outer CLI passes the path to the pipeline process through
`SPARKWING_RUN_HANDLE_FILE`; callers should use the flag. The file is mode
`0600` and replaces its destination atomically.

#### A submitted run uses an allow-listed submission environment

A foreground run inherits the whole environment of the shell that starts
it. A submitted run carries a filtered snapshot of it: every `SPARKWING_*`
and `GITHUB_*` variable, plus `PATH`, `HOME`, `HOSTNAME`, and
`KUBERNETES_SERVICE_HOST`. Sparkwing drops the credential-shaped part of
that set -- names carrying `TOKEN`, `SECRET`, `PASSWORD`, `KEY`, `AUTH`,
`PAT` and similar, bearer headers, PEM blocks, JSON documents with a
credential field, and URLs whose userinfo, query, or path names a
credential -- so a queued run never writes one to disk. A submission never
inherits values from the shell that started the consumer or from another
submission.

Set `SPARKWING_SUBMIT_ENV_ALLOW` to a comma-separated list to widen the
snapshot; an entry ending in `*` matches a prefix, and a bare `*` is
refused rather than silently allowing nothing. The credential filter still
applies to what the list names, and logs at warn the names it drops, so a
pipeline that needs a credential takes it from the secret store rather than
the submitting shell.

```bash
SPARKWING_SUBMIT_ENV_ALLOW='AWS_PROFILE,AWS_REGION,KUBECONFIG,DOCKER_HOST,SSH_AUTH_SOCK' \
  sparkwing runs submit deploy
```

Sparkwing stores the snapshot outside the runs database with mode `0600`,
names it by a hash of the run id, and deletes it when the consumer starts
the run. A run that a consumer shutdown returns to the queue no longer has
a snapshot, so its next dispatch fails rather than running with the
consumer's environment; submit it again.

Prefer pipeline configuration, secret stores, and pipeline arguments for
values that should remain independent of a caller's ambient environment.

#### Which checkout runs

The checkout you are standing in wins, and `-C PATH` points at a
different one. Only if neither declares the pipeline does the repo
registry get consulted. The chosen directory is recorded on the run, so
the consumer executes the tree you submitted from even when a second
checkout of the same project declares the same pipeline name.

#### Cancelling a submitted run

`sparkwing runs cancel --run <id>` works at both stages. Before a
consumer claims it, cancellation is a store transaction that marks the
run cancelled and takes it off the queue -- no dashboard and no profile
required. Once it is running, the admission daemon holding the run's
process cancels it the same way it cancels any local run. Either way the
cancellation names one run id and can only reach that run: a
resubmission is a different run with a different id.

#### The consumer process

One consumer per sparkwing home claims queued runs and executes them.
`sparkwing runs submit` starts one when none is resident and confirms it
owns the queue *before* acknowledging your run, so the acknowledgment
means the machine has taken ownership -- not merely that a child was
forked. The consumer exits on its own after five idle minutes.

Exactly one serves a home at a time, enforced by a file lock rather than
a PID check: a consumer killed with `kill -9` releases the lock
immediately and leaves nothing stale to clean up. A running dashboard
consumes the same queue; whichever holds the lock does the work and the
other stands down, so a run is never dispatched twice.

```bash
sparkwing runs consumer status   # exits 1 when none is resident
sparkwing runs consumer start    # keep one up deliberately
sparkwing runs consumer stop     # queued runs stay queued
```

Recovery is automatic in both directions. Work queued while no consumer
is resident runs as soon as one comes back. A run whose consumer was
killed mid-dispatch stops having its claim renewed, and the next
consumer sweeps the lapsed claim back onto the queue -- unless the run
already reached a terminal status, in which case the claim is closed out
rather than re-executed. Every acknowledged run ends up recoverable or
terminal; none is lost, and none runs twice. A re-executed run is the
same run, not a new one: it keeps the run id you were given along with
its arguments, trigger, and submission time, and only its start time
reflects the attempt that actually ran.

Stopping a consumer never cancels queued runs. Use `runs cancel` for
that.

A run that is *executing* when you stop the consumer is interrupted and
returned to the queue, not failed: it never reached a verdict, so the
next consumer re-executes it from the start. If you want it to stop for
good, cancel it rather than stopping the consumer.

The consumer is also replaced when it is out of date. It records the
sparkwing version it was built from, and a submission from a different
build stops the old consumer and starts one from the new binary --
otherwise a home with a steady queue would keep serving every run from
the build that happened to start first, and an upgrade would never take
effect. Replacing a consumer interrupts whatever it was executing, on
the same terms as stopping one: that run returns to the queue and the
new consumer re-executes it from the start.

### Bouncing a wedged job

Every job in a local run is its own process, which means one job can be
restarted without touching the run around it. `sparkwing runs bounce
--run <id> --node <job>` stops that job's process -- SIGTERM, then
SIGKILL after the grace period -- and runs the job again in place. The
job never reaches a terminal state, so nothing downstream sees a
failure and no other job is disturbed; the run finishes normally on the
attempt that survives.

Reach for it when a job is wedged or misbehaving and cancelling the
whole run would cost more than it saves -- a fifty-minute pipeline
whose deploy step is stuck on a connection that will never answer.

The verb records the request and returns. The runner supervising the
job picks it up within a few seconds, so the stop is prompt rather than
instant, and a job that finishes in the meantime is left alone.
Bouncing again is allowed: one request is one restart, and a job that
wedges repeatedly is bounced repeatedly.

The job re-runs from its **first step**, not from where it stopped.
Steps that already ran run again, so a job with side effects needs the
same idempotency a restarted Kubernetes pod already demands. The node
keeps the admission lease its run holds -- the work was priced once,
and restarting it is not a new charge -- and what the killed attempt
cost the machine is added to the job's recorded CPU and occupancy,
because the box paid for it.

### Remote execution

```
Your laptop:
  1. sparkwing resolves the origin, branch, and commit
  2. sparkwing refreshes or seeds that commit, then POSTs the trigger

Remote runner:
  3. Controller records the trigger; a polling runner claims it
  4. Runner clones the exact commit, compiles, and runs the pipeline
  5. Runner streams logs through the logs service
```

The controller is the gatekeeper for prod-side execution: only the
cluster can push to ECR, update gitops, and dispatch warm runners.

`sparkwing pipeline trigger <pipeline> --profile prod` submits the trigger
to the profile's controller for remote execution. The chosen profile must
have a `controller:` set; passing a controller-less profile errors with a
clear message. By default the command follows the remote run until it
reaches a terminal state -- full log streaming when the profile defines a
logs URL, node-status updates from the controller otherwise. Pass
`--detach` to return as soon as the trigger is registered without
following.

Add `--working-tree` to run current tracked edits and untracked non-ignored
files remotely without committing or pushing them. Sparkwing freezes those
bytes as a synthetic Git commit, requires the bundle seed to finish before it
admits the trigger, and prints the base SHA, snapshot SHA, file count, and
bundle size. The source checkout's HEAD, refs, index, and object database stay
unchanged. The bundle limit is 500 MiB.
The remote checkout is clean and detached at the synthetic SHA; file contents
match the laptop, but staged-versus-unstaged state is intentionally flattened.
Capture requires a complete SHA-1 repository; shallow and SHA-256 repositories
fail before upload. Workspace seed refs are capped at 128 distinct snapshots
per repository; a full cache rejects a new snapshot before trigger admission.

`SPARKWING_FLEET_CONFIG` is the one supported environment override for this
feature; it selects a `fleet.yaml` outside the default config directory. The
remaining `SPARKWING_FLEET*` names are private parent-to-pipeline handoff, not
configuration: `SPARKWING_FLEET`, `SPARKWING_FLEET_SOURCE_ROOT`,
`SPARKWING_FLEET_SOURCE_BUNDLE`, `SPARKWING_FLEET_SOURCE_SHA`,
`SPARKWING_FLEET_SOURCE_REPO_URL`, `SPARKWING_FLEET_SOURCE_FILES`,
`SPARKWING_FLEET_SOURCE_BYTES`, `SPARKWING_FLEET_SOURCE_BUNDLE_BYTES`,
`SPARKWING_FLEET_PARENT_GUARD`, and `SPARKWING_FLEET_PARENT_TOKEN`. Sparkwing
sets and validates them for the lifetime of one foreground run. Do not set or
forward them; submitted environments and retry snapshots remove the parent
guard and token.

An off-cluster machine can claim only these triggers and compile them locally:

```bash
SPARKWING_AGENT_TOKEN=... sparkwing-runner runner \
  --controller=https://sparkwing.example.com \
  --logs=https://sparkwing.example.com \
  --gitcache=https://sparkwing.example.com/api/v1/gitcache \
  --also-claim-triggers --claim-nodes=false \
  --trigger-sources=pipeline-working-tree@laptop-hostname \
  --metrics-addr= --max-claims-before-restart=0
```

The source proxy and trigger claim require the current admin-capable runner
token. Login-enabled dashboard ingress passes this machine bearer directly to
the controller without a browser session or CSRF token. The process opens no
listener. A private direct cache URL can replace the controller proxy when the
machines already share a LAN, VPN, or tailnet. Direct cache binary and seed
writes use only `SPARKWING_CACHE_TOKEN`; the agent/controller token is never
sent to that raw cache. Raw Git reads have no cache-level auth, so keep a direct
cache on a trusted private network. Upload and pack streams have
a 30-minute server window; the CLI gives a direct upload two minutes before a
fresh 15-minute controller fallback. Manual retries and same-repository
`RunAndAwait` children retain the original `pipeline-working-tree@<host>`
placement source.
Do not leave an unrestricted cluster runner racing for the same trigger source
when testing deterministic placement.

### Remote machine capacity

`sparkwing-runner agent` has separate legacy and enrolled modes.

The name-less singular configuration uses the existing outbound FIFO
`/api/v1/nodes/claim` loop. Its `labels` are self-asserted placement terms,
not administrator-trusted capabilities. The bundled service installer writes
this format. Existing files keep their `local_admission` setting, including an
explicit `false`; when enabled, legacy local admission happens after a claim.

Named or plural configuration selects enrolled assisted-offer mode. Before
starting it, a controller administrator binds the executor to the exact prefix
of a live runner or service token:

```bash
sparkwing cluster agents enroll --profile prod \
  --name desk --token-prefix swr_01234567 \
  --kind agent --location local --capability linux-amd64 \
  --base-priority 10 --priority-ceiling 30 \
  --max-concurrent 2 --budget-cores 4 --budget-memory-bytes 8589934592
```

The controller-owned enrollment is the trust envelope. Kind identifies an
`agent` or `gateway` execution boundary; location is controller-owned placement
policy. `location=local` and `location=cloud` requirements match only this
field, while `unknown` fails both. The reserved `location=coordinator` selector
and compatibility alias `local` are ungrantable to helpers. The awarded value
also becomes immutable attribution and a hard requirement for an agent-loss
retry. Capabilities, priority range, concurrency ceiling, and
resource budget come only from enrollment. Worker traffic cannot add or widen
them, and the agents API never returns the credential prefix or principal.

Set `name` for one enrolled coordinator, or use `coordinators` for several.
Every membership needs a distinct revocable token and its enrolled name;
network discovery never grants trust. The top-level concurrency and
contribution settings are machine-wide local ceilings. A membership may narrow
them, never widen them. wingd enforces both levels when it grants each
reservation, so simultaneous slots and separate agent processes cannot
oversubscribe a ceiling after advertising stale headroom:

```yaml
name: desk
max_concurrent: 2
contribution: 4,8gb
local_admission: true
local_reserve: 1,2gb
coordinators:
  - controller: https://personal.example.com
    token: <personal-agent-token>
    max_concurrent: 1
    contribution: 2,4gb
  - name: desk-at-work
    controller: https://team.example.com
    token: <team-agent-token>
```

Enrolled mode requires local admission. It probes wingd for finite nonnegative
headroom and reports liveness to each coordinator; a failed probe sends no
heartbeat and does not clear the coordinator's last report. Coordinator loops
restart independently. Idle enrollments remain visible, and stale ones appear
offline.

For each idle slot, the agent asks a coordinator for the oldest eligible node,
then reserves the returned resource digest and physical slot through wingd
before offering. The reservation remains pinned through the offer round. An
award consumes that same lease; a loss or expiry releases it. The agent shares
one slot ledger across all configured coordinators, so the same physical slot
cannot back simultaneous offers to two controllers. A gateway needs an
equivalent downstream admission reservation before it offers.

The controller waits no more than five seconds. Priority 100 and the exact
highest eligible effective priority recorded at round open win immediately;
otherwise the deadline winner is the highest effective priority, then the
earliest offer, executor name, slot, and holder. `Requires` and resource limits
filter before ranking.
Run priority and the first matching `Prefers` term add to base priority and are
then clamped inside each administrator-owned priority range. A preference is a
small tie-breaking boost, not an absolute override: base priority can still
outweigh it. Arbitration is scoped to one controller; configured memberships
share physical slots but do not yet compare priorities across controllers. A
retry after a lost response recovers the same fenced claim. Legacy direct
claims remain FIFO and do not use this ranking.

The controller owns retries after an agent or gateway disappears. The source
node becomes terminal `agent_lost` and is never resumed. Loss before the
job-body start acknowledgement creates a fresh linked run without spending
`.Retry(n)`; loss after acknowledgement spends the persisted invocation count.
The same total budget covers local retry loops, `RetryAuto` dispatches, and
fresh runs. A replacement keeps the captured source/plan and the original
coordinator and location, rehydrates unrelated terminal work, and reruns only
the lost work and its descendants after durable backoff. Another executor is
preferred briefly, but the original may reclaim when it is the only eligible
capacity. Coordinator fallback cannot relax the required placement. External
effects remain at-least-once within the configured budget.

With the Helm values `runner.triggerRunner.kind: warm` and
`runner.automountServiceAccountToken: true`, a trigger worker offers each node
to remote agents first. If no agent claims an unlabeled node within the
internal window, the worker atomically removes the offer and creates a
Kubernetes Job with the chart's existing image, namespace, service account,
pull policy, and cache settings. A concurrent agent claim defeats revocation,
so fallback cannot double-execute the node. Labeled nodes stay agent-only
because the fallback Job does not advertise labels. Saturated or offline
agents therefore spill generic work to Kubernetes without weakening placement
requirements.

The full-chart path is
`sparkwing-runner-bundle.runner.triggerRunner.kind: warm`, together with
`sparkwing-runner-bundle.runner.automountServiceAccountToken: true`. The
default remains `inprocess` and renders an empty Role. Warm mode adds only the
Job lifecycle and pod-read permissions the Kubernetes fallback calls.
For a manually launched runner, `SPARKWING_RUNNER_SA` supplies the service
account used by `--runner k8s`, `--trigger-runner k8s`, and warm fallback Jobs;
the matching command-line flags take precedence.

On Kubernetes a pipeline's `.Resources()` pin becomes the Job pod's requests
and limits, so an operator bounds it: `--k8s-cpu-ceiling` and
`--k8s-memory-ceiling` (or `SPARKWING_K8S_CPU_CEILING` and
`SPARKWING_K8S_MEMORY_CEILING`, which the Helm values
`runner.jobCeiling.cpu` and `runner.jobCeiling.memory` set) take Kubernetes
quantities such as `8` and `16Gi`. A pin or a measured charge above the
ceiling is clamped to it, burst limit included, and the runner logs the pin,
the ceiling, and the size it settled on, and writes the same line to the run
as a `resource_clamped` event. Both default to empty, which means no ceiling
and a pin that becomes the pod size verbatim. A value that is not a positive
Kubernetes quantity fails at startup rather than at pod creation. The
namespace-wide backstop is the chart's `limitRange` and `resourceQuota`; see
the `sparkwing-runner-bundle` README.

Enrolling a workstation or gateway authorizes repository pipeline code to run
as the agent service's OS user. Complete assisted execution starts every job
body in a child process. The supervisor keeps the enrollment token and claim
identity; the child receives a process-lifetime loopback capability limited to
its awarded run and node. Execution start, finish, and logs also require the
acknowledged attempt ordinal. That capability cannot claim or renew work,
manage the fleet, or call administrative routes. The child inherits only the
minimum runtime environment plus non-credential variables explicitly named by
`SPARKWING_SUBMIT_ENV_ALLOW`, not the agent service's arbitrary cloud, cache,
or service credentials.

This is credential isolation, not an OS sandbox. Pipeline code can still read
files, use the network, and start processes with every permission the agent OS
user has. Run an agent under a dedicated account and enroll it only for
repositories whose code that account may execute. Give each device its own
short-lived runner token and revoke it when the device leaves the pool.
Native Windows helpers bind a suspended body to a non-breakaway Job Object
before its code can run, then wait for the Job to reach zero active processes
after exit or cancellation. Linux, macOS, and WSL helpers terminate and empty
the body's dedicated process session. Unix code can leave that session with
`setsid`, so pipeline bodies must not daemonize out of it. Sparkwing treats
deliberate escape as trusted OS-user code, not as a containment failure.
Sparkwing never joins a tailnet or changes host networking; configure the
controller connection outside Sparkwing. Do not expose a raw unauthenticated
cache outside a trusted private network. Source snapshots remain immutable,
and cached source or binary objects do not contain credentials. The compiled
pipeline binary interprets `warm`, so upgrade the controller, runner, and
pipeline module to the same release before enabling this mode.

Schema 30 is an internal dependency of the assisted-execution release, not a
standalone compatibility boundary. For a foreground `--sw-fleet` run it makes
the fixed listener, enrolled-helper authentication, offer and award, exact
source handoff, and coordinator fallback operational. It does not authorize a
helper to complete a remote job body. Current-attempt-scoped body mutations,
the complete attempt API, and durable grants for `Memoize`, `Concurrency`,
`ToolSlot`, `RunAndAwait`, cross-pipeline references, and dynamic `SpawnNode`
arrive together in schema 31. Do not deploy schema 30 as remote execution.
At this boundary the helper's observed OS, architecture, and environment are
startup diagnostics only; they are not persisted or matched for scheduling.
Use an explicit non-reserved trusted capability when testing admission. Schema
31 must carry observed platform facts through heartbeat, persistence, matching,
and the dashboard before selectors such as `os=windows` are truthful.
The bundled service installer supports Linux and macOS. Native Windows agents
run under an operator-managed service; WSL can use the Linux installer when
systemd user services are enabled.

A follow exits on the run's outcome, the same way a local `sparkwing run`
does: 0 when the run succeeded, 1 when it failed or was cancelled, with the
run's status block and failing-node errors printed to stderr so a
`> run.log` redirect still shows why. If the follow ends without a readable
terminal status -- a dropped connection, a controller restarting mid-run --
the command exits 3 rather than guessing an outcome, and names the run to
re-check with `sparkwing runs status --run <id> --profile <p>`. `--detach`
always exits 0: it reports that the trigger was queued, not how the run
ended.

## Authorization model

Sparkwing intentionally does **not** try to be a permissions boundary
between developers and infrastructure. Authorization is enforced where
it actually lives: the registry, the gitops repo, kubectl. A
developer with ECR push and gitops write access can deploy with or
without sparkwing.

A foreground `sparkwing run` executes with the operator's own authority.
The pipeline process is a child of your shell, holding your kubeconfig,
your cloud profile, your ssh agent, and your git credentials, and it can do
anything you can do from that terminal. A submitted run is narrower: it
carries only the allow-listed snapshot described above. Read a pipeline before you run it, and run
untrusted pipeline code under an account whose reach you accept.

Sparkwing does protect its laptop-local persistence from other OS users. On
POSIX systems it creates `$SPARKWING_HOME`, run directories, and private cache
directories as `0700`, and local state databases, SQLite sidecars, logs, PID
files, and materialized run values as `0600`. `sparkwing doctor --dry-run`
lists permissive legacy paths; `sparkwing doctor` tightens them without
following symlinks or removing the owner execute bit from cached pipeline
binaries. Portable Go file modes do not describe Windows DACLs, so doctor
reports that audit as unverified on Windows rather than claiming the ACL is
private.

### The laptop boundary

Laptop mode trusts the user account on the machine, and nothing narrower.

`sparkwing dashboard start` serves the controller API and the dashboard from
one process with no bearer check, so every caller that reaches the listener
can trigger pipelines, read secrets, and delete runs. It binds
`127.0.0.1:4343` and refuses a non-loopback `--addr` unless you pass
`--allow-remote`; a browser request whose `Origin` is neither loopback nor
named in `--allow-origin` is refused too. Those checks are the whole
boundary. Passing `--allow-remote` hands every host that can reach the port
the authority of your account, which is why the flag exists rather than a
quieter default.

The admission daemon draws the same line. `wingd` listens on a unix socket
whose path is a function of `SPARKWING_HOME`, and everything running as your
account shares that daemon and can queue, inspect, cancel, and drain its
runs. The socket carries no token, because a token your account can read is
a token anything running as your account can read. Give each user of a
shared host their own `SPARKWING_HOME`; see
[security.md](security.md#local-daemon-socket) for the ownership, mode, and
peer-credential checks that keep other accounts out.

Cluster mode is where authentication lives. A controller reached over a
network authenticates every request and scopes it per principal; see
[auth.md](auth.md).

**What sparkwing controls:**

- Which clusters a pipeline can dispatch to (via the `--profile` target's
  controller and its bearer token / scope).
- Audit trail of who ran what, when, from where (in the runs store).
- Consistent workflow (tests always run before deploy, declared once
  in the Plan).

**What infrastructure controls:**

- Who can push images to ECR (IAM roles).
- Who can push to the gitops repo (GitHub permissions).
- Who can `kubectl` into the cluster (RBAC).
- Who can call the controller API (bearer tokens scoped per principal;
  see [auth.md](auth.md)).

If you want to prevent a developer from deploying to production, the
right approach is to not give them the credentials -- not to rely on
sparkwing to block them.

## When to choose which mode

| Mode | Where it runs | Speed | When to use |
|------|--------------|-------|-------------|
| `sparkwing run <pipeline>` | Your laptop | Fast (local caches) | Day-to-day development, fast iteration, local-only deploys |
| `sparkwing run <pipeline> --profile prof` | Your laptop | Fast | Local execution that records state to a shared profile's backend |
| `sparkwing runs submit <pipeline>` | Your laptop, detached | Fast | Work that must outlive the terminal: long deploys over ssh, hand-kicked jobs, agents that must not block |
| `sparkwing pipeline trigger <pipeline> --profile prof` | Cluster | Medium (remote build) | Production deploys, deploys requiring cluster credentials, parity with webhook flow |
| Git push -> webhook | Cluster | Medium | Automated CI/CD on every commit |

## More than one sparkwing on one machine

A machine can end up with more than one sparkwing binary. `go install`
drops one in `$GOBIN`, the source installer builds one into
`~/.local/bin`, a package manager may leave a third in `/usr/local/bin`.
Each is a complete sparkwing.

That only matters when they disagree, and they eventually do. PATH is
not one list: an interactive shell orders it from your shell profile,
while a launchd job, a systemd unit, or a cron entry carries whatever
PATH its own configuration sets. The two can resolve `sparkwing` to
different builds, so the same command resolves to a different program
depending on how it was launched.

Sparkwing handles this by keeping the copies from corrupting each
other's state and by telling you they exist -- it never picks a winner
or touches a binary it did not install.

### Version memory is per install

Each install records the version it last ran as in its own file under
`~/.sparkwing/last-version.d/`, keyed by a digest of the binary's
resolved path (so a symlink or a `/tmp` vs `/private/tmp` alias is one
install, not two). The upgrade notice -- the one-line changelog pointer
printed after a binary changes -- compares each install only against
what it, itself, last ran as. Two copies taking turns can no longer
rewrite a shared record into upgrades and downgrades that never
happened. A separate `SPARKWING_HOME` keeps separate stamps.

### Finding a split install

Every surface that knows about installs reports them, read-only:

- `sparkwing doctor` scans your PATH *and* the well-known install
  directories (`~/.local/bin`, `$GOBIN`, `$GOPATH/bin`,
  `/usr/local/bin`, `/opt/homebrew/bin`) -- a scan that trusted only the
  caller's PATH would report a clean machine from the very shell whose
  neighbor is the conflict. Copies reachable through a symlink collapse
  into one entry. The finding makes a sweep unclean, and each copy is
  printed with the exact reversible `mv` that retires it.
- `sparkwing info` reports the running binary's resolved path and any
  other installs (`executable.path` / `executable.other_installs` in
  `-o json`), and `sparkwing info --for-agent` includes the same
  identity so an agent knows which build its evidence came from.
- `sparkwing update` names any other copies after installing. Release lookup,
  signature, digest, or installation failure is terminal; it never selects an
  unsigned `go install` fallback.
- `bin/install.sh` (the source installer) reports copies outside its
  destination and never modifies anything outside `$DEST`.

### Fixing it

Pick one:

- **Keep one copy.** On Unix, retire the others with the printed guarded
  `mv -n`; it refuses when `<path>.superseded` already exists, and its printed
  undo likewise refuses if the original path has been recreated. Paths with
  whitespace or shell metacharacters are quoted as one shell word. On Windows,
  Sparkwing prints both exact paths and asks you to rename with File Explorer
  or a command quoted for the shell you chose. It does not pretend one command
  can safely quote every legal filename in both cmd.exe and PowerShell. Nothing
  in Sparkwing runs the remedy for you, because it cannot know which copy you
  meant to keep.
- **Keep both, fix the caller.** Point the launchd plist, systemd unit,
  or cron entry at an absolute path rather than at a bare `sparkwing`
  on a PATH you do not control. This is the right fix when the two
  copies are deliberate.

Nothing here ever deletes or renames a binary.

## Per-host concurrency

Two `sparkwing run` invocations on the same machine compete for the same
CPU. Local runs are arbitrated by a per-host admission daemon
(`wingd`) -- invisible infrastructure you never install, start, or
tune. The first run that needs admission brings one up: a lock file under
the sparkwing home makes the race safe, so one process wins and the rest
connect to the winner. The daemon exits on its own once the machine has
been idle for a while, coming back the next time a run needs it.

### Who hosts the daemon

**The installed Sparkwing distribution owns daemon lifecycle. Pipeline
clients declare required capabilities and use the running daemon; they
never host, replace, or upgrade it.**

The daemon is always an installed sparkwing build -- the CLI, or
`sparkwing-runner` on a runner box -- and never a per-repo pipeline
binary. `sparkwing run` hands each run the exact CLI that launched it as
the daemon host, through `SPARKWING_WINGD_BIN`; a pipeline binary invoked
directly falls back to the `sparkwing` on PATH. You can export
`SPARKWING_WINGD_BIN` yourself to point a directly-invoked pipeline
binary (a systemd unit, a deploy box) at a sparkwing that is not on PATH,
and `sparkwing run` will not overwrite a value you set.

A newer *installed* sparkwing transparently replaces a running older
daemon. Pipeline binaries never do, so one repo bumping its `.sparkwing/`
SDK pin cannot churn the daemon every other repo on the box shares.

The daemon serves two sockets in the private directory it owns, both mode
0600, and refuses any connection whose peer uid is not its own. `d.sock`
carries admission. `api.sock` serves the controller HTTP API over the
runs-store handle the daemon holds, so a run it hosts can read and write
run, node, event, and concurrency state without opening the store file
itself; that is what removes the store's schema from a pipeline binary's
contract. Only the process holding the election lock binds either socket,
and a daemon being replaced closes `api.sock` before it acknowledges the
drain, so the successor binds it with no overlap.

`api.sock` can fail to bind while `d.sock` is serving: the socket path is
too long for the OS, the private directory is not owned by this user or is
not mode 0700, or the filesystem refuses the socket. The daemon keeps
arbitrating admission in that state rather than leaving the machine with no
daemon, and says so: `sparkwing daemon status` reports `api_ready: false`
with the bind failure in `api_error`, `sparkwing doctor` warns, and both
name the path as `api_socket`. A cache URL the daemon cannot open is the
same shape of fault: the daemon serves without artifact routes and reports
`artifact_store_error`.

On `api.sock` the peer uid *is* the identity. A request with no
`Authorization` header is served as an admin principal named
`unix-peer:<uid>`, which is every route: every run's state regardless of
which process claimed it, every stored secret, and the ability to mint
bearer tokens that outlive the process. That is the same authority a
same-uid process already had by opening `state.db` directly, and it is why
the socket is 0600 with a peer-credential check on accept and why the
daemon serves exactly one account. A local run sends no token and is
served as that peer principal; that is also the faster path, because a
bearer token is looked up on the writing handle and waits behind whatever
it is doing. A request that does carry a bearer token is authenticated
against the store's tokens instead, so a stale token fails closed rather
than falling back to the uid.

`GET /api/v1/health` is answered by the daemon rather than by a controller,
so it reports on a home that has no runs store. Alongside the usual
`status` and `auth` it carries `store`: `absent` when this home has no
state file (a machine running only object-store profiles, which is healthy
and stays that way, because a probe never creates the file), `ready` when
the daemon can read it, and `error: <reason>` with a 503 when a store that
exists will not open.

### Running with no daemon available

A run the admission daemon cannot serve runs **standalone**: against its
own runs store, saying so once on stderr before its first node, with the
exit code it would have had. Five cases reach it.

The block prints once admission has answered, not when the store is
chosen, so a run that is refused never reads a paragraph ending
"everything else works" immediately above its own failure. A run refused
by admission also leaves no standalone store behind: if it was the run
that created the file, the file is removed again. That is the only ending
that discards anything -- a run that fails later, while shaping its plan
or resolving a secret, has already written its row and keeps both the row
and the block. Every standalone run holds a shared lock on
`standalone/state.lock` for the life of its handle, and a discard runs
only when it can take that lock exclusively, so two runs starting together
cannot delete each other's store.

**No daemon is running and none can be started** -- no sparkwing
installed to host one. The run says:

```
sparkwing: no admission daemon is running and no sparkwing is installed to host one, so this run is standalone. It cannot see other runs on this machine and they cannot see it, so together they may oversubscribe it. Everything else works.

  to host one
    curl -fsSL https://sparkwing.dev/install.sh | sh
```

**The daemon predates something the run needs** -- it never advertised an
`api.sock` at all, it answers 404 on a route this pipeline's SDK uses, or
it reports a runs store its own binary is too old to open, which is what
an installed daemon behind a newer pin that already migrated the file
looks like. Pipeline binaries never replace a daemon, so the remedy is to
update the daemon, and the block names `sparkwing update`. A daemon that
is behind *and* reported a reason of its own -- an `api.sock` it could not
bind, say -- carries that reason on its own line in the block, because
updating is not what fixes a socket path over the OS limit. Only a
release-to-release gap counts as behind: a local build of the same release
carries a describe suffix that sorts below the tag and is not treated as
older.

**The daemon's protocol floor is above this pipeline** -- a release
declared a cut and this repo's pin predates it. The block names
`sparkwing repos update --apply`, which ends the warning period.

**The daemon cannot serve this run's state** -- it advertised `api.sock`
and the socket did not bind, or it will not answer. This is a fault of a
daemon that is not behind this pipeline, so the block carries the
daemon's own reason and points at `sparkwing daemon status` rather than
at an upgrade.

**`SPARKWING_ALLOW_UNADMITTED=1` is set** -- the operator asked for the
direct path, for a box whose other work they know. The block says so and
names `unset SPARKWING_ALLOW_UNADMITTED`; it is read strictly, so only
the exact value `1` turns the check off. The variable is an environment
variable rather than a flag because the runs that need it are the ones no
CLI launched.

A pipeline that reserves host capacity with a plan-level or node-level
`.Resources()` pin is not an exception. It runs standalone rather than
failing, because a commit hook is not the place to fail for a reservation
nothing else on the box is honoring either. What every standalone run
loses is the same thing: host CPU and memory are not arbitrated, so a
standalone run and a hosted one may oversubscribe the box. The read verbs
still find it. `sparkwing runs list`, `jobs`, `runs find`, and
`runs failures` merge this home's own `state.db` with every standalone
store and mark each row with the store it came from; `runs status`,
`runs get`, `runs receipt`, `runs summary`, and `runs timeline` look an id
up in the shared store first and then in each standalone store. The
dashboard reads `state.db` alone and does not see the run.

Some failures are still failures. A daemon whose runs store is unreadable
for a reason that is not age -- a disk error, a file that is not a
database, permissions -- fails the run, because the file is what the
operator must fix and running standalone would hide it behind a run that
looked like it worked. So does a daemon that is present but never
answers, one whose build does not match this pipeline's, and a version
conflict that repeated takeover could not settle: each is a machine to
look at rather than a version gap to route around. A daemon merely too
old for its store is not one of these: the daemon reports the two apart
(`store: skew: ...` against `store: error: ...` on its health endpoint,
`daemon_store_skew` in `sparkwing daemon status`), because a message an
operator reads cannot be told apart by a program.

A degrade that happens after the store is already chosen -- a lease frame
the daemon does not serve, on a run whose state is already with that
daemon -- cannot move stores, so it prints a single line naming the cause
and saying where the run's state stays, instead of one of the blocks
above.

One in-body feature changes shape on the standalone path.
`sparkwing.ToolSlot(ctx, group)` -- the budget a job body takes out
around a tool that manages its own parallelism -- returns
`granted=false`, its documented fallback, and the body uses whatever
private serialization the tool ships with. Job bodies already have to
handle that return.

Dry runs (`--sw-dry-run`) mutate nothing, so a dry run that would go
standalone writes to a throwaway store that is removed when it ends: it
never creates this home's standalone store and never adds rows doctor
counts against it.

#### Where standalone runs live

`~/.sparkwing/standalone/state.db`. Binaries share that one file under the
store's requirements rule: the newest one migrates it, and an older one
keeps opening it as long as it understands every requirement the file
records. A binary that file refuses -- because it records a requirement
that binary does not know -- falls back to
`~/.sparkwing/standalone/schema-<N>/state.db`, where `N` is its own
expected schema. The shared `~/.sparkwing/state.db` is not opened by a
pipeline binary at all.

Sharing one file is what keeps the block's "everything else works" true
across repos: box- and run-scoped `.Concurrency()` groups are enforced
through the store, so two standalone runs at neighboring pins still
serialize against each other.

A child run a standalone run dispatches, and a node replayed from one,
land in the store their parent chose rather than deriving one of their
own: the parent names the file in `SPARKWING_STATE_DB` and its reason in
`SPARKWING_STANDALONE_REASON`, and the child prints no second block. A
child that does reach the daemon ignores both and records nothing about
being standalone.

Only a parent may set those two. They are denied from a submitted run's
captured environment and stripped from what the dashboard's trigger
consumer hands a child, so a value left in a submitting or dashboard shell
cannot send an unrelated run into someone else's store. The consumer
claims from the shared store and passes neither, so a child it dispatches
opens the shared store, which is where its trigger row lives.

`sparkwing doctor` lists each standalone store that exists with its run
count and the oldest run's age. Nothing prunes them: delete a file once
you no longer want the runs in it.

A read verb opens every standalone store read-only, so reporting on one
never migrates it, and lists what it finds newest first. An id that is in
both the shared store and a standalone one lists once, from the shared
store, whatever the two copies say about when they started, which is the
store the single-id verbs resolve first.

A standalone store this build cannot read is named on stderr after the
table instead of listed. One this sparkwing is too old to open -- it records a schema
requirement this build does not know -- is named with the release that
can open it. One written at an older store schema, which is what the
`schema-<N>` directories hold, is named with its run count
(`standalone/schema-20/state.db holds 3 runs written by an older
sparkwing; read them with that release`), because this build's queries
ask for columns that store does not have. One that is busy is named as
busy and read again next time, and one that is not a runs store this
build can read is named as that. No note carries a database error.

A verb that writes prints the same notes when the id it was given is in
no store it could read, so a run in a skipped store is not reported
simply missing.

`sparkwing runs bounce`, `runs annotations add`, `runs approvals approve`,
`runs approvals deny`, `debug rerun`, and `debug replay` write to the
store that holds the run, so they act on a standalone run in its own
store. They open that store read-write only when catching it up to this
build's schema would stamp no requirement it does not already list; when
it would, they refuse and name the requirement, because stamping it is
what puts the file out of reach of the pipeline binary that owns it.

`runs cancel` and `runs retry` cannot act on a standalone run at all, and
say so rather than reporting it missing. Cancel needs something arbitrating the run,
and a standalone run is by definition one no daemon arbitrates, so
nothing is watching its store for a cancel request; stop the process or
wait. Retry submits a new run, which needs a daemon or a controller to
admit it; start the pipeline again from its repository.

The start record of a standalone run carries `standalone: true` and
`standalone_reason` (`no-daemon`, `daemon-older`, `daemon-fault`,
`floor`, or `forced`), and `sparkwing runs status` shows both along with
the store the run was read from. To end the standalone state, fix what the
block names -- install or update sparkwing, raise the repo's pin, or
unset the variable -- and the next run is hosted again. Runs already
written to a standalone store stay there.

These rules are evaluated once, at run start. A daemon that dies
*during* a run is a different path: the run's client reconnects and
reattaches its lease, spawning a replacement through the resolved host
binary if one exists, and if none does the next admission the run needs
fails loudly rather than silently continuing unarbitrated.

The process connects when it needs admission. Explicit run resources and
plan-level `.Concurrency()` groups are admitted at run start and held by
the open connection for the run's lifetime. Unpinned host CPU and memory
are admitted per node as the DAG dispatches, so a fast early node can run
while a later heavy node waits for capacity. While work waits it prints a
single queue-position line on stderr (`queued for local admission:
position 2 of 3 ...`) and Ctrl-C cancels the wait cleanly. When a run
process dies -- crash, kill, or power event -- the kernel closes the
connection and the daemon releases the lease immediately, finalizes the
orphaned run record, and admits the next waiter. There are no heartbeats,
leases to tune, or polling loops. Nested runs never double-charge the
host: a parent passes its active lease to children it spawns (via
`RunAndAwait` or a step that shells out to `sparkwing run`), and each
child attaches to the parent's lease instead of re-admitting.

The ledger survives daemon restarts the same way runs survive daemon
handoffs: every transition is persisted, and a restarting daemon
restores the ledger and holds a short window for clients to reclaim
their leases before releasing the unclaimed rest. Restored unguarded
grants the current budget cannot hold are shed. A file that cannot be
parsed may describe guarded commands, so the daemon refuses admission
instead of guessing it is safe to release them. The startup error names
the file and the explicit recovery command. After verifying those
commands have stopped, `sparkwing daemon recover-state --yes` preserves
the bytes as `state.json.corrupt-<time>` for `sparkwing doctor` to report
and allows the next daemon to start cleanly.

While it serves, the daemon holds one open handle on the runs store
instead of reopening `state.db` for every check, and it reaps that store
through the same handle: lapsed concurrency holders and the waiters
behind them every 10 seconds, and runs whose process died without
finishing them once at start. It opens the store it finds and never
creates one, so a machine whose runs all keep their state in an object
store still has no local database. A store the daemon cannot open does
not stop admission. The run is evicted naming the store's own reason, as
before, `sparkwing daemon status` reports `daemon_store_ready` false with
that reason and a remedy, and the open is retried: immediately while no
store file exists, and every 30 seconds once one does, so a store that
appears or becomes readable is picked up without a restart. A store file
that is deleted or replaced is noticed and reopened rather than read as a
vanished inode. Reads on the admission path use a second, read-only handle
on the same file, so a reaper pass or a finalize cannot hold up the
terminal check, and every store call the daemon makes carries a deadline.
The handles close when the daemon exits or idles out.

### Declare nothing; sparkwing measures

The daemon measures the machine's real cores and memory and admits into
the headroom that is actually free, counting non-sparkwing load against
capacity. It also measures each pipeline and node over their first few
runs. Explicit run resources use the pipeline profile. Unpinned local
work uses the node profile at dispatch, so "one heavy build at a time"
emerges from measurement with no configuration. Declare nothing and it
works.

`sparkwing runs stats --capacity` shows what was learned: duration
percentiles, CPU and memory distributions (p50/p95/peak across recent
runs), and queue-wait p50/p99. The distributions tell you whether work
is steady or spiky and whether the box is too small. Admission charges
cores from the p95 across recent runs of each run's *sustained* demand
-- the level that covers four sampling ticks in five, never below its
average draw, and not the peak it touched once -- while memory still
charges the p95 of the per-run peaks. The dimensions differ because the
resources do: cores are compressible, so the kernel time-slices two
runs that collide for a tick and reserving a burst peak for a whole
hold only refuses work the box could have run, while an oversubscribed
box does not time-slice memory, it OOMs. It is the split admission
already makes when it gates: CPU pressure is backpressure, memory is
strict. Either way the charge takes p95 rather than the maximum, so one
freak run cannot pin the price until it ages out of the window. The
`CPU CHARGE` column reports the resulting core figure, and a blocked
waiter names its provenance, as in `needs 5.0 cores (measured sustained
p95 over 12 runs); 2.1 available`.

The dashboard's **Capacity** page is the same accounting with its work
shown: the live host ledger with the subtraction behind each available
figure, the priced table above, and, per pipeline, the stored sample
window with the run each percentile charge was ranked out of marked. It
is where to look when a charge seems wrong, because it puts the price and
the evidence for it on one screen -- see
[observability.md](observability.md#dashboard).

A pipeline may pass a cold-start hint with
`.Resources(sparkwing.Cores(n), sparkwing.MemoryGB(n))`, and may pin an
explicit cost when it must -- but a pin is policed, not trusted blindly:
when it drifts from what the pipeline actually uses, `sparkwing queue`
flags the gap so the pin can be corrected or dropped. The posture is
declare nothing and let sparkwing measure; pin sparingly, and sparkwing
polices the pin.

The same measurements answer the murkiest recurring question on a shared
box: is sparkwing slow, or is the machine busy? A holder is flagged
`(contended)` only when three things line up at once -- its elapsed time
has run well past its own measured p99, the host has been saturated by
non-sparkwing load for a sustained share of the run, and it has enough
duration samples to have a trustworthy baseline. An unprofiled run, a run
that is merely at the slow end of its own distribution, and a run on an
idle host are all left unflagged. When a contended run finishes it prints
a one-line attribution (`took 12m vs p50 8m30s; host saturated 62% of the
run`), and `sparkwing runs stats --capacity` shows each pipeline's
contended share, so "the tool is slow" becomes a measurement instead of a
guess. Detection is observability only; it never changes an admission
decision.

`.Concurrency(group)` is for *logical* mutual exclusion only -- a deploy
lock, a shared fixture -- never host sizing. A run- or box-scoped group
is local to the machine; a global-scoped group pools across the whole
fleet through the controller's shared state (see [sdk.md](sdk.md)).

### Recovering from bad measurements

Measurement drives admission, so a wrong reading needs an escape hatch
that does not mean "wait for the window to age out." There are two:

- **A host sensor that cannot read.** The `EXTERNAL` column carries the
  reading the availability math ran on, and the view prints how old that
  effective reading is and how recently the host sensor successfully read
  at least one pressure dimension. The deadband absorbs small wiggles, but
  reapplies its newest effective value within 30 seconds, so a recovered
  reading near an admission threshold cannot strand a waiter indefinitely.
  A failed sample updates neither timestamp and cannot apply its returned values.
  When a dimension cannot be read at all -- macOS with no
  `kern.memorystatus_level`, a platform with no host-pressure sensor -- the
  cell prints `unmeasured` rather than a figure, an `external: unmeasured on
  <dimension> (host sensor unavailable); no external load subtracted from
  available` line says what admission did about it, and the JSON row carries
  `"external_source": "unmeasured"`. A sampler response with no measured
  pressure dimension does not refresh the measurement age. Nothing is
  subtracted for a dimension nobody read, because a substituted figure in a
  measurement's format is what pinned memory headroom at zero on every box.
- **A misreading host sensor.** If the external-load reading is wrong and
  admission is queuing runs against phantom pressure, add `ignore-external`
  to the machine budget. Admission then plans against total capacity minus
  the reserve, subtracting no external load. The `EXTERNAL` column in
  `sparkwing queue` still shows the real reading -- observability stays
  truthful -- with an `external: ignored (operator setting, ...)` line that
  names which setting turned it on, and contention detection keeps using
  the real saturation. Use it alone (`ignore-external`) or alongside a cap
  (`50%,ignore-external`). Put it in the budget config file rather than the
  environment when you want it to outlive the daemon running now: the
  daemon is started on demand by whichever run needs it first and inherits
  that process's environment, so an exported variable lasts only as long as
  that one daemon. `sparkwing doctor` reports a machine admitting with
  external load ignored, so the state is findable without suspecting it
  first.
- **A poisoned learned profile.** One freak run can record an absurd peak
  that inflates a pipeline's charge for the rest of the window, and
  sustained external load can ratchet a still-measuring pipeline's demand
  floor upward. The floor self-corrects: each contended run that measures
  below it halves it, mirroring the ceiling-hit doubling that raised it.
  That correction only works if the run is admitted, so two rules keep it
  reachable. A charge resolved from measurement is capped at the machine's
  grantable ceiling on **both** cores and memory, and the run says so,
  naming the profile; `sparkwing doctor` flags the same state. And with
  nothing admitted, the queue head is granted on either dimension no matter
  what external load is reading, so a pipeline can always measure its way
  back down instead of being locked out by a floor it can never disprove.
  To clear a floor immediately anyway, reset with
  `sparkwing runs stats --reset --pipeline <name>` (profiles are scoped by the
  repository's canonical identity for runs launched inside a git repo and
  shown as `repo/pipeline`, exactly as `runs stats --capacity` prints them; a
  bare pipeline name reaches every repo-scoped key that carries it and the
  summary names each one): the
  learned samples, peaks, floors, waits, and contention tally are dropped
  so the pipeline re-learns from a cold start, and the command prints what
  it removed -- including a floor with no measured samples behind it, which
  is the state a pipeline that never finished a clean run is priced off. An
  explicit `.Resources()` pin is preserved -- admission keeps charging the
  pin while the profile re-learns. To reset every pipeline at once, use
  `sparkwing runs stats --reset --all --yes`.

A request that no release could ever satisfy -- a pin larger than the
machine -- is refused at submit with the arithmetic (`needs 12GiB of
memory, this machine has 8GiB`) rather than queued to a timeout. A box
that is merely busy still queues, because a holder finishing fixes that.

### Operating it

Day-to-day operation runs through two commands, and neither can hurt the
machine:

- `sparkwing queue` -- the truthful view of local admission: every
  resource holder with the repo it came from, how long it has held, and its
  cost; connected run registrations that hold no resources, labeled separately;
  every waiter in admission order with its position, priority, estimated start,
  and the resources that actually hold it under the ledger's liveness rules,
  plus a health flag on any holder that is not running cleanly:
  `(stalled)` for one that is alive but idle while runs wait behind it,
  and `(contended)` for one that is measurably slower than its profile
  while the host is saturated. A child run riding its parent's lease
  renders indented under that parent. The header summarizes the last day
  of admission outcomes in one line -- runs granted, median wait,
  evictions by key, queue timeouts, how many runs were contended, and how many
  younger backfills activated waiter protection -- so a chronic pattern shows
  up before it becomes an incident. It also
  names the serving daemon's version and uptime, and warns when an
  older-pinned pipeline binary is admitting outside the daemon. Every
  view states whether the daemon was reached: an idle machine and a
  socket that would not answer are different answers, and the second
  exits 4 with the dial failure named rather than printing an empty
  queue it never looked at.
- `sparkwing doctor` -- the one repair verb. It tightens permissive local-home
  paths and removes only provably-dead state (an interrupted run's leftover
  row, an orphaned lock file whose owner is gone), then reports what it found
  and did. Every report
  opens with the daemon's state -- serving with its version and protocol,
  none running, or unreachable -- because the checks below it only run
  when the daemon answered, so their emptiness means nothing on its own.
  An unreachable daemon is never a clean bill, and the run-row repair is
  skipped there rather than risk finalizing a run that daemon is holding.
  Standing problems it cannot safely repair -- repeated admission
  rejections, a daemon version skew, a contention-poisoned capacity
  profile, a daemon serving another sparkwing home at a version no
  release carries -- are reported with the exact fix instead. It never
  kills a process and never touches live admission, so it is safe to run
  at any time; on a healthy machine it finds nothing and says so.

Each sparkwing home keeps its own daemon, so a daemon for one home is
invisible from another even though both show up side by side in a process
listing. That makes a scratch daemon -- one a build with a local `replace`
directive left running, which reports version `v0.0.0` -- easy to mistake
for the machine's resident one and read its log as production state.
`sparkwing doctor` names any it finds, with the socket it answers on, so
the mistake is caught before it explains an unrelated failure.

The daemon writes an operational log to `wingd/d.log` under the sparkwing
home (`~/.sparkwing/wingd/d.log` by default) for when you want to see
what it did.

If a daemon is ever busy in a way its log does not explain -- burning CPU
with nothing queued, or answering nothing -- send it `SIGUSR1`
(`kill -USR1 <pid>`, POSIX only). It appends a line counting its
connections, holders, waiters, leases, and guards, followed by a stack
for every goroutine, to that same log. The daemon keeps running, so you
can capture the state before deciding whether to stop it, and the dump is
what a bug report about a stuck or spinning daemon should carry. Each
dump adds up to 2MB to `d.log`. The log is rotated once to `d.log.1`
when it passes 1MB -- both when a daemon starts and before a dump is
written -- so a long-lived daemon you ask for many dumps keeps the pair
bounded rather than growing one file forever. The rotation copies the
log aside and empties it in place rather than renaming it, so the
daemon, the supervisor watching it, and anything else already writing
to `d.log` all keep writing to `d.log`. Only the previous stretch is
kept, so copy a dump you care about out of `d.log` before asking for
several more.

### Capping sparkwing's share of the machine

Measured admission is the primary mechanism, and for most machines it is
the only one you need. When you want a hard ceiling -- "CI may use at most
half my laptop" -- set one machine budget. It takes a core count, a
percentage, or both a core and a memory term, plus optional `enforce` and
`ignore-external` terms:

```
6               # at most 6 cores
50%             # at most half the machine's cores
6,8gb           # 6 cores and 8 GiB
50%,enforce     # half the cores, hardened at the OS level
ignore-external # admit against total capacity, ignoring external load
```

#### Where to set it

The same value is read from several settings, resolved in the order the
rest of the wing family uses -- the more specific setting wins:

| Setting | Reaches | Lives until |
|---|---|---|
| Internal daemon `--budget` argument (not a public CLI command) | that daemon process | that daemon exits |
| `SPARKWING_BUDGET` in the environment | any daemon spawned from that environment | that daemon exits |
| `~/.config/sparkwing/budget` (or `$XDG_CONFIG_HOME/sparkwing/budget`) | every daemon on the machine | you edit or delete the file |

The config file is the durable one, and it is the setting to reach for
when you mean "this machine, from now on". The admission daemon is
started on demand by whichever run needs it first, inheriting that
process's environment, so a budget exported in one shell applies to
whatever daemon that shell happened to spawn and disappears with it. The
file is read at daemon startup, so a budget written there is in force
again the moment a daemon respawns.

The file holds one setting line. Blank lines and `#` comments are
skipped, so the reason a budget is in force can live next to the budget:

```
# host sensor over-reads external load on this box
50%,ignore-external
```

A value that will not parse fails daemon startup rather than being
dropped, whichever setting it came from. A budget that silently does
nothing is worse than one that says it is wrong.

#### Seeing which one is in force

The budget caps the admission ledger below the machine total, so it holds
everywhere admission already runs, with no other change to how runs are
scheduled. `sparkwing queue` shows it as its own row in the headroom
arithmetic, naming the setting behind it
(`budget 6.0 cores (machine 10.0) (from config ~/.config/sparkwing/budget)`),
so an operator can revoke a cap they did not set themselves. With no
budget set anywhere the row says so rather than staying silent, because a
machine admitting against everything it has looks exactly like a
deliberate whole-machine budget from the outside. `sparkwing doctor`
reports a non-default budget too, and says out loud when the one in force
came from an environment that the next respawn may not carry.

A requested cap above the machine total is clamped to the machine, and the
daemon logs a one-line note when it does.

### Containers: the daemon respects its own cgroup

You do not have to set a budget to keep sparkwing inside a container. On
Linux the daemon reads its own cgroup v2 limits at startup (`cpu.max` and
`memory.max`, with a cgroup v1 fallback) and clamps capacity to them, so a
6 GiB container on a 24 GiB host plans against 6 GiB, never the host it
sits on. External-load sensing follows suit, measuring the container's own
CPU and memory usage rather than the machine's. `sparkwing queue` shows the
clamp as a `container limit: 6.0 cores (host 24.0), 6.0 GiB memory (host 24.0 GiB)`
row, and a machine budget still caps below the detected limit. macOS has
no cgroups and so no container path -- capacity there is always the host.

Add `enforce` to harden the cap at the operating-system level as well as
in admission:

- **Linux** places admitted run processes in a daemon-managed cgroup v2
  with `cpu.max` and `memory.max` matching the budget, a kernel wall. When
  the cgroup filesystem is absent or unwritable (an unprivileged laptop),
  the daemon logs a note and the admission cap still applies.
- **macOS** has no cgroups, so it demotes admitted runs to background QoS
  (the `taskpolicy -b` equivalent: efficiency-core scheduling and
  throttled I/O) and raises their scheduler nice. This is advisory
  scheduling that yields to foreground work, not a hard cap.

This is the one machine-level knob. It complements measured admission; it
does not replace it.

### Whoever owns the machine owns admission

The gate is host-local by design: two laptops pointed at the same shared
backend (Mode 2 / 3 / 4) each run their own daemon, and nothing
coordinates raw CPU across machines. On a Kubernetes runner the pod's CPU
is already bounded by the kube scheduler and the warm-runner pool's own
budget, so admission there belongs to the cluster, not to a sparkwing
daemon -- runner pods do not start one. Cross-machine coordination is the
job of global-scope `.Concurrency()` groups, which pool through the
controller's shared state.

## Pipeline configuration

Local vs remote is decided at invocation time (`sparkwing run` for here,
`sparkwing pipeline trigger` for the cluster), not declared per-pipeline.
Pipelines themselves only declare *triggers*:

```yaml
# .sparkwing/sparkwing.yaml
pipelines:
  - name: build-test-deploy
    entrypoint: BuildTestDeploy
    description: Build, test, and deploy
    on:
      push:
        branches: [main]
```

If a pipeline is locally-runnable (most are), `sparkwing run build-test-deploy`
just works. If a step needs cluster credentials it cannot reach from a
laptop, the pipeline author either dispatches the whole run remotely with
`sparkwing pipeline trigger`, or splits the deploy into a sub-pipeline that
runs on the cluster (`RunAndAwait`, reading typed output via `Ref[T]`; see
[sdk.md](sdk.md)).
