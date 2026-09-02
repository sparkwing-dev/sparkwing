# Cache (Gitcache)

sparkwing-cache is sparkwing's in-cluster git cache, blob store, and
package proxy. It mirrors repositories from GitHub, serves git clones over
HTTP, stores SHA-scoped Git bundles and legacy code uploads, caches package
registry responses, and keeps itself fresh with a background fetch loop.

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

Off-cluster runners can set `SPARKWING_GITCACHE_URL` to
`https://<controller>/api/v1/gitcache`. The controller exposes admin-scoped
registration and read-only smart-Git proxy routes at that prefix and removes
the caller's bearer before contacting the internal cache. This keeps the raw
Git cache private while a laptop, desktop, or bare-metal runner uses outbound
HTTPS only. The dashboard ingress exposes this prefix as a machine-bearer route
even when browser login is required. A direct cache URL over a LAN, VPN, or
tailnet remains supported; direct binary and seed writes use only
`SPARKWING_CACHE_TOKEN`, never the runner's controller token.

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

## Dependency proxy defaults & egress

The cache pod also serves a pull-through package proxy at
`/proxy/{npm,pypi,pythonhosted,rubygems,golang,alpine}/...`. Immutable
artifacts (`.tgz`, `.whl`, `.gem`, `.zip`, `.apk`, ...) are cached for
7 days; mutable metadata for 10 minutes, with an expired entry served
stale if upstream is unreachable.

**Wired by default.** With `cache.enabled` (the chart default), the
runner container and every pod the runner spawns start with:

| Variable | Value |
|----------|-------|
| `GOPROXY` | `http://<cache>/proxy/golang\|https://proxy.golang.org,direct` |
| `npm_config_registry` | `http://<cache>/proxy/npm` |
| `PIP_INDEX_URL` | `http://<cache>/proxy/pypi/simple/` |
| `PIP_TRUSTED_HOST` | `<cache host>` |

Without this, every run re-downloads its whole dependency set from the
public internet -- on a managed cluster that is per-run NAT-gateway
egress you pay for twice, in bytes and in wall time.

Details worth knowing:

- `GOPROXY` separates the proxy from upstream with `|`, not `,`: `|`
  falls through on **any** proxy error, so a rolling cache pod slows
  builds instead of failing them. `,` only falls through on 404 and
  410.
- `direct` stays last, so `GOPRIVATE` modules keep resolving straight
  from your forge through the `~/.netrc` the runner entrypoint seeds
  from `GITHUB_TOKEN`. Private modules never transit the proxy.
- pip **ignores** a plain-HTTP index unless its host is also named in
  `PIP_TRUSTED_HOST`, and then fails with "no matching distribution"
  rather than falling back to PyPI -- so both variables ship together.
  The proxy rewrites the file URLs inside `/proxy/pypi/simple/` onto
  `/proxy/pythonhosted`, so downloads follow the index automatically.
- npm and pip have no upstream fallback of their own. If the cache is
  down their fetches fail until it is back -- the same exposure a run
  already has on its gitcache clone.
- `npm install` writes the proxy URL into `package-lock.json`'s
  `resolved` fields. Don't commit a lockfile generated inside the
  cluster, or a laptop `npm ci` will chase a host it cannot resolve.

**Opting out.** Set `cache.dependencyProxy.enabled=false` in the chart:
the env is not emitted and the runner is started with
`--dependency-proxy=off` so the pods it spawns skip the wiring too. On
the runner binary directly, `--dependency-proxy=off` (or
`SPARKWING_DEPENDENCY_PROXY_URL=off`); pass a URL instead to point at
some other pull-through mirror. Overriding a single ecosystem is a
`runner.extraEnv` entry with the same name -- a name you set there
suppresses the chart's default rather than colliding with it.

**Image pulls.** Runner pods are created with
`imagePullPolicy: IfNotPresent`; `--image-pull-policy` (or
`SPARKWING_IMAGE_PULL_POLICY`) accepts `Always`, `IfNotPresent`, or
`Never`. `Always` re-downloads the runner image on every node in the
DAG, which is the other per-run egress bill worth reading twice.

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

With `--working-tree`, the CLI captures tracked changes plus untracked
non-ignored files as a deterministic synthetic child commit. It seeds that
bundle before creating the trigger and never refreshes the origin for the
synthetic SHA. Capture rejects conflicts, submodules, sparse or shallow
checkouts, SHA-256 repositories, and configured Git content filters. The source
repository is not mutated.
The runner sees a clean detached checkout at the synthetic SHA rather than the
laptop's staged-versus-unstaged split.
The cache moves each accepted snapshot from the transient seed namespace into
`refs/sparkwing-workspace/*` and retains at most 128 distinct workspace refs per
repository. Re-seeding the same snapshot refreshes one ref. A new snapshot is
rejected before trigger admission when the repository is full; Sparkwing never
evicts an admitted snapshot to make room. Treat those refs as retained
unpublished source and keep the cache private. Before retrying a rejected
upload, delete workspace refs that no admitted run needs.

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
protocol, archive/file, artifact, proxy, and status routes on the raw cache are
unauthenticated. Keep that service private when it can contain uncommitted
source. The controller's `/api/v1/gitcache/git/...` proxy requires admin scope
and permits upload-pack reads only. Authenticated requests carry the token as:

```
Authorization: Bearer <SPARKWING_API_TOKEN>
```

Every caller presents the token, in-cluster ones included. Reaching the
cache through the k8s Service rather than the ingress proves nothing about
the caller, so requests to those endpoints without a valid bearer get 401
wherever they come from. Runners and the controller read the token from
`SPARKWING_CACHE_TOKEN`.

The cache refuses to start without a token. A laptop or test setup that
wants the endpoints open passes `--allow-unauthenticated` (or
`SPARKWING_CACHE_ALLOW_UNAUTHENTICATED=1`); the pod logs a warning at
startup so an unauthenticated deployment is visible.

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
| POST | `/sync/seed?repo=X&sha=Y[&workspace=1]` | Seed repo from a SHA-scoped git bundle; workspace mode caps retained refs (auth required) |

### Artifacts

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/artifacts/<jobID>?path=X` | Upload artifact |
| GET | `/artifacts/<jobID>` | List artifacts |
| GET | `/artifacts/<jobID>?glob=X` | Download matching artifacts |

### Binary & Dependency Cache

A `/bin/<name>` key folds the repository's `.sparkwing/` source inputs, not the
binary's content, so the cache records the sha-256 of each uploaded body and the
writing principal's token fingerprint beside the blob and serves that digest on
every download. Clients hash what they download and discard a mismatch before
the binary lands, and treat a response without a digest as a miss.

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/bin/<name>` | Download cached binary; carries `Digest: sha-256=<base64>` and `ETag` (auth required) |
| PUT | `/bin/<name>` | Upload binary to cache; returns its digest (auth required) |
| GET | `/cache/<key>` | Download cached dependency archive (auth required) |
| HEAD | `/cache/<key>` | Check if cache entry exists (auth required) |
| PUT | `/cache/<key>` | Upload dependency archive to cache (auth required) |

### Package Registry Proxy

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET, HEAD | `/proxy/<registry>/<path...>` | Pull-through fetch; `<registry>` is one of `npm`, `pypi`, `pythonhosted`, `rubygems`, `golang`, `alpine` |
| GET | `/stats` | Per-registry cached file count + bytes |

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
| `SPARKWING_API_TOKEN` | Bearer token for the blob and sync endpoints. Required unless auth is disabled |
| `SPARKWING_CACHE_ALLOW_UNAUTHENTICATED` | Start without a token, leaving the blob and sync endpoints open |
| `GITCACHE_REPOS` | Comma-separated `name=url` pairs for auto-registration |
| `FETCH_INTERVAL` | Background fetch interval (default: `30s`) |
| `FETCH_FRESH_WINDOW` | How long a successful fetch lets request handlers skip their own fetch (default: `15s`; negative disables) |
| `RECLONE_COOLDOWN` | Minimum gap between `/archive` recovery reclones of one repo (default: `1h`; negative disables) |
| `DATA_DIR` | Override data root (default: `/data`) |
| `PORT` | Listen port (default: `8090`) |

The server variables above configure the cache pod. On the client side,
`SPARKWING_GITCACHE` forces a specific gitcache base URL for git clones:
set it to a reachable cache server and sparkwing routes clones through
that server instead of probing for a local one. Empty (the default)
leaves sparkwing to auto-detect, falling back to a direct clone when no
gitcache answers.

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
