# Fast builds: best practices

A living checklist of things that make sparkwing pipelines iterate fast.
These emerged from measuring real iterations (touch a file, rebuild,
redeploy, observe running) and watching where the time went.

Target for a single-app Go service, laptop -> cluster via upload trigger:
**< 15 seconds from edit to running and healthy**.

---

## 1. Mount Go build + module cache in your Dockerfile

**Impact: 13s -> 0.7s** on the Go compile step.

Without cache mounts, every docker build starts from scratch when the
`COPY . .` layer invalidates - which happens on any source change. With
them, Go's incremental compiler only rebuilds what actually changed.

```dockerfile
# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /out/server ./cmd/server
```

The cache mounts persist across builds on the same BuildKit daemon (local
docker, or the runner pod's DinD). They survive image-layer
invalidation - exactly the opposite of a `COPY`-cached `RUN go build`.

Same pattern works for Rust (`/usr/local/cargo/registry`,
`/app/target`), Node (`/app/node_modules`), Maven (`~/.m2`), etc.

## 2. Don't push to registries you don't need

**Impact: ~5-7 seconds** when iterating against a local cluster.

If your pipeline pushes to multiple registries every build, you are
paying the round-trip cost on every iteration. Gate the registry list
on the deploy target:

```go
// Example: only push to a remote registry when running in production
var registries []string
if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
    registries = []string{prodRegistry}
} else {
    registries = []string{"localhost:30500"} // local registry
}
```

## 3. Use upload triggers, not git pushes, for iteration

`wing build-deploy --on prod` uploads an incremental diff from the
current commit to the cache and triggers the pipeline in-cluster.
Versus `git commit && git push`:

- no commit pollution of history for every experimental edit
- incremental sync is `HEAD` + changed files, typically a few KB
- wing streams live logs back via SSE

Git-push mode is a good production path (CI-style, audited), but for
"change a log line and re-run" it is the wrong gear.

## 4. Register your repo with the cache on startup

If you delete the cache PVC (or stand up a new cluster), repos have to
be re-registered before the runner can clone them. The failure mode is
cryptic:

```
fatal: repository '.../<repo>/' not found
```

Register up-front - script it as part of your cluster bootstrap:

```bash
POST /git/register?name=<repo>&repo=<ssh-url>
```

## 5. Let sparkwing resolve spark libraries automatically

Spark libraries are resolved at build time from `.sparkwing/sparks.yaml` -
you do not need to bump `go.mod` across repos when a new version ships.
See [sparks.md](sparks.md) for details.

```yaml
# .sparkwing/sparks.yaml
libraries:
  - name: my-spark-lib
    source: github.com/example/my-spark-lib
    version: latest
```

When a new version is tagged, every pipeline picks it up on its next
build automatically. The binary cache key includes the resolved version,
so a new release triggers a recompile only once - then it is cached.

To force an immediate update: `sparkwing pipeline sparks update`.

## 6. Keep your Dockerfile's external dependencies cached

Per-build `apk add`, `curl kubectl`, `npm install` without cache mounts
blow a few seconds every build. Either:

- move them into an earlier, rarely-changing layer (before `COPY . .`)
- mount the tool cache (`/var/cache/apk`, `~/.npm`, `~/.m2`)
- bake them into the base image

## 7. Consolidate redundant image tags

Every extra `docker push` is a separate manifest API call (~200ms apiece
on a warm remote registry, ~1-2s apiece on cold local registries). If
you are pushing `:latest`, `:commit-xxx`, `:files-yyy`, and a long
deploy tag, drop the ones nothing consumes. The ones that matter:

- `:commit-xxx-files-yyy-prod` - what gitops pins in kustomization
- `:latest` - nice for humans doing `kubectl set image` manually

Anything else is likely debugging residue.

## 8. Match your local cluster to production

When your local and prod clusters have different deployment layouts (one
has the app, the other does not), iteration in local mode fails on
deploy - the image pushes fine but `kubectl rollout restart` hits
`deployment not found`. Apply the same gitops layout locally that ArgoCD
applies to prod.
