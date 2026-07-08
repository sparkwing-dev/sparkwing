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

Two `sparkwing run` invocations on the same machine compete for the
same CPU. By default the host admits at most `max(1, NumCPU /
workers-per-run)` concurrent runs, so the admitted runs sum to about
`NumCPU` worker goroutines instead of oversubscribing the box. A single
run never blocks on itself. Overlapping runs against a shared local
SQLite backend especially benefit: without the cap, enough overlap
saturates the single SQLite writer until lease heartbeats fail and runs
collapse; the host cap prevents that saturation at the source.

Tune or disable the cap:

- `sparkwing run X --sw-box-slots N` (or `SPARKWING_BOX_SLOTS=N`) admits
  at most `N` concurrent runs per host; extra invocations queue FIFO
  with a periodic `waiting for box slot (N active, max M)` line on
  stderr. Ctrl-C cancels the wait cleanly.
- `sparkwing run X --sw-box-slots off` (or `SPARKWING_BOX_SLOTS=off`)
  disables the cap entirely, restoring uncapped overlap.
- `sparkwing run X --sw-no-wait` fails immediately with `box slots full`
  instead of queueing -- the shape CI runners want when they would
  rather decline overlap than block. With the cap disabled, there is
  nothing to gate.

### Live tuning

The cap is a host control that queued and running runs re-read on each
acquire poll, so you can rebalance concurrency without restarting
in-flight work:

- `sparkwing box-slots show` reports the cap in force, where it came
  from (the live control, the `SPARKWING_BOX_SLOTS` env baseline, or the
  heuristic default), and how many runs currently hold a slot versus
  wait for one.
- `sparkwing box-slots set --to N` raises or lowers the cap live.
  Raising it lets queued runs acquire on their next poll; lowering it
  drains as current holders finish -- running work is never evicted.
  `--to off` disables the semaphore; `--to default` reverts to the
  env/heuristic.

Precedence, highest first: an explicit per-run `--sw-box-slots` pins
that one run above everything else; then the live `box-slots set`
control; then `SPARKWING_BOX_SLOTS`; then the heuristic default. So an
operator can retune the host with `box-slots set` while a run that
deliberately pinned its own cap keeps it.

The gate is host-local. Two laptops pointed at the same shared state
backend (Mode 2 / 3 / 4) each keep their own slot count; nothing
coordinates CPU across machines. Cluster runner pods skip the gate
because their CPU is already capped by Kubernetes and the warm-runner
pool's own concurrency budget.

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
runs on the cluster (`PipelineRef` / `AwaitPipelineJob`; see
[pipelines.md](pipelines.md)).
