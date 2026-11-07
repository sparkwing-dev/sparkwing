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
GitHub repository identity is resolved by the selected controller through the
team's GitHub App installation or configured source credentials. An App-covered
private repository needs no local GitHub token for identity lookup, including
SSH clones and triggers without `--ref`. The CLI can use an explicit `GH_TOKEN`
or `GITHUB_TOKEN` if the controller cannot resolve identity. It never performs
an anonymous GitHub lookup. Clone access and the controller's source authority
remain separate from identity lookup.

Every profile with a controller follows the same dispatch flow:

```
sparkwing pipeline trigger <pipeline> --profile <profile>
  → CLI reads the origin URL + branch/SHA from your checkout and POSTs the trigger
  → CLI checks both selected commits against local origin fetch records
  → controller enqueues the run
  → a runner in the pool claims it on its next poll
  → runner clones the ref and executes the pipeline
```

The default clones the named commit and excludes uncommitted files.
`--working-tree` instead creates a deterministic synthetic commit from tracked
changes and untracked non-ignored files, uploads its Git bundle before trigger
admission, and runs that exact snapshot without pushing it to the origin. The
capture rejects conflicts, submodules, sparse checkouts, and Git content
filters. It also requires a complete SHA-1 repository.

Snapshots and their bundles are limited to 500 MiB. If an upload exceeds this
limit, exclude unneeded files with `.gitignore`. Tracked files must first be
untracked with `git rm --cached PATH`. Alternatively, commit and run `git push`,
then trigger without `--working-tree` to fetch the commit.

The default requires an `origin` URL and a pushed commit. Both the execution
commit and any separate pipeline-code commit must appear on an origin-tracking
branch or in an exact-SHA `FETCH_HEAD` record from that origin. Sparkwing's
runner checkouts fetch by SHA, so a job can trigger another pipeline at its
fetched commit without fetching branch tips first. These local records check
availability, not repository trust. The controller still decides source authority.

`--working-tree` needs a local Git checkout with a HEAD commit and an origin URL. It scans the
final bundle and source metadata for configured local secret values and uploads the scanned copy
through the controller's signed storage binding before admitting the run.
Source acquisition ignores Git replacement objects, so a selected commit ID
keeps its original contents.
Remote API submissions must describe uploaded snapshots as committed checkouts
with their bundle digest. The `source.working_tree` field requires a local
checkout and is refused at intake when one is absent. The CLI's
`pipeline trigger --working-tree` prepares the remote snapshot automatically.
Declared children in the same team reuse the parent's retained bundle when the
repository, ref, execution commit, pipeline commit and bundle digest all match.
Unrelated runs require their own committed upload.
Sparkwing Cloud uses the launcher for plan, evaluation and work Jobs. Every Job
runs a rootful DinD sidecar inside its user-namespaced pod, never privileged and
with namespaced capabilities only, on a node bound to one team and run, with
fixed 20 GiB aggregate scratch. Jobs keep their exact team-node reservation,
tolerate the generic dedicated taint and carry `karpenter.sh/do-not-disrupt`.
The launcher's Kubernetes identity is separate from the customer Job identity.
Jobs exchange a one-use bootstrap Secret for an in-memory awarded capability.

The [Cloud chart](../charts/sparkwing-full/README.md) documents its values and
node/network prerequisites. Configure one shared Karpenter NodePool per hardware
class and deployment. Its template carries
`node-restriction.kubernetes.io/sparkwing-cloud-class=<cores>` and
`sparkwing.dev/launcher-namespace=<Cloud Job namespace>`, plus `sparkwing.dev/warm-namespace=<trusted warm namespace>` and the generic
`sparkwing.dev/dedicated=true:NoSchedule` taint. Team creation needs no NodePool
change. The launcher binds a never-used node to the exact protected team value
and run when it reserves it. Permanent used-team and used-run fences prevent
reuse across runs, including child runs. Later jobs of the same run may share
its node. If a matching node is finishing the previous job's fenced teardown,
the successor waits up to 30 seconds before requesting another node. A live job
still permits fanout onto fresh capacity.

Once a used node has sat unreserved and empty for 60 seconds, the launcher
retires it by deleting its UID-fenced NodeClaim, whatever state its run is in. A
later job of the same run that reserves the node within that grace reuses it; one
that becomes ready later, such as after an approval, gets a fresh node.
`sparkwing-runner launch --idle-grace` changes the grace. Retirement checks the
retained run identity, protected team/run labels and shared-pool provenance, and
requires every non-DaemonSet Pod to have disappeared. Reconciliation finds these
spent nodes after a launcher restart. Older nodes without retained run identity
fall back to Karpenter consolidation.

When no vacant Ready node fits, the launcher checks shared-pool provenance with
read-only NodePool access. A compatible pool that is scaling or exhausted keeps
the award pending. An unbound, award-sized capacity-request Pod selects the
requested class and deployment so Karpenter can add a node. Run status shows
“Waiting for dedicated Cloud capacity” with elapsed time until activation.
The launcher renews held awards during that wait, preserving the plan generation
through placement retries and claim backoff. Losing the launcher owner allows
the lease to expire and the controller to requeue the plan.
The initial credit reservation is a hold. Usage billing begins with execution.
Users can cancel while waiting.

Missing shared capacity for the requested class ends the unit with
`cloud_capacity_unconfigured`, releases its unstarted credit hold, and gives
support@sparkwing.dev plus the run ID as a support reference. Configure that
class's shared pool and trigger a new run. A request above the launcher's CPU or
memory ceiling ends the same way, but its error names the requested class, CPU
and memory and the ceilings, and says to request a smaller class or ask
support@sparkwing.dev to raise the ceiling. The launcher logs that refusal at
INFO. Pool-list failures retry. NodePools
need `consolidateAfter` above the 3-minute award lease.

A Cloud budget breaker stops Karpenter from launching job nodes and publishes
its state, `open` or `closed`, to a flag. Mount that flag in the launcher's
credentials directory as `budget-state` (chart value
`launcher.budgetStateSecret`, usually an ExternalSecret with a one-minute
refresh). While it reads `closed`, an award that needs a fresh node ends with
`cloud_capacity_unavailable` and "Cloud capacity is temporarily unavailable.
Contact support@sparkwing.dev" with the run ID as a support reference. Its
unstarted credit hold is released, so refused runs cost nothing. Units already
placed on a node keep running, and an award that finds a vacant node still
runs. An absent flag never trips. An unreadable or unknown value logs a
warning and keeps the award waiting, so a read fault never fails a run.

Refused runs do not resume. After the breaker reopens (the deny detached and
the flag back to `open`), affected customers trigger their runs again.

Low-priority, volume-free placeholder Deployments hold never-used warm nodes in
`<release namespace>-cloud-warm` by default (`launcher.warmNamespace`). That
namespace is separate from the release and customer Job namespaces. Pool warm
labels must match it. The launcher checks the ReplicaSet UID and its owning
Deployment name, `sparkwing-cloud-warm-<vCPU>`. `launcher.nodeClassName` pins the
hardened EC2NodeClass (`sparkwing-jobs` by default). Shared pools must reference
that exact name with group `karpenter.k8s.aws` and kind `EC2NodeClass`. Give a
NodePool of any other EC2NodeClass a different class, launcher-namespace or
warm-namespace label. Karpenter forbids Pods from selecting the EC2NodeClass
label, so placeholders and capacity requests can land on a pool sharing those
labels, and the launcher refuses its nodes. The launcher leaves such nodes out
of the warm count and logs a WARN naming them and their NodePool.

Operator setting
`cloud_upsize_enabled` defaults to true. Only to avoid a cold start, the launcher
may use an idle warm node one class above the requested class when no requested
class warm node is free. The job keeps its requested billing class and CPU and
memory cgroup limits. A cold capacity request always asks for the requested class.

Warm capacity defaults Off for every class. The operator **Warm capacity** page
and CLI show warm/desired nodes, start-time percentiles, cold starts and upsizes,
with signup and active-team counts. Operators select Off, Manual count or Auto
minimum/maximum and a start-time target (default 20s). Auto uses cold-start waits
in the last 30 minutes and, while a job is waiting for a fresh node, median start
time in the last hour, clamped to the configured bounds; no waiting means the
minimum.

The chart's optional `warmCapacity` infrastructure creates low-priority pause
placeholders and KEDA PostgreSQL scalers. Each placeholder reserves one catalog
node. KEDA queries the `warm_pool_desired` view with a separate read-only role;
its DSN Secret, TriggerAuthentication and ScaledObjects live in the dedicated
warm namespace. Placeholder pods mount no volumes. Infrastructure installs KEDA and
supplies shared NodePools. See the chart README and
[proposed infrastructure manifests](../design/rewrite/proposals/warm-capacity.md)
for role grants, Secret keys, resource sizing and scheduling labels.

Self-hosted runner pools use `runner.repos`, `runner.alsoClaimTriggers` and pool
scheduling values. They claim through HTTP and need no Kubernetes API token.

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

### Same-run preparation

Cloud planning publishes its source snapshot and installed pipeline through the
controller's existing data store. Jobs of the same run verify and restore it;
missing artifacts or different build inputs use fresh preparation. Corrupt
artifacts fail before execution. Prepared records follow run retention and never
provide cross-run or cross-team access.

Update the controller, launcher, runner image and generated admission policies
together. The policies read the runner and Docker images from the ConfigMap
`sparkwing-cloud-admission` in the job namespace, so an installation that
applies the chart's cluster-scoped objects separately changes only that
namespaced ConfigMap when the images move. Job Pods contain the Docker sidecar and `run-node`; remove custom
`fetch-source` init containers and `--source-dir` flags. Existing data storage and
credentials suffice; there is no prepared-cache setting.
