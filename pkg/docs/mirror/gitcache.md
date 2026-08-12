# Cache (Gitcache)

sparkwing-cache is sparkwing's in-cluster git cache, blob store, and
package proxy. It mirrors repositories from GitHub, serves git clones
over HTTP, stores uploaded code tarballs, caches package registry
responses, and keeps itself fresh with a background fetch loop.

The cache is **read-only for git** - pipelines clone from it but push
directly to GitHub. This eliminates a class of divergence bugs where
the cache's bare repos would drift from upstream.

## Architecture

```
                   ┌─────────────┐
                   │   GitHub    │
                   └──────┬──────┘
                          │ fetch (background, every 30s)
                   ┌──────▼──────┐
 sparkwing CLI ────────►│   cache     │◄──── runner (clone + pkg proxy)
 (eager refresh)   │  (read-only │
                   │   + blobs   │
                   │   + proxy)  │
                   └─────────────┘

 runner ──── push gitops ────► GitHub (direct, via GITHUB_TOKEN PAT)
```

**Reads** (clone, fetch, file, archive) go through the cache - fast,
in-cluster, no GitHub rate limits.

**Writes** (gitops deploy push) go directly to GitHub via HTTPS + PAT.
Runners have `GITHUB_TOKEN` from the `github-config` k8s secret.

## Repo Registration

Repos are registered by name so pipelines can clone them as
`http://gitcache/git/<name>` without knowing the full URL.

### Auto-registration (recommended)

Set `GITCACHE_REPOS` env var on the cache deployment:

```yaml
env:
  - name: GITCACHE_REPOS
    value: "gitops=git@github.com:user/gitops.git,app=git@github.com:user/app.git"
```

On startup, the cache registers the name-to-URL mappings and eagerly
clones each repo (best-effort). If a startup clone fails (e.g. no SSH
access), that repo falls back to being cloned on-demand when first
requested or seeded manually. If the PVC is nuked, repos are re-cloned
automatically on next startup or access.

### Manual registration

```bash
curl -X POST "http://sparkwing-cache:8090/git/register?name=gitops&repo=git@github.com:user/repo.git"
```

### Seeding (no SSH required)

If the cache doesn't have SSH access, seed from a machine that does:

```bash
git clone --bare git@github.com:user/repo.git /tmp/repo-seed
cd /tmp/repo-seed
sha=$(git rev-parse HEAD)
git update-ref "refs/sparkwing-seed/$sha" "$sha"
git bundle create /tmp/repo.bundle "refs/sparkwing-seed/$sha"
git update-ref -d "refs/sparkwing-seed/$sha"
curl -X POST "http://gitcache:8090/sync/seed?repo=git@github.com:user/repo.git&sha=$sha" \
  --data-binary @/tmp/repo.bundle
```

## Operator Discovery

Some operator flows -- the eager-refresh on
`sparkwing pipeline trigger --profile <controller-profile>` and the
profile health probe -- talk to the cache pod directly over HTTP. They
discover the cache pod's URL from the controller -- no per-profile
configuration required on the operator side.

Wire it up on the controller deployment:

```yaml
env:
  - name: CACHE_POD_URL
    value: "https://cache-sparkwing.example.dev"
```

(Or pass `--cache-pod-url=https://cache-sparkwing.example.dev` on
the controller's command line.) The controller announces this URL
via `GET /api/v1/services`; operator CLIs fetch it once per session
and cache in-process.

If `CACHE_POD_URL` is unset the announce endpoint returns 404. The
profile health probe then reports a warning (`controller announced no
cache pod URL`) instead of a pass, and eager-refresh falls back to the
controller's gitcache proxy routes (`POST /api/v1/gitcache/refresh`,
then a SHA-scoped bundle seed via `POST /api/v1/gitcache/seed`); if
those also fail the CLI prints a note and the runner retries on a stale
SHA. The controller serves the proxy routes only when started with
`--cache-url` (or `SPARKWING_CACHE_URL`) pointing at the in-cluster
cache Service, so set both: `--cache-pod-url` for the
externally-reachable URL operators hit directly, `--cache-url` for the
controller-to-cache proxy target.

## Background Fetch

The cache periodically fetches upstream for all registered bare repos
(default: every 30 seconds, configurable via `FETCH_INTERVAL` env var).

This keeps repos fresh so that:

- Runner clones see recent commits without cold-start fetches
- Ancestor negotiation for incremental uploads succeeds more often

## Egress Guards

Both guards below exist to stop the cache re-downloading from GitHub
more than it has to. In a cluster behind NAT, every avoided fetch is
avoided egress cost.

### Fetch freshness throttle

`/archive`, `/file`, `/tree-hash`, `/branch-contains`, and
`/sync/negotiate` used to run their own `git fetch` on **every** request.
The background loop already fetches every repo every 30 seconds, so a
webhook burst multiplied GitHub traffic without making anything fresher.

Now a successful fetch (from the background loop, from a request, or from
`/git/refresh`) marks the repo fresh for `FETCH_FRESH_WINDOW`
(default 15s), and requests inside that window serve straight from the
mirror. Worst-case staleness is unchanged in practice: it is still bounded
by the background fetch interval.

`POST /git/refresh` **is not throttled**. It exists to close the
`git push && sparkwing pipeline trigger` race, so it always performs a
real fetch. Use it (as the CLI does) whenever a caller needs a just-pushed
SHA immediately. Cloning a repo that is not cached yet is also unaffected.

### Recovery reclone circuit breaker

When `/archive` cannot fetch a repo, it can recover by deleting the mirror
and cloning it again. That is the right move for a corrupted mirror and
the wrong move for a fetch that will keep failing -- a conflicting local
ref after an upstream branch rename (local `foo` vs remote `foo/bar`), for
example, made every archive request re-download the entire repository.

A reclone is now allowed at most once per `RECLONE_COOLDOWN` (default
1h) per repo. Inside the cooldown, a failed fetch returns `502` with the
underlying git error, the remaining cooldown, and a pointer to the fix.
Each reclone logs loudly with the `recovery reclone:` prefix and the repo
hash, and increments the `sparkwing.gitcache.recovery_reclones` counter.

Health problems to expect from `GET /health`:

| Problem text | What it means |
|--------------|---------------|
| `repo <hash>: recovery reclone ran N times in 24h -- persistent fetch failure; ...` | The mirror keeps failing to fetch and reclones are papering over it. Read the `recovery reclone:` log line for the git error, fix the cause (often a conflicting ref -- `git remote prune origin`, or delete the conflicting ref inside `/data/repos/<hash>.git`), then let the background loop resume. |
| `repo <hash>: <friendly fetch error>` | The most recent background fetch failed (SSH, DNS, timeout, fork exhaustion). Unchanged behavior. |

An operator who wants the old per-request behavior back can set
`FETCH_FRESH_WINDOW` and/or `RECLONE_COOLDOWN` to a negative duration
(e.g. `-1s`) to disable that guard.

## Code delivery on remote triggers

`sparkwing pipeline trigger <pipeline> --profile prod` triggers by commit
SHA: the CLI sends the branch + SHA to the controller, and the runner
clones that SHA from the cache. To close the
`git push && sparkwing pipeline trigger` race -- where the cache hasn't yet
mirrored the just-pushed commit -- the CLI fires a best-effort eager
refresh of the repo (`POST /git/refresh`) before it creates the trigger,
falling back to a SHA-scoped bundle seed (`POST /sync/seed`) if the
refresh fails; the runner also retries on a stale SHA.

```
sparkwing CLI -> cache POST /git/refresh     (eager mirror of the pushed SHA)
  (on failure) -> cache POST /sync/seed      (bundle the SHA from the local checkout)
sparkwing CLI -> controller /api/v1/triggers (branch + SHA)
runner        -> cache /git/<name>           (clone at SHA)
```

The cache also exposes tarball-upload and ancestor-negotiation endpoints
(`/upload`, `/uploads/<id>`, `/sync/negotiate`) for code-sync flows; see
the API table below.

## GitOps Deployment Flow

```
1. Runner builds Docker image from source
2. Runner pushes image to a registry (ECR, GCR, Docker Hub, etc.)
3. Runner clones the gitops repo from the cache (read cache)
4. Runner updates kustomization.yaml with new image tag
5. Runner pushes the gitops repo directly to GitHub (HTTPS + PAT)
6. ArgoCD detects change, syncs cluster
```

The runner uses `GITHUB_TOKEN` (from `github-config` k8s secret) to
authenticate the push. The PAT needs write access to the gitops repo.

## Auth

The cache is exposed externally via ingress at your dashboard host's
`cache-` subdomain. The blob and sync endpoints require a bearer token
-- `/bin/...`, `/cache/...`, `/upload`, `/uploads/...`,
`/sync/negotiate`, and `/sync/seed` -- on reads as well as writes. Git
protocol, archive/file, artifact, proxy, and status routes are
unauthenticated. Authenticated requests carry the token as:

```
Authorization: Bearer <SPARKWING_API_TOKEN>
```

In-cluster requests (from controller, runners) skip auth - they reach
the cache via the k8s Service without the `X-Forwarded-For` header that
the ingress sets.

## API Endpoints

### Git Protocol (read-only)

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/git/register?name=X&repo=Y` | Register a repo name |
| GET | `/git/<name>/info/refs?service=git-upload-pack` | Clone/fetch discovery |
| POST | `/git/<name>/git-upload-pack` | Clone/fetch data |
| POST | `/git/<name>/git-receive-pack` | **Returns 403** (read-only) |
| POST | `/git/refresh?name=X` (or `?repo=Y`) | Synchronous fetch of one bare repo (eager refresh) |

### Archives & Files

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/archive?repo=X&branch=Y` | Download repo as tar.gz |
| GET | `/file?repo=X&branch=Y&path=Z` | Get a single file |
| GET | `/tree-hash?repo=X&branch=Y&path=Z` | Content-addressable hash |
| GET | `/branch-contains?repo=X&branch=Y&commit=Z` | Check if commit is on branch |

### Uploads (Code Sync)

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/upload` | Upload a tarball (auth required) |
| POST | `/upload?repo=X&base=Y` | Incremental upload on base commit |
| GET | `/uploads/<id>` | Download uploaded tarball (auth required) |
| POST | `/sync/negotiate` | Find common ancestor (auth required) |
| POST | `/sync/seed?repo=X&sha=Y` | Seed repo from a SHA-scoped git bundle (auth required) |

### Artifacts

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/artifacts/<jobID>?path=X` | Upload artifact |
| GET | `/artifacts/<jobID>` | List artifacts |
| GET | `/artifacts/<jobID>?glob=X` | Download matching artifacts |

### Binary & Dependency Cache

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/bin/<name>` | Download cached binary (auth required) |
| PUT | `/bin/<name>` | Upload binary to cache (auth required) |
| GET | `/cache/<key>` | Download cached dependency archive (auth required) |
| HEAD | `/cache/<key>` | Check if cache entry exists (auth required) |
| PUT | `/cache/<key>` | Upload dependency archive to cache (auth required) |

### Status

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/health` | Health check (`{"status":"ok"}`) |
| GET | `/repos` | List registered repos |

## Deployment

The cache runs as a Deployment in the `sparkwing` namespace:

- **Image**: `sparkwing-cache`
- **Port**: 8090 (service port 80)
- **Storage**: PVC at `/data`
- **SSH**: Optional, mounted at `/etc/ssh-key` from `ssh-key` secret
- **Ingress**: your dashboard host's `cache-` subdomain

### Environment Variables

| Variable | Description |
|----------|-------------|
| `SPARKWING_API_TOKEN` | Bearer token for write endpoint auth |
| `GITCACHE_REPOS` | Comma-separated `name=url` pairs for auto-registration |
| `FETCH_INTERVAL` | Background fetch interval (default: `30s`) |
| `FETCH_FRESH_WINDOW` | How long a successful fetch lets request handlers skip their own fetch (default: `15s`; negative disables) |
| `RECLONE_COOLDOWN` | Minimum gap between `/archive` recovery reclones of one repo (default: `1h`; negative disables) |
| `DATA_DIR` | Override data root (default: `/data`) |
| `PORT` | Listen port (default: `8090`) |

### Data directories

| Path | Contents |
|------|----------|
| `/data/repos/` | Bare git repositories (named by content hash) |
| `/data/archives/` | Cached repo tarballs |
| `/data/uploads/` | Uploaded code tarballs |
| `/data/artifacts/` | Job output artifacts |
| `/data/bins/` | Compiled pipeline binary cache |
| `/data/cache/` | Dependency-archive cache (gems, node_modules, etc.) |
| `/data/proxy/` | Package-registry proxy cache (npm, PyPI, Go, etc.) |
| `/data/repo-names.json` | Friendly name → URL registry |
