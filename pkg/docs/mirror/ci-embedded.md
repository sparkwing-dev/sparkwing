# ci-embedded mode

Run sparkwing pipelines **inside** an existing CI job (GitHub Actions,
Buildkite, GitLab CI, CircleCI, ...) without standing up a sparkwing
cluster. Logs and artifacts go to S3-compatible storage so a remote
dashboard can replay the run after the CI VM exits.

## When to use

| Scenario | Mode |
| -------- | ---- |
| Laptop dev loop, fast feedback | `local` (default) |
| Migrating from GHA / Buildkite, want better DX without changing CI vendor | **`ci-embedded`** |
| Self-hosted cluster with runners, fan-out | `distributed` |

ci-embedded is the migration wedge: keep your CI vendor's job orchestration,
let sparkwing handle the pipeline DSL + caching + dashboard.

## Quick start (GitHub Actions)

`.github/workflows/ci.yaml`:

```yaml
name: ci
on: [push]

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.26' }
      - run: curl -fsSL https://sparkwing.dev/install.sh | bash

      - name: Run sparkwing release pipeline
        env:
          AWS_ACCESS_KEY_ID:     ${{ secrets.AWS_ACCESS_KEY_ID }}
          AWS_SECRET_ACCESS_KEY: ${{ secrets.AWS_SECRET_ACCESS_KEY }}
          AWS_REGION:            us-west-2
        run: sparkwing run release-prod --sw-mode=ci-embedded --sw-workers=4
```

State, cache, and logs destinations come from the resolved profile in
`~/.config/sparkwing/profiles.yaml`. The built-in `gha` profile
auto-selects when `GITHUB_ACTIONS=true`. See
[storage backends](backends) for the configuration shape.

A pipeline node that fails fails the GHA job (exit code propagates).

## How it works

1. `--sw-mode=ci-embedded` plumbs through `sparkwing` -> the pipeline binary
   via env vars (`SPARKWING_MODE`, `SPARKWING_WORKERS`).
2. The orchestrator resolves state, cache, and logs from the active
   profile. In GHA the built-in `gha` profile auto-detects and selects
   (e.g.) S3.
3. SQLite handles fast lifecycle writes; state goes to the profile's
   configured state backend.
4. Per-node log lines route to the resolved `Logs` backend instead
   of `~/.sparkwing/runs/<id>/`.
5. When the pipeline exits, run + node records are serialized to
   NDJSON and uploaded to `<cache>/runs/<runID>/state.ndjson`.
6. A dashboard configured with the matching backends reads
   everything back.

## Flags

| Flag | Default | Description |
| ---- | ------- | ----------- |
| `--sw-mode=ci-embedded` | (off) | Enables this mode. |
| `--sw-workers=N` | `runtime.NumCPU()` | Caps the local dispatcher. GHA hosted runners are 2-CPU; setting `--sw-workers=4` on small VMs over-subscribes -- pick deliberately. |
| `--profile PROFILE` | (auto) | Selects a profile from `~/.config/sparkwing/profiles.yaml`. Absent, the auto-detected (`gha`) or default profile applies. |

State, cache, and logs come from the resolved profile; see
[storage backends](backends) for the configuration shape.

### Recommended: `SPARKWING_NO_SPARKS_RESOLVE=1` in CI

If your `.sparkwing/sparkwing.yaml` declares a `sparks:` block, sparkwing
auto-refreshes the resolved overlay at run time by default. That
shells out to `go env` / `go list`, which means CI runners would
need a Go toolchain even on a cache hit. **Set
`SPARKWING_NO_SPARKS_RESOLVE=1` in the CI step's env** so the runner
trusts the committed `.resolved.mod` overlay and never resolves on
its own.

Workflow then becomes:

```sh
# locally, when you want a fresh resolve
sparkwing pipeline sparks update
git diff .sparkwing/.resolved.mod
git commit -am "bump sparks-core"
git push                          # triggers publish + run with frozen overlay
```

CI never re-resolves; the publish step on your laptop (or in a
publish-on-merge workflow) is the deliberate "go fresh" surface.
Repos without a `sparks:` block ignore this var -- it's a no-op.

## Profile-based config (laptop)

`~/.config/sparkwing/profiles.yaml`:

```yaml
profiles:
  ci-team:
    state: { type: s3, bucket: my-team-sparkwing, prefix: state }
    cache: { type: s3, bucket: my-team-sparkwing, prefix: cache }
    logs:  { type: s3, bucket: my-team-sparkwing, prefix: logs }
```

Then:

```sh
sparkwing run release-prod --sw-mode=ci-embedded --profile ci-team
```

## Watching from a laptop dashboard

After (or during) a ci-embedded run, point your local dashboard at
the same bucket:

```sh
sparkwing dashboard start \
    --profile ci-team \
    --read-only
```

The dashboard reads `state.ndjson` for run metadata, the LogStore
for per-node lines, and the ArtifactStore for any blobs the pipeline
saved. **Live tail** during an in-flight run is **not yet supported**
in ci-embedded mode. Today the dashboard renders once the CI job has
finished writing the dump.

### Fresh laptop, no SQLite (`--no-local-store`)

The default invocation above still opens `~/.sparkwing/state.db` so
locally-triggered runs can coexist with the remote ones. On a clean
machine that has *only* the bucket -- new hire, ephemeral
container, etc. -- pass `--no-local-store` to skip SQLite entirely
and have the dashboard list runs directly from
`<artifact-store>/runs/*/state.ndjson`:

```sh
sparkwing dashboard start \
    --profile ci-team \
    --no-local-store \
    --read-only
```

This mode is read-only by construction: the orchestrator's write
endpoints (cancel, retry, approvals) are not mounted, since there's
no local SQLite to persist to. Passing `--no-local-store` without
both `--log-store` and `--artifact-store` (directly or via `--profile`)
errors out -- the dashboard would have nowhere to read from.

## S3 layout

```
<bucket>/<prefix>/
    cache/                                  # ArtifactStore
        runs/<runID>/state.ndjson           # final run + node dump
        <user-keys...>                      # pipeline-saved blobs
    logs/                                   # LogStore
        <runID>/<nodeID>/<seq>.ndjson       # rolling per-Append parts
```

Object-per-Append is intentional: S3 has no native append. Reads
list+concat by prefix, which is fine at single-run scales.

## Exit codes

- `0` if every pipeline node succeeds.
- `1` if any node fails or the orchestrator errors.

The exit code is what the wrapping CI job sees, so a failed sparkwing
node fails the CI step.

## Caveats

- **Live tail not supported** during in-flight runs. State dump is
  written on exit. Streaming run state to S3 incrementally is on
  the roadmap.
- **No webhooks**. ci-embedded mode is invoked by the host CI; let
  GitHub Actions / Buildkite handle the trigger.
- **Caching across runs** depends on stable `bincache.PipelineCacheKey`
  output (sha256 over source + go toolchain). Same source tree on the
  same Go version = warm cache.
- **Worker count vs CPU**. GHA hosted runners default to 2 CPUs.
  `--sw-workers=NumCPU` (the default) usually fits fine; larger numbers
  trade memory pressure for less queueing.

## Buildkite

```yaml
steps:
  - label: "release"
    command: |
      sparkwing run release-prod --sw-mode=ci-embedded --sw-workers=4
    plugins:
      - aws-credentials#v1.0:
          role: arn:aws:iam::1234:role/buildkite-sparkwing
```

State, cache, and logs come from the resolved profile. Buildkite
doesn't have a built-in detect rule out of the box; declare a profile
with its own `detect:` block if you want a Buildkite-specific overlay:

```yaml
# ~/.config/sparkwing/profiles.yaml
profiles:
  buildkite:
    detect: { env_var: BUILDKITE, equals: "true" }
    state: { type: s3, bucket: my-team-sparkwing, prefix: state/ }
    cache: { type: s3, bucket: my-team-sparkwing, prefix: cache/ }
    logs:  { type: s3, bucket: my-team-sparkwing, prefix: logs/  }
```

## GitLab CI

```yaml
release:
  image: alpine:latest
  before_script:
    - apk add --no-cache curl
    - curl -fsSL https://sparkwing.dev/install.sh | sh
  script:
    - sparkwing run release-prod --sw-mode=ci-embedded --sw-workers=4
```

Declare a `gitlab` profile with `detect: { env_var: GITLAB_CI, equals:
"true" }` in `~/.config/sparkwing/profiles.yaml` if you want a
GitLab-specific overlay.

## Related

- The storage interface + filesystem / S3 backends.
- The dashboard's storage-aware reads.
- Live tail of in-flight ci-embedded runs (planned).
