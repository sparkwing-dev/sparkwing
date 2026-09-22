# Cache (Gitcache)

sparkwing-cache is sparkwing's in-cluster git cache, blob store, and
package proxy. It mirrors repositories from GitHub, serves git clones over
HTTP, stores SHA-scoped Git bundles and legacy code uploads, caches package
registry responses, fetches a commit it lacks on demand, and keeps mirrors
that were used recently warm.

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

The SDK's `git.Clone` is the exception: it addresses a repository by the hash of
its clone URL, not by an alias, so a repository registered only under an alias is
served to pipelines and cloned from upstream by `git.Clone`. Register it under
the hashed name as well to serve both.

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
curl -X POST -H "Authorization: Bearer $SPARKWING_CACHE_TOKEN" \
  "http://sparkwing-cache:8090/git/register?name=gitops&repo=git@github.com:user/repo.git"
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
curl -X POST -H "Authorization: Bearer $SPARKWING_CACHE_TOKEN" \
  "http://gitcache:8090/sync/seed?repo=git@github.com:user/repo.git&sha=$sha" \
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

Off-cluster agents default `gitcache` to
`https://<controller>/api/v1/gitcache`. During node execution the runner
narrows that URL to `/api/v1/runs/<run>/gitcache`; the `nodes.claim` bearer may
register and read only the repository of its live run claim. The controller
removes that bearer before contacting the internal cache. The unscoped
`/api/v1/gitcache/git/...` routes remain admin-only. This keeps the raw cache
private while a workstation or server uses outbound HTTPS only. The dashboard
ingress exposes these routes to machine bearers without accepting browser
session credentials. A direct cache URL over a LAN, VPN, or tailnet remains
supported through `agent.yaml` `gitcache`; the agent reaches it with a
per-run [cache grant](#cache-grants).

## On-Demand Fetch

Every clone refreshes the mirror before the cache advertises its refs,
so a run triggered seconds after a push checks out that push instead of
being told the ref is not ours. This is the only path that reaches
origin for a clone: `git fetch <sha>` reads `info/refs` first, and by
the time `git upload-pack` answers, the commit is there.

The refresh runs only for `git-upload-pack`, and only when the mirror's
last successful fetch is older than `FETCH_FRESH_WINDOW` (default 10
seconds). That window is the bound on what a caller can spend: **any
token that can reach these routes can force at most one origin fetch per
repository per ten seconds**, however many clones it starts. A push
advertisement (`git-receive-pack`) refreshes nothing, because the cache
refuses the push anyway.

A commit origin does not have on any branch costs one fetch and then the
same `not our ref` refusal git has always sent. Known limit: a want for a
fork or pull-request head that origin keeps on a non-head ref is in that
class, since the mirror fetches `refs/heads/*` only. Such a checkout
pays a fetch and is still refused; seed it with `POST /sync/seed`.

A failed fetch is not fatal to a clone: the cache logs it and serves the
refs it has, so a broken SSH key degrades freshness rather than stopping
every build.

`sparkwing.gitcache.fetch_duration` counts and times every mirror fetch,
labeled `reason` (`on_demand` or `keep_warm`) and `failed`.

## Keep-Warm Pass

`FETCH_INTERVAL` is off by default (`0`). Set it, and the cache refreshes
the mirrors a request touched in the last hour on that cadence, leaving
every other mirror alone: a repository nobody is building costs origin
nothing, which is what matters on a hosted cache holding many customers'
repositories. It buys an active repository a warm mirror, so a clone
skips its own fetch and ancestor negotiation for incremental uploads
succeeds more often.

Correctness never depends on it. A fetch that fails backs off from the
interval and doubles to ten minutes, so a repository whose credentials
broke does not re-dial origin every cycle.

## Egress Guards

Both guards below exist to stop the cache re-downloading from GitHub
more than it has to. In a cluster behind NAT, every avoided fetch is
avoided egress cost.

### Fetch freshness throttle

`/archive`, `/file`, `/tree-hash`, `/branch-contains`, and
`/sync/negotiate` used to run their own `git fetch` on **every** request,
so a webhook burst multiplied GitHub traffic without making anything
fresher.

Now a successful fetch (from the keep-warm pass, from a request, from a
clone reading `info/refs`, or from `/git/refresh`) marks the repo fresh
for `FETCH_FRESH_WINDOW` (default 10s), and requests inside that window
serve straight from the mirror. The same window bounds the clone path, so
ten seconds is also the worst-case staleness a checkout can see and the
most origin traffic one repository can be made to spend.

`POST /git/refresh` **is not throttled**. It exists to close the
`git push && sparkwing pipeline trigger` race, so it always performs a
real fetch. Use it (as the CLI does) whenever a caller needs a just-pushed
SHA immediately. Cloning a repo that is not cached yet is not throttled by
the freshness window; the reclone cooldown bounds a clone that keeps
failing (below).

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

The same cooldown bounds cloning a mirror that is missing, on `/archive`
and on the `/git/<name>` path a runner clones through. A reclone deletes
the mirror before it clones, so a reclone whose own clone fails leaves no
mirror at all, and without the bound every later request re-downloaded the
whole repository. A repo whose mirror is absent is still cloned once; a
second attempt inside `RECLONE_COOLDOWN` that still finds no mirror is
refused, naming the remaining cooldown and the error the last attempt hit.
A successful fetch or clone clears the record, and so does re-registering
the repo, which is the deliberate way out.

Health problems to expect from `GET /health`:

| Problem text | What it means |
|--------------|---------------|
| `repo <hash>: recovery reclone ran N times in 24h -- persistent fetch failure; ...` | The mirror keeps failing to fetch and reclones are papering over it. Read the `recovery reclone:` log line for the git error, fix the cause (often a conflicting ref -- `git remote prune origin`, or delete the conflicting ref inside `/data/repos/<hash>.git`), then let the background loop resume. |
| `repo <hash>: <friendly fetch error>` | The last fetch of this mirror failed (SSH, DNS, timeout, fork exhaustion), so clones are being served older refs. |
| `repo <hash>: clone failed: ...` / `auto-clone failed: ...` | A mirror that was missing could not be cloned. The repo is on the clone cooldown until it expires or the repo is re-registered; seeding via `POST /sync/seed` also works when upstream is unreachable. |

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
Capture also records the commit HEAD shares with the origin default branch. The
runner fetches that commit through the same cache, names it with the
remote-tracking ref it had locally, and grafts the parentless snapshot commit
onto it with a `refs/replace/` entry, so a step scoped by `git merge-base
origin/main HEAD` reads the range the laptop would. `git rev-parse HEAD` still
answers with the snapshot SHA. A checkout with no origin remote records no
baseline, and a step that needs one reports the ref it cannot resolve. A source
that advertises only the snapshot, which is what a local fleet run serves, has
no baseline to give and the runner skips the fetch. A mirror that has not caught
up yet is retried, and a baseline that stays unreachable is named in the run's
log so the widened scope has a stated cause.
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
`cache-` subdomain. Every route except `/health`, `/metrics`, `/stats`, and the
package proxy under `/proxy/` requires a bearer token, on reads as well as
writes: the git protocol and registration routes (`/git/...`), the source read
routes (`/archive`, `/file`, `/tree-hash`, `/branch-contains`, `/repos`),
`/artifacts/...`, and the blob and sync routes (`/bin/...`, `/cache/...`,
`/upload`, `/uploads/...`, `/sync/negotiate`, `/sync/seed`). The package proxy
stays open because Go, npm, and pip fetch through it without a credential;
it serves upstream registry bytes, not repository content. The controller's
`/api/v1/gitcache/git/...` proxy requires admin scope and permits upload-pack
reads only. Authenticated requests carry the token as:

```
Authorization: Bearer <SPARKWING_API_TOKEN>
```

Every caller presents the token, in-cluster ones included. Reaching the
cache through the k8s Service rather than the ingress proves nothing about
the caller, so requests to those endpoints without a valid bearer get 401
wherever they come from. The controller and an operator's own shell read the
token from `SPARKWING_CACHE_TOKEN`; runners never hold it and present a cache
grant instead.

`POST /git/register` accepts a `name` of 1-64 alphanumeric, dash, underscore,
or dot characters, and refuses to repoint a name that is already registered to
a different repository unless the request carries the token. Registering the
same name to the same URL stays idempotent.

### Cache grants

A multi-team controller gives runners a cache grant instead of the cache's
token. `POST /api/v1/runs/<run>/cache-grant` answers `{grant, team,
expires_at}`: a bearer the controller signs with its own cache token, naming
the run's team and valid for six hours. The cache verifies it without calling
the controller and confines the request to that team:

- `/bin/...`, `/cache/...` and `/artifacts/...` read and write the team's own
  tree under `<data-dir>/teams/<team>/`, so two teams naming the same key never
  see or replace each other's bytes. A team's bins count toward the store
  ceiling.
- `/git/register` and `/git/<name>/...` reach only an `https` repository
  registered under the name `repo-<sha256 of the URL>`, the name runners
  already derive. The mirrors are shared, so a grant never clones through the
  cache's SSH key and cannot squat a name another team's runner will clone.
- Every other route (`/sync/...`, `/git/refresh`, `/archive`, `/file`,
  `/tree-hash`, `/branch-contains`, `/repos`, `/upload`, `/uploads/...`,
  `/admin/...`) refuses a grant with 401.

The operator token keeps its unscoped access, and a cache started with
`--allow-unauthenticated` accepts no grants because it has no key to verify
them with.

Every response carries `X-Content-Type-Options: nosniff`, and artifact
downloads carry `Content-Type: application/octet-stream` with
`Content-Disposition: attachment`, so a stored HTML or SVG artifact cannot
execute in a browser on the cache's origin.

The cache refuses to start without a token. A laptop or test setup that
wants the endpoints open passes `--allow-unauthenticated` (or
`SPARKWING_CACHE_ALLOW_UNAUTHENTICATED=1`); the pod logs a warning at
startup so an unauthenticated deployment is visible.

### Network policy

The runner-bundle chart ships a default-deny ingress NetworkPolicy for the
cache pod, admitting four peers on the cache port: the release's runner,
controller, and dashboard pods, plus the Job pods the Kubernetes runner
backend creates. Set `networkPolicy.enabled=false` to drop it. Point
`networkPolicy.controllerPodSelector` and `networkPolicy.webPodSelector` at
your own pod labels when the controller or the dashboard runs under a
different release; both default to this release's own pods. Override
`networkPolicy.runnerJobPodSelector`, which defaults to
`app.kubernetes.io/name: sparkwing-runner`, when the Job template carries
other labels. Add peers through `networkPolicy.extraIngress` for an
out-of-cluster runner pool. The chart refuses to render a non-`ClusterIP`
`cache.service.type` unless `controller.tokenSecret.name` is set and
`cache.allowUnauthenticated` is false, so a published cache always demands a
bearer.

## API Endpoints

### Git Protocol (read-only)

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/git/register?name=X&repo=Y` | Register a repo name; `name` is 1-64 alphanumeric/dash/underscore/dot chars (auth required) |
| GET | `/git/<name>/info/refs?service=git-upload-pack` | Clone/fetch discovery (auth required) |
| POST | `/git/<name>/git-upload-pack` | Clone/fetch data (auth required) |
| POST | `/git/<name>/git-receive-pack` | **Returns 403** (read-only) |
| POST | `/git/refresh?name=X` (or `?repo=Y`) | Synchronous fetch of one bare repo (auth required) |

### Archives & Files

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/archive?repo=X&branch=Y` | Download repo as tar.gz (auth required) |
| GET | `/file?repo=X&branch=Y&path=Z` | Get a single file (auth required) |
| GET | `/tree-hash?repo=X&branch=Y&path=Z` | Content-addressable hash (auth required) |
| GET | `/branch-contains?repo=X&branch=Y&commit=Z` | Check if commit is on branch (auth required) |

### Uploads (Code Sync)

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/upload` | Upload a tarball (auth required) |
| POST | `/upload?repo=X&base=Y` | Incremental upload on base commit |
| GET | `/uploads/<id>` | Download uploaded tarball (auth required) |
| POST | `/sync/negotiate` | Find common ancestor (auth required) |
| POST | `/sync/seed?repo=X&sha=Y[&workspace=1]` | Seed repo from a SHA-scoped git bundle; workspace mode caps retained refs at 128 and archives refs past `WORKSPACE_SEED_MAX_AGE` (auth required) |

### Artifacts

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/artifacts/<jobID>?path=X` | Upload artifact (auth required) |
| GET | `/artifacts/<jobID>` | List artifacts (auth required) |
| GET | `/artifacts/<jobID>?glob=X` | Download matching artifacts as an attachment (auth required) |

`<jobID>` must be one path segment of 1-128 alphanumeric, dash, underscore, or
dot characters. Anything else is rejected with 400 before a path is built.

### Binary & Dependency Cache

A `/bin/<name>` key folds the repository's `.sparkwing/` source inputs, not the
binary's content, so the cache records the sha-256 of each uploaded body and the
writing principal's token fingerprint beside the blob and serves that digest on
every download. Clients hash what they download and discard a mismatch before
the binary lands, and treat a response without a digest as a miss.

The digest covers the window between the upload and the download: bytes altered
in transit, or altered on the cache's disk without also rewriting the recorded
digest, are discarded and the client recompiles. It says nothing about who
uploaded the binary, because the cache derives the digest from the body it was
handed. The bearer token on `PUT /bin/<name>` is what keeps an attacker from
uploading a poisoned binary along with a digest that attests it.

A `<name>` is one to four hyphen-joined groups of eight hex digits, optionally
suffixed `.sha256`. That suffix is the digest sidecar a cache-backed artifact
store writes beside the blob: it reaches the cache through the generic artifact
store interface, which carries no `Digest` header, so it keeps its own copy of
the digest under its own key.

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
| GET | `/repos` | List registered repos (auth required) |

## Deployment

The cache runs as a Deployment in the `sparkwing` namespace:

- **Git**: 2.24 or newer on cache and runner hosts, because every ref
  argument is passed after `--end-of-options`
- **Image**: `sparkwing-cache`
- **Port**: 8090 (service port 80)
- **Storage**: PVC at `/data`
- **SSH**: Optional, mounted at `/etc/ssh-key` from `ssh-key` secret
- **Ingress**: your dashboard host's `cache-` subdomain

### Environment Variables

| Variable | Description |
|----------|-------------|
| `SPARKWING_API_TOKEN` | Bearer token for every route outside `/health`, `/metrics`, `/stats`, and `/proxy/`. Required unless auth is disabled |
| `SPARKWING_CACHE_ALLOW_UNAUTHENTICATED` | Start without a token, leaving those routes open |
| `GITCACHE_REPOS` | Comma-separated `name=url` pairs for auto-registration |
| `FETCH_INTERVAL` | Cadence of the keep-warm pass over mirrors a request touched in the last hour (default: `0`, the pass is off) |
| `FETCH_FRESH_WINDOW` | How long a successful fetch lets request handlers skip their own fetch, bounding a caller to one origin fetch per repository per window (default: `10s`; negative disables) |
| `RECLONE_COOLDOWN` | Minimum gap between `/archive` recovery reclones, and between clone-if-missing attempts, for one repo (default: `1h`; negative disables) |
| `WORKSPACE_SEED_MAX_AGE` | How long a working-tree snapshot ref is retained before the next seed archives it under `refs/sparkwing-workspace-archive/`, where it survives another seven times this window so a retry still finds its snapshot (default: `24h`; negative disables expiry) |
| `DATA_DIR` | Override data root (default: `/data`) |
| `PORT` | Listen port (default: `8090`) |

The server variables above configure the cache pod. On the client side,
`SPARKWING_GITCACHE` forces a specific gitcache base URL for git clones:
set it to a reachable cache server and sparkwing routes clones through
that server instead of probing for a local one. `SPARKWING_GITCACHE_URL`,
the variable the runner chart stamps on every runner pod, is the fallback
when `SPARKWING_GITCACHE` is empty, so a chart-deployed runner already
names its cache. With neither set, sparkwing auto-detects a cache on
`localhost:18090` and falls back to a direct clone when none answers.

A clone through a named cache carries the run's `SPARKWING_CACHE_GRANT`, or
else `SPARKWING_CACHE_TOKEN`, as its bearer, so a cache that guards its git
routes still serves it. The bearer
travels in the environment as a cache-scoped header, never on the command
line, and never goes to the auto-detected cache: only an operator naming
the cache in one of those two variables authorizes sending a credential to
it. Redirects are off for the cache URL so the bearer cannot follow a
request to another host, which means a cache behind a redirecting ingress
must be named by the URL it finally serves on. A cache that fails the
clone for any reason sends it to the upstream remote instead, with one
line on stderr naming the cache. A later `git fetch` through
`sparkwing/git` reuses the same bearer when the checkout's `origin` is
under the named cache.

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
