# Deployment

Sparkwing is unopinionated about how your pipelines deploy. It provides
the infrastructure - controller, runners and shared data storage - and your
pipeline code decides what to do with it.

## Run Targets

`sparkwing run` executes locally; `sparkwing pipeline trigger` dispatches
to a cluster via a profile's controller:

| Source | Target | Command |
|--------|--------|---------|
| Local code | Local machine | `sparkwing run build` |
| Local working tree at a git ref | Local machine | `sparkwing run build --sw-ref main` |
| Committed ref at origin | Any cluster | `sparkwing pipeline trigger build --profile dev` |
| Dirty working tree | Remote runner | `sparkwing pipeline trigger build --profile dev --working-tree` |

The `--profile` flag resolves a **profile** - a named cluster endpoint.
Every profile with a controller follows the same dispatch flow:

```
sparkwing pipeline trigger <pipeline> --profile <profile>
  → CLI reads the origin URL + branch/SHA from your checkout and POSTs the trigger
  → CLI requires the commit on an origin-tracking branch
  → controller enqueues the run
  → a runner in the pool claims it on its next poll
  → runner clones the ref and executes the pipeline
```

The default clones the named commit and excludes uncommitted files.
`--working-tree` instead creates a deterministic synthetic commit from tracked
changes and untracked non-ignored files, uploads its Git bundle before trigger
admission, and runs that exact snapshot without pushing it to the origin. The
capture rejects conflicts, submodules, sparse checkouts, and Git content
filters. It also requires a complete SHA-1 repository. The default requires an `origin` URL and a pushed commit. `--working-tree`
needs a local Git checkout with a HEAD commit and an origin URL. It scans the
final bundle and source metadata for configured local secret values and uploads the scanned copy
through the controller's signed storage binding before admitting the run.
Source acquisition ignores Git replacement objects, so a selected commit ID
keeps its original contents.
Remote API submissions must describe uploaded snapshots as committed checkouts
with their bundle digest. The `source.working_tree` field requires a local
checkout and is refused at intake when one is absent. The CLI's
`pipeline trigger --working-tree` prepares the remote snapshot automatically.
Sparkwing Cloud uses the launcher for plan, evaluation and work Jobs. Every Job
runs a rootful DinD sidecar inside its user-namespaced pod, never privileged and
with namespaced capabilities only, on a team-dedicated node, with
fixed 20 GiB aggregate scratch. Jobs keep their exact team-node reservation,
tolerate the CPU-band taint and carry `karpenter.sh/do-not-disrupt`.
The launcher's Kubernetes identity is separate from the customer Job identity.
Jobs exchange a one-use bootstrap Secret for an in-memory awarded capability.

The [Cloud chart](../charts/sparkwing-full/README.md) documents its values and
node/network prerequisites. Team-node labels hash the Cloud Job namespace with the team slug.
When a team has no vacant Ready node, the launcher lists NodePools read-only. A
compatible pool has the exact protected team label and `NoSchedule` taint in
its template and permits the job's Linux hardware class. Explicit instance CPU
or memory bounds that exclude the award make a pool incompatible. Unknown
instance availability keeps waiting. Pool readiness, limits and provider
availability do not determine whether capacity is configured. With a compatible
pool, the launcher holds the award and creates
an unbound, award-sized capacity-request Pod that only the team's NodePool can
satisfy, so Karpenter adds a node that the held award then reserves. Run status
shows “Waiting for dedicated Cloud capacity” with elapsed time until activation.
The initial credit reservation is a hold. Usage billing begins with execution.
Users can cancel while waiting.

When no compatible pool exists, the unit ends with reason
`cloud_capacity_unconfigured` and “Cloud capacity is not configured for this
team. Contact support@sparkwing.dev”, plus the run ID as a support reference.
The controller releases the unstarted credit reservation in the terminal
transaction, and the launcher deletes the capacity-request Pod. Its operator
log names the protected team label value to configure. Pool-list failures keep
the award pending for retry. Provision the team pool and trigger a new run. Team
NodePools need `consolidateAfter` above the 3-minute award lease. Self-hosted runner pools use `runner.repos`,
`runner.alsoClaimTriggers` and pool scheduling values. They claim through HTTP
and need no Kubernetes API token.

The runner does not care which cluster it lives in. The same pipeline
binary runs everywhere - the only differences are the controller URL and
the registries available.

## Profiles

Profiles map cluster names to controller URLs. Stored in the `profiles`
section of `~/.config/sparkwing/config.yaml`:

```yaml
profiles:
  dev:
    controller:
      url: http://localhost:9001
      token: <api-token>
  prod:
    controller:
      url: https://api.example.com
      token: <api-token>
```

Register profiles with `sparkwing profiles add`.

## Deploy Strategies

What happens after a pipeline builds images is entirely up to the pipeline
author. Common patterns:

### kubectl (simple, works everywhere)

```go
func (j *Deploy) Run(ctx context.Context) error {
    _, err := sparkwing.Bash(ctx, "kubectl rollout restart deploy/myapp -n default").Run()
    return err
}
```

### GitOps + ArgoCD

Push updated image tags to a gitops repo, let ArgoCD sync:

```go
func (j *Deploy) Work(w *sw.Work) (*sw.WorkStep, error) {
    update := sw.Step(w, "update-gitops", func(ctx context.Context) error {
        return patchKustomization(ctx)
    })
    sw.Step(w, "sync-argocd", func(ctx context.Context) error {
        _, err := sw.Bash(ctx,
            "kubectl annotate application.argoproj.io/myapp -n argocd "+
                "argocd.argoproj.io/refresh=hard --overwrite").Run()
        return err
    }).Needs(update)
    return nil, nil
}
```

### Helm

```go
_, err := sparkwing.Bash(ctx,
    "helm upgrade myapp ./charts/myapp --set image.tag="+tag).Run()
```

### S3 Static Sites

```go
_, err := sparkwing.Bash(ctx, "aws s3 sync out/ s3://my-bucket/ --delete").Run()
```

### Anything else

Pipelines are Go functions. If you can script it, you can deploy it -
Terraform, Pulumi, `rsync`, custom APIs, etc.

## Container Registries

Sparkwing does not deploy a container registry. Pipelines push to whatever
registry you provide - one you run in the cluster yourself, or a hosted
service:

| Registry | Example |
|----------|---------|
| One you run in-cluster | `localhost:<nodeport>/myapp:latest` |
| ECR | `<account>.dkr.ecr.<region>.amazonaws.com/myapp:v1` |
| Docker Hub | `docker.io/myorg/myapp:v1` |
| GCR / GAR | `gcr.io/myproject/myapp:v1` |

The SDK provides `sparkwing.Exec()` and `sparkwing.Bash()` - use whatever
Docker / registry commands your pipeline needs.

## Change Detection

Pipelines can implement their own change detection. A common pattern is
mapping file paths to images:

```go
var appMapping = []struct {
    prefix string
    images []string
}{
    {"web/",     []string{"frontend"}},
    {"cmd/api/", []string{"api-server"}},
    {"pkg/",     []string{"api-server", "worker"}},
}

// Compare prefixes against rc.Git.ChangedFiles(ctx, base)
```

Sources for changed files:

1. `rc.Git.ChangedFiles(ctx, base)` - repo-relative paths changed
   between a base ref and HEAD (a git diff; see
   [sdk-reference.md](sdk-reference.md))
2. An explicit `--all`-style input on your pipeline to deploy everything
