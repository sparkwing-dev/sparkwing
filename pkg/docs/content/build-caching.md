# Build Caching

How sparkwing makes Docker builds fast, and where the time actually goes.

## Where build time goes

Benchmark: full-stack Dockerfile installing 225 npm packages + 100 Ruby gems
(including native extensions like pg, nio4r, bootsnap). Tested on Apple
Silicon Mac and EKS (Graviton arm64).

### Local Mac results

| Scenario | Time | What's cached |
|---|---|---|
| True cold (system prune) | 94s | nothing |
| Base images cached | 92s | container images |
| Images + warm cache mounts | 61s | images + compiled deps |
| Full layer cache (unchanged) | 1s | everything |

### Derived cost breakdown

| Component | Cost | Notes |
|---|---|---|
| Base image pulls | ~2s | Docker Hub CDN is fast |
| Package downloads (npm + gems) | ~10–15s | registry CDNs are fast on good networks |
| Native extension compilation | ~40–50s | gcc/make for pg, nio4r, bootsnap, sassc... |
| npm resolution + linking | ~10–15s | CPU-bound, not network |
| Docker layer export | ~5–7s | writing to image store |

**The bottleneck is CPU, not network.** Compiling native extensions accounts
for ~50% of a cold build. Package downloads are only ~15% of the total.

### EKS results (Graviton arm64, 2 vCPU)

| Scenario | Time | Savings |
|---|---|---|
| Cold build | 105s | — |
| Layer cache (nothing changed) | 0s | 105s (100%) |
| --no-cache, cache mounts warm | 101s | 4s (4%) |
| Dep change + warm cache mounts | 101s | 2s vs cold dep change |
| Cold build + proxy (cached) | 98s | 7s (7%) |

The EKS build is 1.7x slower than the Mac, primarily from CPU limits
(2 vCPU pod) and EBS disk I/O (network-attached storage).

### EKS time breakdown (~105s)

| Component | Time | % of build |
|---|---|---|
| `bundle install` (download + compile) | ~76s | 72% |
| `npm install` (resolve + link) | ~15s | 14% |
| Docker layer export | ~13s | 12% |
| Base image + setup | ~2s | 2% |

Native extension compilation (pg, nio4r, bootsnap, sassc) dominates.
No caching strategy can skip compilation — only layer cache (unchanged
rebuild) or pre-compiled base images avoid it.

## Caching layers — what each one does

Sparkwing has four caching layers. Each addresses a different failure mode:

### 1. Docker layer cache (biggest win: ~99% speedup)

When nothing in the Dockerfile changes, every layer is cached and the build
completes in ~1s. This is Docker's default behavior — no sparkwing
configuration needed.

**Breaks when:** any file referenced by `COPY` changes (code, package.json,
Gemfile), Dockerfile changes, or build args change.

### 2. BuildKit cache mounts (second biggest: ~34% speedup)

```dockerfile
RUN --mount=type=cache,target=/root/.npm npm install
RUN --mount=type=cache,target=/usr/local/bundle bundle install
```

Cache mounts persist compiled artifacts and downloaded packages across builds
even when the layer cache is busted. The mount directory survives `--no-cache`
and Dockerfile changes — only `docker builder prune -af` clears it.

**Benchmark proof:**
- Images cached, cold mounts: 92s
- Images cached, warm mounts: 61s
- **Savings: 31s (34%)**

The savings come from skipping native extension recompilation (pg, nio4r, etc.),
not from skipping downloads.

**Breaks when:** the base image's runtime version changes (Ruby 3.3 → 3.2
compiled extensions are incompatible), or build cache is pruned.

### 3. Warm PVC pool (multiplier for cache mounts)

The controller pre-warms PVCs with Docker image layers. The DinD sidecar
on each runner pod mounts a warm PVC at `/var/lib/docker`. Since the warmer
is additive (never wipes the PVC), BuildKit cache mounts from previous job
runs persist on the PVC.

This means cache mounts survive across pipeline runs — not just within a
single build session. The first build on a PVC is cold; every subsequent
build benefits from warm mounts.

**Breaks when:** the PVC is recycled (new PVC from the pool), or the warmer
is run with a destructive reset (it currently doesn't — see `warmer.go`).

### 4. Dependency proxy (reliability + bandwidth, modest speed)

sparkwing-cache includes a package proxy that caches npm, pip, gem, Go
module, and Alpine package downloads in-cluster. Runners fetch packages
from the cache proxy instead of the public internet.

**Speed impact:** ~4s savings on a 104s EKS build. The proxy eliminates
network egress for cached packages, but since package downloads are only
~15% of total build time, the absolute savings are small on fast networks.

**Where the proxy matters:**
- **Reliability:** builds succeed when npmjs.org or rubygems.org have outages
  (stale-on-error fallback serves cached responses)
- **Bandwidth:** 290MB of cached packages not re-downloaded from the internet
  on every cold build, across every node
- **Constrained networks:** air-gapped clusters, metered egress, cross-region
  builds where registry latency is high
- **Concurrent builds:** 10 runners building simultaneously don't each fetch
  the same 200MB from upstream

**Does NOT help when:** the bottleneck is compilation (most builds), the
network is fast (AWS to npm CDN), or the packages aren't in the cache yet
(first build).

## Recommendations

### For pipeline authors (Dockerfile best practices)

Always use BuildKit cache mounts for package managers:

```dockerfile
# Job
RUN --mount=type=cache,target=/root/.npm npm ci

# Ruby
RUN --mount=type=cache,target=/usr/local/bundle bundle install

# Python
RUN --mount=type=cache,target=/root/.cache/pip pip install -r requirements.txt

# Go
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build ./...

# Rust
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/app/target \
    cargo build --release
```

### For using the proxy in Dockerfiles

```dockerfile
ARG PROXY_URL=""

# npm
RUN --mount=type=cache,target=/root/.npm \
    if [ -n "$PROXY_URL" ]; then npm config set registry ${PROXY_URL}/proxy/npm/; fi && \
    npm ci

# pip
RUN --mount=type=cache,target=/root/.cache/pip \
    pip install --index-url ${PROXY_URL:-https://pypi.org}/simple/ \
    --trusted-host sparkwing-cache.sparkwing.svc.cluster.local \
    -r requirements.txt

# apk (Alpine)
RUN if [ -n "$PROXY_URL" ]; then \
      sed -i "s|https://dl-cdn.alpinelinux.org|${PROXY_URL}/proxy/alpine|g" /etc/apk/repositories; \
    fi && apk add --no-cache git
```

The `PROXY_URL` build arg defaults to empty (no proxy). When running in the
cluster, the controller passes `http://sparkwing-cache.sparkwing.svc.cluster.local:80`.

### What NOT to optimize (and why)

These were all investigated and benchmarked. The savings are real but small
because **builds are CPU-bound, not network-bound**.

- **Base image pull time** — only ~2s on fast networks. The warm PVC pool
  already pre-pulls common images.
- **Package download caching (proxy/mounts)** — saves 2–7s on a 105s build.
  Downloads are ~15% of total time; compilation is ~72%. The proxy's value
  is reliability (builds work when registries are down) and bandwidth
  savings, not speed.
- **P2P artifact distribution** — overkill at current scale. The dependency
  access pattern (many small files, different per-repo) doesn't benefit from
  peer-to-peer the way large uniform blobs do.

### What WOULD help

- **More CPU for runner pods** — compilation is the bottleneck. Bumping from
  2 to 4 vCPU would let `bundle install --jobs=4` actually parallelize and
  could cut ~30% off build time.
- **Faster disk** — EBS adds ~6s to Docker layer exports vs local SSD.
  Local NVMe instances (c5d, m5d) or higher IOPS gp3 would help.
- **Pre-compiled base images** — a custom base image with common gems
  pre-installed eliminates compilation entirely for those deps. This is the
  nuclear option: build time drops to seconds, but you own the base image.

## Proxy service

The package proxy is part of sparkwing-cache. It supports six upstream
registries:

| Registry | Upstream | URL rewriting |
|---|---|---|
| npm | registry.npmjs.org | Yes (tarball URLs in metadata) |
| pypi | pypi.org | Yes (file URLs in simple index) |
| pythonhosted | files.pythonhosted.org | No |
| rubygems | rubygems.org | No |
| golang | proxy.golang.org | No |
| alpine | dl-cdn.alpinelinux.org | No |

**Cache policy:**
- Immutable content (.tgz, .whl, .gem, .zip, .jar, .apk): cached indefinitely
- Metadata (JSON, HTML): 10-minute TTL, stale-on-error fallback
- Background cleanup: removes expired entries hourly

**Endpoints:**
- `GET /proxy/{registry}/{path}` — cached reverse proxy
- `GET /stats` — cache size per registry
- `GET /health` — liveness/readiness

**Configuration (env vars):**
- `PROXY_CACHE_DIR` — cache directory (default: `/data/proxy`)
- `PROXY_CACHE_TTL` — metadata TTL (default: `10m`)
- `PROXY_MAX_AGE` — cleanup threshold for immutable entries (default: `168h`)
