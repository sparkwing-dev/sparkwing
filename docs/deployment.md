# Deployment

Sparkwing is unopinionated about how your pipelines deploy. It provides
the infrastructure - controller, runners, cache, registries - and your
pipeline code decides what to do with it.

## Run Targets

Every pipeline supports `--on` to target a cluster:

| Source | Target | Command |
|--------|--------|---------|
| Local code | Local machine | `wing build` |
| Local code | Any cluster | `wing build --on dev` |
| Remote (git ref) | Any cluster | `wing build --from main --on prod` |

The `--on` flag resolves a **profile** - a named cluster endpoint. Every
profile follows the same dispatch flow:

```
wing <pipeline> --on <profile>
  → upload code to controller (or trigger by SHA if clean)
  → controller enqueues run
  → dispatcher creates k8s Job
  → runner executes pipeline
```

The runner does not care which cluster it lives in. The same pipeline
binary runs everywhere - the only differences are the controller URL and
the registries available.

## Profiles

Profiles map cluster names to controller URLs. Stored in
`~/.config/sparkwing/profiles.yaml`:

```yaml
profiles:
  dev:
    controller: http://localhost:9001
    token: <api-token>
  prod:
    controller: https://api.example.com
    token: <api-token>
```

Register profiles with `sparkwing configure profiles add`.

## Deploy Strategies

What happens after a pipeline builds images is entirely up to the pipeline
author. Common patterns:

### kubectl (simple, works everywhere)

```go
func (j *DeployJob) Run(ctx context.Context) error {
    _, err := sparkwing.Bash(ctx, "kubectl rollout restart deploy/myapp -n default").Run()
    return err
}
```

### GitOps + ArgoCD

Push updated image tags to a gitops repo, let ArgoCD sync:

```go
func (j *DeployJob) Work() *sw.Work {
    w := sw.NewWork()
    update := w.Step("update-gitops", func(ctx context.Context) error {
        return patchKustomization(ctx)
    })
    w.Step("sync-argocd", func(ctx context.Context) error {
        _, err := sw.Bash(ctx,
            "kubectl annotate application.argoproj.io/myapp -n argocd "+
                "argocd.argoproj.io/refresh=hard --overwrite").Run()
        return err
    }).Needs(update)
    return w
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

Sparkwing creates an in-cluster registry at NodePort 30500. Pipelines can
push to any registry they want:

| Registry | Example |
|----------|---------|
| In-cluster | `localhost:30500/myapp:latest` |
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

// Compare against webhook payload (ChangedFiles) or git diff
```

Sources for changed files (in priority order):

1. `sparkwing.RunContext.Trigger.ChangedFiles` - from webhook payload
2. `git diff` against a base branch
3. Explicit `--all` flag to deploy everything
