# Deployment

Sparkwing is unopinionated about how your pipelines deploy. It provides
the infrastructure - controller, runners, cache, logs - and your
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
  → CLI seeds that commit into the cluster's gitcache (best effort, so an unpushed
     SHA is still fetchable)
  → controller enqueues the run
  → a runner in the pool claims it on its next poll
  → runner clones the ref and executes the pipeline
```

The default clones the named commit and excludes uncommitted files.
`--working-tree` instead creates a deterministic synthetic commit from tracked
changes and untracked non-ignored files, uploads its Git bundle before trigger
admission, and runs that exact snapshot without pushing it to the origin. The
capture rejects conflicts, submodules, sparse checkouts, and Git content
filters. It also requires a complete SHA-1 repository. Both modes require an
`origin` URL as the cache namespace.
A runner started with `--trigger-runner k8s` creates one Kubernetes Job per
node. `--trigger-runner warm` offers nodes to remote agents first and uses
Kubernetes for unlabeled overflow. Both modes are opt-in; the runner-bundle
chart exposes them through `runner.triggerRunner.kind`, while `inprocess`
remains the default.

The runner does not care which cluster it lives in. The same pipeline
binary runs everywhere - the only differences are the controller URL and
the registries available.

## Profiles

Profiles map cluster names to controller URLs. Stored in
`~/.config/sparkwing/profiles.yaml`:

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

Register profiles with `sparkwing configure profiles add`.

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
