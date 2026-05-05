# sparks-core (Example Spark Library)

sparks-core is an example of a **spark library** — a reusable Go module that provides pipeline helpers. It is not part of the sparkwing SDK and is not required to use sparkwing. It demonstrates the pattern of extracting common pipeline logic into shared libraries.

This particular library provides packages for Docker builds, GitOps deployments, Kubernetes operations, AWS helpers, pre-commit checks, S3 static site deploys, and deploy orchestration. Your spark libraries can provide whatever your team needs.

**Install:** `go get github.com/sparkwing-dev/sparks-core@latest`

## Packages

| Package | Import | Purpose |
|---------|--------|---------|
| `checks` | `sparks-core/checks` | Pre-commit/pre-push checks: formatting, linting, tests, trailing newlines |
| `docker` | `sparks-core/docker` | Docker build, push, multi-registry tagging with deterministic content hashing |
| `git` | `sparks-core/git` | Git commit SHA, dirty detection, fileset hashing |
| `gitops` | `sparks-core/gitops` | GitOps deployment with kustomize patching, retry, and ArgoCD sync |
| `kube` | `sparks-core/kube` | Kubernetes deploy helpers: kubectl, kustomize, node architecture detection |
| `aws` | `sparks-core/aws` | AWS profile detection and IRSA support |
| `deploy` | `sparks-core/deploy` | Deploy orchestrator: routes to gitops+ArgoCD or kubectl based on environment |
| `s3` | `sparks-core/s3` | S3 static site deployment with correct cache headers |

---

## checks

Pre-commit and pre-push validation. All functions panic on failure (sparkwing catches panics and reports them as pipeline errors).

```go
import "github.com/sparkwing-dev/sparks-core/checks"
```

### `checks.GoFmt()`

Checks that all git-tracked `.go` files are formatted. Panics with a list of unformatted files.

### `checks.GoVet(pkgs ...string)`

Runs `go vet` on the given packages. Defaults to `./...`.

For multi-module repos (e.g., a polyglot monorepo), pass a subdirectory pattern like `"./services/api/..."`. If the subdirectory contains its own `go.mod`, the command automatically rewrites to `go -C services/api vet ./...`.

```go
// Single-module repo
checks.GoVet()

// Multi-module repo
checks.GoVet("./services/api/...")
```

### `checks.GoTestShort(pkgs ...string)`

Runs `go test -short`. Same multi-module support as `GoVet`.

```go
checks.GoTestShort()              // ./...
checks.GoTestShort("./myapp/...") // multi-module
```

### `checks.GoTest(pkgs ...string)`

Runs `go test` (without `-short`). Same multi-module support.

### `checks.TrailingNewlines()`

Validates that all git-tracked text files end with a newline. Skips binary files and common non-text extensions (images, fonts, archives). Should be the first check in pre-commit hooks.

---

## docker

Docker build, push, and image tagging with deterministic content-based tags.

```go
import "github.com/sparkwing-dev/sparks-core/docker"
```

### `docker.ComputeTags() ImageTag`

Derives deterministic image tags from the current repo state:
- **Commit** — 12-char git SHA (or `SPARKWING_COMMIT` env var)
- **Content** — 12-char SHA256 of all files affecting the Docker build (respects `.dockerignore`)
- **Dirty** — true if working tree has uncommitted changes

```go
tags := docker.ComputeTags()
tags.DeployTag() // "commit-abc123def456-files-789012345678"
tags.ProdTag()   // "commit-abc123def456-files-789012345678-prod"
// If dirty: "commit-abc123def456-files-789012345678-dirty"
```

### `docker.BuildAndPush(cfg BuildConfig)`

Builds a Docker image and pushes to one or more registries. Handles ECR authentication and repo creation automatically.

```go
docker.BuildAndPush(docker.BuildConfig{
    Image:      "myapp",
    Dockerfile: "Dockerfile",
    Context:    ".",                    // default: "."
    Registries: registries,
    Tags:       tags,
    Platform:   "linux/arm64",         // optional: cross-compile target
})
```

**BuildConfig fields:**

| Field | Type | Description |
|-------|------|-------------|
| `Image` | `string` | Image name (e.g., `"sparkwing-controller"`) |
| `Dockerfile` | `string` | Path to Dockerfile |
| `Context` | `string` | Build context directory (default: `"."`) |
| `Registries` | `[]string` | Registries to push to |
| `Tags` | `ImageTag` | Computed image tags |
| `AWSProfile` | `string` | AWS profile for ECR (auto-detected if empty) |
| `Platform` | `string` | Target platform (e.g., `"linux/arm64"`) |

### `docker.DetectRegistries(cluster, defaultECR string) []string`

Returns all available registries: local Kind cluster registry + the default ECR.

```go
registries := docker.DetectRegistries("sparktest", "123456789012.dkr.ecr.us-west-2.amazonaws.com")
```

### `docker.DetectLocalRegistries(cluster string) []string`

Returns only the local Kind cluster's registry. Use this when you want to skip ECR entirely (local-only mode).

---

## git

Git state detection for deterministic builds.

```go
import "github.com/sparkwing-dev/sparks-core/git"
```

### `git.ShortCommit() string`

Returns the current HEAD commit SHA (12 chars). Falls back to `SPARKWING_COMMIT` env var when not in a git repo.

### `git.IsDirty() bool`

Returns true if the working tree has uncommitted or unstaged changes. Honors `SPARKWING_DIRTY` env var.

### `git.FilesetHash() string`

SHA256 hash of all files that affect the Docker build (respects `.gitignore` and `.dockerignore`). Returns 12-char hex. Falls back to filesystem walk when `.git` is not available.

---

## gitops

GitOps deployment: clone the gitops repo, patch kustomization.yaml image tags, push. Includes ArgoCD sync.

```go
import "github.com/sparkwing-dev/sparks-core/gitops"
```

### `gitops.Deploy(cfg DeployConfig)`

Clones the gitops repo, updates image tags in `kustomization.yaml`, and pushes. Retries on concurrent push conflicts.

```go
gitops.Deploy(gitops.DeployConfig{
    GitopsRepo: "git@github.com:your-org/gitops.git",
    GitopsPath: "sparkwing",           // path within repo
    ECR:        "123456789012.dkr.ecr.us-west-2.amazonaws.com",
    Images:     []string{"myapp"},
    Tag:        tags.ProdTag(),
    CommitMsg:  "deploy: v1.2.3",      // optional, default: "deploy: <tag>"
    MaxRetries: 5,                     // optional, default: 5
})
```

**Transport priority:**
1. Gitcache HTTP (fast clone, no SSH needed)
2. GitHub HTTPS + PAT (`GITHUB_TOKEN` env var)
3. Direct SSH (k8s-mounted keys)

**Authorization:** Before pushing, calls the sparkwing controller's `/authorize` endpoint for audit logging. Skip with `SPARKWING_NO_VERIFY=1` (break-glass).

### `gitops.SyncArgoCD(appName string)`

Triggers a hard ArgoCD sync and waits until the application is synced + healthy. Retries refresh+sync to handle stale cache. 4-minute timeout.

```go
gitops.SyncArgoCD("sparkwing")
```

---

## kube

Kubernetes deployment and cluster detection.

```go
import "github.com/sparkwing-dev/sparks-core/kube"
```

### `kube.IsRunningInK8s() bool`

Returns true when executing inside a Kubernetes pod (checks `KUBERNETES_SERVICE_HOST`).

### `kube.DetectNodeArch() string`

Returns the cluster's node architecture as a Docker platform string (e.g., `"linux/arm64"`). Useful for cross-platform builds when building on ARM Macs for Graviton nodes.

### `kube.DeployKubectl(images []string, deployMap map[string]string, namespace string)`

Restarts deployments directly via `kubectl rollout restart`. The `deployMap` maps image names to k8s deployment names:

```go
kube.DeployKubectl(
    []string{"myapp"},
    map[string]string{"myapp": "deploy/myapp"},
    "default",
)
```

### `kube.DeployKustomize(cfg DeployKustomizeConfig)`

Updates a local cluster's `kustomization.yaml` with pinned image tags and applies via `kubectl apply -k`. Falls back to rollout restart if the kustomization file doesn't exist.

```go
kube.DeployKustomize(kube.DeployKustomizeConfig{
    Images:    []string{"myapp"},
    Tag:       tags.DeployTag(),
    Cluster:   "sparktest",
    Namespace: "sparkwing",       // default: "sparkwing"
    Registry:  "localhost:30500", // default: "localhost:30500"
    DeployMap: imageDeployment,   // fallback for missing kustomization
})
```

---

## aws

AWS profile and IRSA detection.

```go
import "github.com/sparkwing-dev/sparks-core/aws"
```

### `aws.ProfileFlag(defaultProfile string) string`

Returns `" --profile <name>"` for local development, or `""` when running under IRSA (EKS). Append directly to AWS CLI commands.

```go
profileFlag := aws.ProfileFlag("prod")
sparkwing.Bash(ctx, "aws s3 ls"+profileFlag).Run()
// Local:  aws s3 ls --profile prod
// EKS:    aws s3 ls
```

### `aws.IsIRSA() bool`

Returns true when running with IAM Roles for Service Accounts (checks `AWS_WEB_IDENTITY_TOKEN_FILE`).

### `aws.DefaultProfile`

The default AWS profile name: `"prod"`.

---

## deploy

High-level deploy orchestrator that routes to the correct strategy based on the execution environment.

```go
import "github.com/sparkwing-dev/sparks-core/deploy"
```

### `deploy.Run(cfg Config)`

Executes a deployment:
- **In-cluster (EKS):** pushes image tags to the gitops repo via `gitops.Deploy()`, then syncs ArgoCD
- **Local (Kind):** restarts deployments directly via `kubectl rollout restart`

```go
deploy.Run(deploy.Config{
    GitopsRepo: "git@github.com:your-org/gitops.git",
    GitopsPath: "sparkwing",
    ECR:        "123456789012.dkr.ecr.us-west-2.amazonaws.com",
    Images:     []string{"myapp"},
    Tag:        tags.ProdTag(),
    AppName:    "sparkwing",     // ArgoCD application name
    Namespace:  "sparkwing",     // K8s namespace for kubectl fallback
    DeployMap:  imageDeployment, // image name -> k8s deployment
})
```

This replaces the common pattern of manually checking `kube.IsRunningInK8s()` and branching between gitops and kubectl.

---

## s3

S3 static site deployment with correct cache headers for CDN-served sites.

```go
import "github.com/sparkwing-dev/sparks-core/s3"
```

### `s3.DeployStaticSite(cfg StaticSiteConfig) (SyncResult, error)`

Syncs a static site build directory to S3 with appropriate cache headers:
- **Non-HTML assets** (JS, CSS, images): 1-year immutable cache (fingerprinted by bundler)
- **HTML files**: no-cache (always serve fresh content)

Returns per-pass upload counts (`SyncResult{AssetUploads, HTMLUploads}`)
so callers can detect internally inconsistent deploys (new chunks
shipped while HTML is unchanged).

```go
res, err := s3.DeployStaticSite(ctx, s3.StaticSiteConfig{
    Bucket:     "my-website-bucket",
    OutDir:     "out",          // default: "out"
    AWSProfile: "prod",    // default: aws.DefaultProfile
})
```

---

## Debug Mode

Set `SPARKWING_DEBUG=1` to enable verbose debug logging from all sparks-core packages. Debug output is dimmed and prefixed with `debug:` to distinguish it from normal pipeline output.

```bash
SPARKWING_DEBUG=1 wing build-deploy
```

Debug logging covers:
- **git**: commit SHA source, dirty detection method, fileset hash computation
- **docker**: registry detection results, environment overrides
- **deploy**: strategy selection (gitops+ArgoCD vs kubectl)
- **kube**: in-cluster detection, node architecture
- **aws**: profile selection, IRSA detection
- **s3**: bucket and output directory

Normal pipeline runs show none of this — only the step banners and results.

---

## Environment Variables

sparks-core respects these environment variables:

| Variable | Used By | Purpose |
|----------|---------|---------|
| `SPARKWING_DEBUG` | all | Enable verbose debug logging |
| `SPARKWING_COMMIT` | `git` | Override commit SHA (set by runner in-cluster) |
| `SPARKWING_DIRTY` | `git` | Override dirty detection |
| `SPARKWING_GITCACHE` | `gitops` | Gitcache URL override |
| `SPARKWING_CONTROLLER` | `gitops` | Controller URL for deploy authorization |
| `SPARKWING_NO_VERIFY` | `gitops` | Skip deploy authorization (break-glass) |
| `SPARKWING_REGISTRY` | `docker` | Override registry detection |
| `GITHUB_TOKEN` | `gitops` | GitHub PAT for HTTPS push |
| `AWS_PROFILE` | `aws` | Override AWS profile |
| `AWS_WEB_IDENTITY_TOKEN_FILE` | `aws` | IRSA detection |
| `KUBERNETES_SERVICE_HOST` | `kube` | In-cluster detection |
