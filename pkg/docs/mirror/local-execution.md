# Local Execution

Sparkwing pipelines run anywhere -- on a Kubernetes cluster, on your
laptop, or both. This is a core design advantage: your CI/CD is not a
black box in the cloud, it is a portable program you can run yourself.

## Why local execution matters

Most CI systems only run inside their own infrastructure. If GitHub
Actions is down you can't deploy; if your Jenkins server crashes,
builds stop. Your ability to ship depends on their uptime.

Sparkwing pipelines are Go programs. You can run them on any machine
with Docker installed. This means:

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
  3. sparkwing run records the run to ~/.sparkwing/
     (SQLite + per-run log files)
```

Your laptop runs the pipeline directly. No sparkwing controller is
involved. Each invocation writes its outcome to the local SQLite store
under `~/.sparkwing/`, which is what `sparkwing dashboard start` reads.
Run `sparkwing dashboard start` once and leave it up to watch
concurrent runs in a browser without needing any remote service.

When you run locally against a remote profile (`sparkwing run X --profile
prod`), the run dual-writes state to both the profile's backend and the
local SQLite store. The remote is canonical; the local copy is a free
byproduct, so `sparkwing runs list` on your laptop sees the run afterward
even with no network. Set `mirror_local: false` on a profile to skip the
local copy for automated workers that fire off many runs.

See [native-mode.md](native-mode.md) for the full local-mode design.

### Remote execution

```
Your laptop:
  1. sparkwing pipeline trigger tarballs .sparkwing/ + working tree
     (incremental sync)
  2. sparkwing POSTs the upload + a trigger to the profile's controller

Cluster:
  3. Controller dispatches a runner Job
  4. Runner clones the upload, compiles, runs the pipeline
  5. Your laptop streams logs back via the logs service
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

## Authorization model

Sparkwing intentionally does **not** try to be a permissions boundary
between developers and infrastructure. Authorization is enforced where
it actually lives: the registry, the gitops repo, kubectl. A
developer with ECR push and gitops write access can deploy with or
without sparkwing.

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
| `sparkwing pipeline trigger <pipeline> --profile prof` | Cluster | Medium (remote build) | Production deploys, deploys requiring cluster credentials, parity with webhook flow |
| Git push -> webhook | Cluster | Medium | Automated CI/CD on every commit |

## Per-host concurrency

Two `sparkwing run` invocations on the same machine compete for the same
CPU. Local runs are arbitrated by a per-host admission daemon
(`sparkwingd`) -- invisible infrastructure you never install, start, or
tune. The first run that needs admission elects one: a lock file under
the sparkwing home makes the race safe, so one process wins and the rest
connect to the winner. A newer sparkwing binary transparently takes over
from a running older daemon, and the daemon exits on its own once the
machine has been idle for a while, coming back the next time a run needs
it.

At run start the process connects and submits one admission request
covering everything the run needs: host CPU and memory plus any logical
`.Concurrency()` groups the plan claims. The grant is all-or-nothing,
and the lease is held by the open connection for the run's lifetime.
While a run waits it prints a single queue-position line on stderr
(`queued for local admission: position 2 of 3 ...`) and Ctrl-C cancels
the wait cleanly. When a run process dies -- crash, kill, or power event
-- the kernel closes the connection and the daemon releases the lease
immediately, finalizes the orphaned run record, and admits the next
waiter. There are no heartbeats, leases to tune, or polling loops.
Nested runs never double-charge the host: a parent passes its lease to
children it spawns (via `RunAndAwait` or a step that shells out to
`sparkwing run`), and each child attaches to the parent's lease instead
of re-admitting.

### Declare nothing; sparkwing measures

The daemon measures the machine's real cores and memory and admits into
the headroom that is actually free, counting non-sparkwing load against
capacity. It also measures each pipeline's own cost over its first few
runs, so "one heavy build at a time" emerges from measurement with no
configuration. Declare nothing and it works.

`sparkwing runs stats --capacity` shows what was learned: each
pipeline's duration percentiles, its CPU and memory distributions
(p50/p95/peak across recent runs), and its queue-wait p50/p99. The
distributions tell you whether a pipeline is steady or spiky and whether
the box is too small; admission always charges the measured peak, never
a percentile, because under-reserving a spiky pipeline recreates exactly
the oversubscription the daemon exists to prevent. Percentiles inform,
peak admits.

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

- **A misreading host sensor.** If the external-load reading is wrong and
  admission is queuing runs against phantom pressure, add `ignore-external`
  to `SPARKWING_BUDGET`. Admission then plans against total capacity minus
  the reserve, subtracting no external load. The `EXTERNAL` column in
  `sparkwing queue` still shows the real reading -- observability stays
  truthful -- with an `external: ignored (operator setting)` line stating
  that admission is not acting on it, and contention detection keeps using
  the real saturation. Use it alone (`SPARKWING_BUDGET=ignore-external`) or
  alongside a cap (`SPARKWING_BUDGET=50%,ignore-external`).
- **A poisoned learned profile.** One freak run can record an absurd peak
  that inflates a pipeline's charge for the rest of the window. Reset it
  with `sparkwing runs stats --reset --pipeline <name>`: the learned
  samples, peaks, waits, and contention tally are dropped so the pipeline
  re-learns from a cold start, and the command prints what it removed. An
  explicit `.Resources()` pin is preserved -- admission keeps charging the
  pin while the profile re-learns. To reset every pipeline at once, use
  `sparkwing runs stats --reset --all --yes`.

### Operating it

There are exactly two operational commands, and neither can hurt the
machine:

- `sparkwing queue` -- the truthful view of local admission: every
  holder with the repo it came from, how long it has held, and its cost,
  every waiter in arrival order with its position and estimated start,
  and a health flag on any holder that is not running cleanly:
  `(stalled)` for one that is alive but idle while runs wait behind it,
  and `(contended)` for one that is measurably slower than its profile
  while the host is saturated. A child run riding its parent's lease
  renders indented under that parent. The header summarizes the last day
  of admission outcomes in one line -- runs granted, median wait,
  evictions by key, queue timeouts, and how many runs were contended --
  so a chronic pattern shows up before it becomes an incident. It also
  names the serving daemon's version and uptime, and warns when an
  older-pinned pipeline binary is admitting outside the daemon.
- `sparkwing doctor` -- the one repair verb. It removes only provably-
  dead state (an interrupted run's leftover row, an orphaned lock file
  whose owner is gone) and reports what it found and did. It never kills
  a process and never touches live admission, so it is safe to run at any
  time; on a healthy machine it finds nothing and says so.

The daemon writes an operational log to `wingd/d.log` under the sparkwing
home (`~/.sparkwing/wingd/d.log` by default) for when you want to see
what it did.

### Capping sparkwing's share of the machine

Measured admission is the primary mechanism, and for most machines it is
the only one you need. When you want a hard ceiling -- "CI may use at most
half my laptop" -- set one machine budget with the `SPARKWING_BUDGET`
environment variable. It takes a core count, a percentage, or both a core
and a memory term, plus optional `enforce` and `ignore-external` terms:

```
SPARKWING_BUDGET=6               # at most 6 cores
SPARKWING_BUDGET=50%             # at most half the machine's cores
SPARKWING_BUDGET=6,8gb           # 6 cores and 8 GiB
SPARKWING_BUDGET=50%,enforce     # half the cores, hardened at the OS level
SPARKWING_BUDGET=ignore-external # admit against total capacity, ignoring external load
```

The budget caps the admission ledger below the machine total, so it holds
everywhere admission already runs, with no other change to how runs are
scheduled. `sparkwing queue` shows it as its own row in the headroom
arithmetic (`budget 6.0 cores (machine 10.0)`) so the constraint is
visible rather than mysterious. A requested cap above the machine total is
clamped to the machine, and the daemon logs a one-line note when it does.

### Containers: the daemon respects its own cgroup

You do not have to set a budget to keep sparkwing inside a container. On
Linux the daemon reads its own cgroup v2 limits at startup (`cpu.max` and
`memory.max`, with a cgroup v1 fallback) and clamps capacity to them, so a
6 GiB container on a 24 GiB host plans against 6 GiB, never the host it
sits on. External-load sensing follows suit, measuring the container's own
CPU and memory usage rather than the machine's. `sparkwing queue` shows the
clamp as a `container limit: 6.0 cores (host 24.0), 6.0 GiB memory (host 24.0 GiB)`
row, and a `SPARKWING_BUDGET` still caps below the detected limit. macOS has
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
