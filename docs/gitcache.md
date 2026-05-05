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
 wing CLI ────────►│   cache     │◄──── runner (clone + pkg proxy)
 (upload tarball)  │  (read-only │
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

On startup, the cache registers the name-to-URL mappings. Repos are
cloned on-demand when first requested (e.g. via `/archive` or `/upload`).
If the PVC is nuked, repos are re-cloned automatically on next access.

### Manual registration

```bash
curl -X POST "http://sparkwing-cache:8090/git/register?name=gitops&repo=git@github.com:user/repo.git"
```

### Seeding (no SSH required)

If the cache doesn't have SSH access, seed from a machine that does:

```bash
git clone --bare git@github.com:user/repo.git /tmp/repo-seed
cd /tmp/repo-seed && git bundle create /tmp/repo.bundle --all
curl -X POST "http://gitcache:8090/sync/seed?repo=git@github.com:user/repo.git" \
  --data-binary @/tmp/repo.bundle
```

## Background Fetch

The cache periodically fetches upstream for all registered bare repos
(default: every 30 seconds, configurable via `FETCH_INTERVAL` env var).

This keeps repos fresh so that:
- Runner clones see recent commits without cold-start fetches
- Ancestor negotiation for incremental uploads succeeds more often

## Code Uploads

When running `wing <pipeline> --on prod --from local`, the wing CLI
uploads a code tarball directly to the cache (not through the controller):

```
wing CLI -> cache /upload (stores tarball, returns ref ID)
wing CLI -> controller /trigger (job with upload_ref)
runner   -> cache /uploads/<ref> (downloads tarball)
```

For incremental uploads, wing first negotiates a common ancestor with
the cache (`/sync/negotiate`), then sends only the changed files since
that commit.

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
`cache-` subdomain. Write endpoints (`/upload`,
`/sync/negotiate`, `/sync/seed`) require a bearer token:

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
| GET | `/uploads/<id>` | Download uploaded tarball |
| POST | `/sync/negotiate` | Find common ancestor (auth required) |
| POST | `/sync/seed?repo=X` | Seed repo from git bundle (auth required) |

### Artifacts
| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/artifacts/<jobID>?path=X` | Upload artifact |
| GET | `/artifacts/<jobID>` | List artifacts |
| GET | `/artifacts/<jobID>?glob=X` | Download matching artifacts |

### Binary & Dependency Cache
| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/bin/<name>` | Download cached binary |
| PUT | `/bin/<name>` | Upload binary to cache |
| GET | `/cache/<key>` | Download cached dependency archive |
| HEAD | `/cache/<key>` | Check if cache entry exists |
| PUT | `/cache/<key>` | Upload dependency archive to cache |

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
| `DATA_DIR` | Override data root (default: `/data`) |
| `PORT` | Listen port (default: `8090`) |

### Data directories

| Path | Contents |
|------|----------|
| `/data/repos/` | Bare git repositories (named by content hash) |
| `/data/archives/` | Cached repo tarballs |
| `/data/uploads/` | Uploaded code tarballs |
| `/data/artifacts/` | Job output artifacts |
| `/data/repo-names.json` | Friendly name → URL registry |
