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
| Cold build | 105s | -- |
| Layer cache (nothing changed) | 0s | 105s (100%) |
| --no-cache, cache mounts warm | 101s | 4s (4%) |
| Dep change + warm cache mounts | 101s | 2s vs cold dep change |

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
No caching strategy can skip compilation -- only layer cache (unchanged
rebuild) or pre-compiled base images avoid it.

## Caching layers -- what each one does

Docker provides two caching layers. Each addresses a different failure mode:

### 1. Docker layer cache (biggest win: ~99% speedup)

When nothing in the Dockerfile changes, every layer is cached and the build
completes in ~1s. This is Docker's default behavior -- no sparkwing
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
and Dockerfile changes -- only `docker builder prune -af` clears it.

**Benchmark proof:**

- Images cached, cold mounts: 92s
- Images cached, warm mounts: 61s
- **Savings: 31s (34%)**

The savings come from skipping native extension recompilation (pg, nio4r, etc.),
not from skipping downloads.

**Breaks when:** the base image's runtime version changes (Ruby 3.3 → 3.2
compiled extensions are incompatible), or build cache is pruned.

## Recommendations

### For pipeline authors (Dockerfile best practices)

Always use BuildKit cache mounts for package managers:

```dockerfile
# Node
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

### What NOT to optimize (and why)

These were all investigated and benchmarked. The savings are real but small
because **builds are CPU-bound, not network-bound**.

- **Base image pull time** -- only ~2s on fast networks.
- **P2P artifact distribution** -- overkill at current scale. The dependency
  access pattern (many small files, different per-repo) doesn't benefit from
  peer-to-peer the way large uniform blobs do.

### What WOULD help

- **More CPU for runner pods** -- compilation is the bottleneck. Bumping from
  2 to 4 vCPU would let `bundle install --jobs=4` actually parallelize and
  could cut ~30% off build time.
- **Faster disk** -- EBS adds ~6s to Docker layer exports vs local SSD.
  Local NVMe instances (c5d, m5d) or higher IOPS gp3 would help.
- **Pre-compiled base images** -- a custom base image with common gems
  pre-installed eliminates compilation entirely for those deps. This is the
  nuclear option: build time drops to seconds, but you own the base image.

## Native frontend exports

The repository's candidate installer requests `bin/install.sh --reuse-web`.
This calls `bin/build-web.sh --reuse`, which can reuse the existing static export
without npm installation, Next compilation, or recopying unchanged assets.
With `pnpm` available, ordinary calls to either script build fresh. Both build modes use
production settings and explicitly install development dependencies needed by
TypeScript and PostCSS. The Next compiler cache remains available for rebuilds.

If `pnpm` is absent, `bin/install.sh` copies a prebuilt `web/out` into the
embedded dashboard directory. If `web/out` is absent, it keeps an existing
embedded dashboard; with neither asset available, installation still builds the
CLI and runner and warns that the dashboard is unavailable in the CLI.
Install `pnpm` and rerun the script to build fresh dashboard assets.

Reuse is local to the checkout. A private receipt under
`internal/web/.build-state/` records hashes of frontend source and configuration,
including untracked files and ignored dotenv files, builder scripts, Node/npm
identity, effective npm configuration, and relevant build environment values.
It also records a hash of every published export file. A changed input or a
missing, edited, or extra output file triggers a fresh build. Environment values
are hashed, never written into the receipt. Before recording proof, inputs are
checked again; a mismatch fails the build.

Generated frontend directories are excluded only at the web root. Changes to
frontend build inputs outside this boundary must extend the proof before reuse
can cover them. Go source is outside the frontend proof and retains its native
compiler cache. The normal Go build and candidate provenance checks still run.

The native installer and direct frontend builder coordinate with a file lock
through Go compilation. Hosts without `flock`, unsupported input trees, and
custom Node preload or npm script-shell configuration build fresh without
recording reusable proof. The receipt lives outside the embedded export.
This is local iteration reuse; it makes no reproducibility claim about remote
font downloads or other external build services.
