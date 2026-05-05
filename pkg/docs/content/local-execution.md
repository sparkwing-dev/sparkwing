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
wing build-deploy

# Run on a cluster -- triggers remote execution via the controller
wing build-deploy --on prod
```

Both run the same pipeline code. The difference is where.

### Local execution

```
Your laptop:
  1. wing compiles the pipeline from .sparkwing/
  2. Pipeline runs whatever its code says (test, build, deploy, etc.)
  3. wing records the run to ~/.sparkwing/
     (SQLite + per-run log files)
```

Your laptop runs the pipeline directly. No sparkwing controller is
involved. Each invocation writes its outcome to the local SQLite store
under `~/.sparkwing/`, which is what `sparkwing dashboard start` reads.
Run `sparkwing dashboard start` once and leave it up to watch
concurrent runs in a browser without needing any remote service.

See [native-mode.md](native-mode.md) for the full local-mode design.

### Remote execution

```
Your laptop:
  1. wing tarballs .sparkwing/ + working tree (incremental sync)
  2. wing POSTs the upload + a trigger to the profile's controller

Cluster:
  3. Controller dispatches a runner Job
  4. Runner clones the upload, compiles, runs the pipeline
  5. Your laptop streams logs back via the logs service
```

The controller is the gatekeeper for prod-side execution: only the
cluster can push to ECR, update gitops, and dispatch warm runners.

## Authorization model

Sparkwing intentionally does **not** try to be a permissions boundary
between developers and infrastructure. Authorization is enforced where
it actually lives: the registry, the gitops repo, kubectl. A
developer with ECR push and gitops write access can deploy with or
without sparkwing.

**What sparkwing controls:**

- Which clusters a pipeline can dispatch to (via `--on PROFILE` and the
  controller's bearer token / scope).
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
| `wing <pipeline>` | Your laptop | Fast (local caches) | Day-to-day development, fast iteration, local-only deploys |
| `wing <pipeline> --on prof` | Cluster | Medium (remote build) | Production deploys, deploys requiring cluster credentials, parity with webhook flow |
| Git push -> webhook | Cluster | Medium | Automated CI/CD on every commit |
| `sparkwing pipeline run --pipeline X --on prof` | Cluster | Medium | Explicit (canonical) form of remote dispatch |

## Pipeline configuration

Local vs remote is decided at invocation time (`--on` or absent), not
declared per-pipeline. Pipelines themselves only declare *triggers*:

```yaml
# .sparkwing/pipelines.yaml
build-test-deploy:
  description: Build, test, and deploy
  on:
    push:
      branches: [main]
  tags: [ci, deploy]
```

If a pipeline is locally-runnable (most are), `wing build-test-deploy`
just works. If a step needs cluster credentials it cannot reach from a
laptop, the pipeline author either uses `--on` to dispatch the whole
run remotely, or splits the deploy into a sub-pipeline that runs on
the cluster (`PipelineRef` / `AwaitPipelineJob`; see
[pipelines.md](pipelines.md)).
