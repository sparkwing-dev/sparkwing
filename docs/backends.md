# Storage backends

`backends.yaml` declares where the three persistence surfaces live:

- **cache** — content-addressed artifacts and compiled pipeline binaries
- **logs** — per-job log streams
- **state** — run records, plan snapshots, status

A pipeline run picks a backend per surface through one resolved
configuration. The same pipeline source runs on a laptop with the
filesystem, in GitHub Actions with S3, and in a self-hosted cluster
through the controller's services -- without code changes.

## File layout

Two locations are merged at load time (repo wins per non-zero field):

- `.sparkwing/backends.yaml` — team-shared, checked in
- `~/.config/sparkwing/backends.yaml` — per-user additions / overrides

Honors `$XDG_CONFIG_HOME` for the user-level file.

## Shape

```yaml
defaults:
  cache:
    type: filesystem
    path: ~/.cache/sparkwing
  logs:
    type: filesystem
    path: ~/.cache/sparkwing/logs
  state:
    type: sqlite
    path: ~/.cache/sparkwing/state.db

environments:
  gha:
    detect: { env_var: GITHUB_ACTIONS, equals: "true" }
    cache: { type: s3, bucket: sparkwing-cache, prefix: "${GITHUB_REPOSITORY}/" }
    logs:  { type: s3, bucket: sparkwing-logs,  prefix: "${GITHUB_REPOSITORY}/" }
    state: { type: postgres, url_source: state_db_url }

  kubernetes:
    detect: { env_var: KUBERNETES_SERVICE_HOST, present: true }
    cache: { type: controller }
    logs:  { type: controller }
```

`environments.gha` and `environments.kubernetes` are built-in detect
rules; declaring them in your file overrides per-field (e.g. the
`gha` entry above adds cache, logs, and state surfaces; the built-in
rule contributes only the detect predicate).

## Backend types

| Surface | Types | Use |
| --- | --- | --- |
| `cache` | `filesystem`, `s3`, `gcs`, `azure-blob`, `controller` | Content-addressed artifact and compiled-binary store |
| `logs`  | `filesystem`, `s3`, `gcs`, `azure-blob`, `controller`, `stdout` | Per-job log stream persistence |
| `state` | `sqlite`, `postgres`, `mysql`, `controller` | Run records, plan snapshots, status |

Required fields per type:

- `filesystem` — `path`
- `s3`, `gcs`, `azure-blob` — `bucket` (plus optional `prefix`)
- `postgres`, `mysql` — exactly one of `url` or `url_source` (the
  latter names a secret in the resolved source)
- `controller`, `stdout`, `sqlite` — no required fields

Recognized backend types that aren't implemented in the current
build surface a clear error at run start ("type X is recognized but
not implemented in this build") instead of silently falling back.

## Selection precedence

First non-zero per surface:

1. Per-target overlay (`targets.<name>.backend` on the pipeline)
2. Environment auto-detect (first matching `environments:` entry)
3. `defaults:` block

A per-target override carries the same shape as `defaults:` and
typically pins one surface for an audit requirement:

```yaml
# pipelines.yaml
targets:
  prod:
    runners: [prod-builders]
    backend:
      logs: { type: s3, bucket: prod-audit-logs, prefix: "${RUN_ID}/" }
```

## Pipeline binary distribution

Compiled pipeline binaries live in the cache surface under
`bin/<hash>`. On a cache hit, the orchestrator fetches and execs
without recompiling. An optional `cache.binaries` sub-spec isolates
binaries to a separate destination:

```yaml
defaults:
  cache:
    type: filesystem
    path: ~/.cache/sparkwing
    binaries:
      type: s3
      bucket: sparkwing-binaries
      prefix: "${PIPELINE_NAME}/"
```

## Legacy env-var shim

`SPARKWING_LOG_STORE` and `SPARKWING_ARTIFACT_STORE` are deprecated.
When either is set, sparkwing translates the value into a synthetic
`defaults.logs` / `defaults.cache` entry, prints a one-shot
deprecation warning on stderr, and proceeds. Explicit
`backends.yaml` entries take precedence over the shim, so a config
that declares both wins.

Migrate by replacing the env-var assignments with a
`.sparkwing/backends.yaml` entry:

```yaml
# was: SPARKWING_LOG_STORE=s3://my-team-sparkwing/logs
# was: SPARKWING_ARTIFACT_STORE=s3://my-team-sparkwing/cache
defaults:
  cache: { type: s3, bucket: my-team-sparkwing, prefix: cache/ }
  logs:  { type: s3, bucket: my-team-sparkwing, prefix: logs/  }
```
