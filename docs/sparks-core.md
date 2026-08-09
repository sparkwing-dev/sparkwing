# sparks-core (Example Spark Library)

sparks-core is an example of a **spark library** -- a multi-module monorepo
of reusable Go modules that provide pipeline helpers, each versioned and
consumed independently. It is not part of the sparkwing SDK and is
not required to use sparkwing. It demonstrates the pattern of extracting
common pipeline logic into shared libraries: your own spark libraries can
provide whatever your team needs.

**Install:** each block is its own Go module -- `go get
github.com/sparkwing-dev/sparks-core/<block>@latest`, e.g.
`go get github.com/sparkwing-dev/sparks-core/docker@latest`. The umbrella
root path holds no importable packages.

The exact, current API -- every function signature, type, and field -- is
on pkg.go.dev, one page per block module, e.g.
[pkg.go.dev/github.com/sparkwing-dev/sparks-core/docker](https://pkg.go.dev/github.com/sparkwing-dev/sparks-core/docker).
This page is a conceptual tour of what the library offers and how the
pieces fit together; treat the godoc as authoritative for signatures.

Every helper that does work takes a `context.Context` as its first
argument and returns an `error` (sparkwing reports the error as a step
failure). They do not panic.

## Block modules

Each block is its own Go module; `spark.json` at the repo root is the full
list:

| Package | Purpose |
|---------|---------|
| `checks` | Pre-commit/pre-push checks: formatting, vet, tests, trailing newlines |
| `docker` | Docker build, push, ECR login, and multi-registry detection |
| `gitops` | GitOps deploy: clone, patch image tags, push, ArgoCD sync |
| `kube` | Kubernetes helpers: kubectl apply/rollout, kustomize, node-arch detection |
| `aws` | AWS profile / IRSA detection for CLI invocations |
| `deploy` | Deploy orchestrator: routes to gitops+ArgoCD or local kubectl |
| `rollback` | Rollback orchestrator (the inverse of `deploy`) |
| `s3` | S3 static-site deployment with correct cache headers |
| `notify` | Webhook / Slack notifications |
| `probe` | HTTP health probes for post-deploy verification |
| `services` | Ephemeral Docker services (e.g. Postgres) for tests |
| `templates` | Pipeline template registry and rendering |
| `gcp` | GCP project/auth resolution and Workload Identity detection |
| `cloudrun` | Cloud Run deploy, traffic shifting, URL discovery, rollback |
| `ecs` | ECS/Fargate task-definition rollout, wait-for-stable, rollback |
| `lambda` | Lambda deploys (image and zip), version publish, alias shift |
| `terraform` | Terraform init, plan-to-file, apply-saved-plan, change summaries |
| `release` | Version gating, changelog parsing, checksums, GitHub/npm/PyPI publish |
| `coverage` | Coverage report parsing (coverprofile, lcov, cobertura) and gating |
| `contentkey` | Content-addressed cache keys and skip predicates for path-scoped work |
| `migrate` | Schema migrations via the golang-migrate CLI |
| `dbbackup` | Database dump, backup upload, and restore helpers |
| `pipelines` | High-level primitives: DockerDeploy, StaticDeploy, NextJSBuild |
| `step` | Shared step-banner and error-wrapped shell helpers |

## checks

Pre-commit and pre-push validation. Each function checks one thing and
returns an error describing what failed:

- **GoFmt** -- all git-tracked `.go` files are formatted.
- **GoVet** -- `go vet` on the given packages (default `./...`).
- **GoTestShort / GoTest** -- `go test`, with or without `-short`.
- **TrailingNewlines** -- all git-tracked text files end with a newline.

`GoVet`, `GoTestShort`, and `GoTest` accept package patterns. For a
polyglot monorepo, pass a subdirectory pattern like
`"./services/api/..."`; if that subdirectory has its own `go.mod`, the
command rewrites itself to run in that module.

```go
import "github.com/sparkwing-dev/sparks-core/checks"

checks.GoFmt(ctx)
checks.GoVet(ctx)                    // ./...
checks.GoTestShort(ctx, "./api/...") // multi-module
```

## docker

Docker build, push, and ECR helpers.

`BuildAndPush` builds an image and pushes it to one or more registries,
handling ECR login automatically (the target ECR repository must already
exist; a push to a missing repo fails). Its `Tags`
field takes a `docker.ImageTag` value from the **SDK** (`sparkwing/docker`,
via `docker.ComputeTags`) -- see [sdk-reference.md](sdk-reference.md) for
how content-addressed tags are derived. sparks-core itself does not
compute tags; it consumes the SDK's.

```go
import "github.com/sparkwing-dev/sparks-core/docker"

docker.BuildAndPush(ctx, docker.BuildConfig{
    Image:      "myapp",
    Dockerfile: "Dockerfile",
    Context:    ".",
    Registries: registries,
    Tags:       tags,           // a sparkwing/docker.ImageTag
    Platform:   "linux/arm64",  // optional cross-compile target
})
```

`DetectRegistries(cluster, defaultECR)` and `DetectLocalRegistries(cluster)`
return the available push targets (and an error). The local form skips
ECR for local-only builds.

## gitops

Clone a gitops repo, patch image tags in `kustomization.yaml`, push, and
optionally sync ArgoCD. `Deploy` retries on concurrent push conflicts and
reports whether it changed anything.

**Transport:** the initial clone is via gitcache when reachable (a fast
read cache); the push always goes direct to GitHub over HTTPS with a PAT
(`GITHUB_TOKEN`), falling back to SSH. gitcache is never a push target.

When `SPARKWING_CONTROLLER` is set, `Deploy` asks the controller to
authorize the push before it happens: a 403 blocks the deploy, while an
unreachable or erroring controller logs and continues. With
`SPARKWING_CONTROLLER` unset the check is skipped entirely.
`SPARKWING_NO_VERIFY=1` is the break-glass opt-out.

```go
import "github.com/sparkwing-dev/sparks-core/gitops"

gitops.Deploy(ctx, gitops.DeployConfig{
    GitopsRepo: "git@github.com:your-org/gitops.git",
    GitopsPath: "sparkwing",
    ECR:        "123456789012.dkr.ecr.us-west-2.amazonaws.com",
    Images:     []string{"myapp"},
    Tag:        tags.ProdTag(),
})
gitops.SyncArgoCD(ctx, "sparkwing")
```

## kube

Kubernetes deployment and cluster detection: `IsRunningInK8s`,
`DetectNodeArch` (returns a Docker platform string like `"linux/arm64"`,
handy when building on ARM Macs for Graviton nodes), `DeployKubectl`
(rollout restart), `DeployKustomize` / `DeployKindKustomize` (pin image
tags and `kubectl apply -k`), plus lower-level `Apply`, `SetImage`, and
`RolloutUndo`.

```go
import "github.com/sparkwing-dev/sparks-core/kube"

kube.DeployKubectl(ctx,
    []string{"myapp"},
    map[string]string{"myapp": "deploy/myapp"},
    "default",
)
```

## aws

`ProfileFlag(defaultProfile)` returns `" --profile <name>"` for local
development, or `""` under IRSA (EKS), so you can append it directly to an
AWS CLI command. `ProfileArgs` is the slice form. `IsIRSA` reports whether
a web-identity token is mounted. With `AWS_PROFILE` unset and an empty
`defaultProfile`, both helpers return nothing, so the aws CLI falls back to
its own credential chain (env keys, SSO cache, instance metadata).

```go
import "github.com/sparkwing-dev/sparks-core/aws"

flag := aws.ProfileFlag("staging")
sparkwing.Bash(ctx, "aws s3 ls"+flag).Run()
// Local:  aws s3 ls --profile staging
// EKS:    aws s3 ls
```

## deploy

High-level orchestrator that picks a deploy strategy:

- **Prod (gitops):** pushes image tags to the gitops repo via
  `gitops.Deploy`, then syncs ArgoCD.
- **Local (kind):** restarts deployments directly via kubectl, or applies
  a repo-owned `k8s/` kustomization when present.

The routing decision is based on `cfg.Local` and the `SPARKWING_KIND_CLUSTER`
environment variable (which sparkwing sets when the resolved profile
targets a kind cluster), **not** on whether the code happens to run inside
a cluster. A laptop deploy to prod still goes through gitops.

```go
import "github.com/sparkwing-dev/sparks-core/deploy"

deploy.Run(ctx, deploy.Config{
    GitopsRepo: "git@github.com:your-org/gitops.git",
    GitopsPath: "sparkwing",
    ECR:        "123456789012.dkr.ecr.us-west-2.amazonaws.com",
    Images:     []string{"myapp"},
    Tag:        tags.ProdTag(),
    AppName:    "sparkwing",
    Namespace:  "sparkwing",
    DeployMap:  imageDeployment,
})
```

## s3

`DeployStaticSite` syncs a build directory to S3 with cache headers tuned
for CDN serving: non-HTML assets (JS, CSS, images -- bundler-fingerprinted)
get a one-year immutable cache; HTML files get no-cache so visitors always
get fresh markup. It returns per-pass upload counts so callers can detect
an inconsistent deploy (new chunks shipped while HTML is unchanged).
`Bucket` is required; `OutDir` defaults to `out`. `AWSProfile` is optional --
leave it empty in-cluster and the aws CLI uses its ambient credentials.

```go
import "github.com/sparkwing-dev/sparks-core/s3"

res, err := s3.DeployStaticSite(ctx, s3.StaticSiteConfig{
    Bucket:     "my-website-bucket",
    OutDir:     "out",
    AWSProfile: "prod",  // optional; empty -> ambient credentials
})
```

## Environment variables

sparks-core reads these (most are set automatically by sparkwing when it
launches a runner):

| Variable | Used by | Purpose |
|----------|---------|---------|
| `SPARKWING_KIND_CLUSTER` | `deploy`, `rollback`, `docker`, `kube` | Routes deploys into local-kubectl mode |
| `SPARKWING_KUBE_CONTEXT` | `kube` | Kubectl context override |
| `SPARKWING_KUBE_ALLOW_CURRENT` | `kube` | Opt in to the current kubeconfig context |
| `SPARKWING_REGISTRY` | `docker` | Local registry override |
| `SPARKWING_ECR_REGISTRY` | `docker` | ECR registry override |
| `SPARKWING_CONTROLLER` | `gitops` | Controller URL for deploy authorization |
| `SPARKWING_NO_VERIFY` | `gitops` | Skip deploy authorization (break-glass) |
| `GITHUB_TOKEN` | `gitops` | GitHub PAT for HTTPS push |
| `AWS_PROFILE` | `aws` | AWS profile name |
| `AWS_WEB_IDENTITY_TOKEN_FILE` | `aws` | IRSA detection |
| `KUBERNETES_SERVICE_HOST` | `kube` | In-cluster detection |
| `SPARKWING_DRY_RUN` | `cloudrun`, `ecs`, `lambda`, `terraform`, `gcp`, `docker`, `kube`, `release`, `dbbackup`, `contentkey` | Non-empty makes mutating commands echo their argv instead of executing |
| `SPARKWING_ARGOCD_SERVER` / `SPARKWING_ARGOCD_TOKEN` | `gitops` | ArgoCD API endpoint and bearer token for `SyncArgoCD` |
| `SPARKWING_API_TOKEN` | `gitops` | Bearer sent to the controller authorization endpoint |
