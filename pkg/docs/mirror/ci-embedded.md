# ci-embedded mode

Run sparkwing pipelines **inside** an existing CI job (GitHub Actions,
Buildkite, GitLab CI, CircleCI, ...) without standing up a sparkwing
cluster. The run executes on the CI runner against that runner's local
SQLite store, logs, and cache, as `sparkwing run` does on a laptop, and
those records go away with the CI VM. A CI job that wants run history and
a dashboard in a shared place triggers the pipeline on a controller
instead, or records to a shared bucket or database; see
[sharing results](#sharing-results).

## When to use

| Scenario | Mode |
| -------- | ---- |
| Laptop dev loop, fast feedback | `local` (default) |
| Migrating from GHA / Buildkite, want better DX without changing CI vendor | **`ci-embedded`** |
| Self-hosted cluster with runners, fan-out | `distributed` |

ci-embedded is the migration wedge: keep your CI vendor's job orchestration,
let sparkwing handle the pipeline DSL and caching.

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
        run: sparkwing run release-prod --sw-mode=ci-embedded --sw-workers=4
```

State, cache, and logs stay on the CI runner's disk under `~/.sparkwing`.

A pipeline node that fails fails the GHA job (exit code propagates).

## How it works

1. `--sw-mode=ci-embedded` plumbs through `sparkwing` -> the pipeline binary
   via env vars (`SPARKWING_MODE`, `SPARKWING_WORKERS`).
2. Without `--profile`, the orchestrator records run and node state in
   the runner's local SQLite store, per-node logs under
   `~/.sparkwing/runs/<id>/`, and cache entries on local disk.
3. The run records `mode` among its invocation flags.

## Flags

| Flag | Default | Description |
| ---- | ------- | ----------- |
| `--sw-mode=ci-embedded` | (off) | Enables this mode. |
| `--sw-workers=N` | `runtime.NumCPU()` | Caps the local dispatcher. A hosted runner has far fewer cores than a workstation, so a value picked for local use over-subscribes it -- pick deliberately. |
| `--profile NAME` | (none) | Records the run to the backends of a profile with no controller whose state is `s3` or `postgres`; see [through a shared bucket or database](#through-a-shared-bucket-or-database). Any other profile is refused. Without it, the run records to the runner's local disk. |

### Recommended: `--sw-no-update` in CI

If your `.sparkwing/sparkwing.yaml` declares a `sparks:` block, sparkwing
auto-refreshes the resolved overlay at run time by default. That
shells out to `go env` / `go list`, which means CI runners would
need a Go toolchain even on a cache hit. **Pass `--sw-no-update` to
`sparkwing run` in the CI step** so the runner trusts the committed
`.resolved.mod` overlay and never resolves on its own.

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
Repos without a `sparks:` block ignore the flag -- it's a no-op.

## Sharing results

### Through a controller

A CI job whose runs belong in a shared run history triggers the pipeline
on a controller (Sparkwing Cloud or one you host) rather than running it
on the CI VM:

```sh
sparkwing pipeline trigger release-prod --profile prod
```

The controller dispatches the run to its own runners. The CI step follows
the run until it finishes and exits non-zero when the run fails. See
[deployment modes](deployment-modes.md) for the controller shapes.

### Through a shared bucket or database

`sparkwing run --profile NAME` with a profile whose state is an S3 bucket
or a Postgres database runs the pipeline on the CI runner and records its
state, logs, and cache to that profile's backends, so a remote dashboard
can follow the run live and replay it after the CI VM exits. This is a
temporary exception slated for removal; a controller is the supported way
to share runs. Any other profile on `run` is refused with a message naming
`sparkwing pipeline trigger <pipeline> --profile NAME`.

Declare the profile in `~/.config/sparkwing/config.yaml` on the runner
(override the path with `SPARKWING_CONFIG`):

```yaml
profiles:
  ci-team:
    state: { type: s3, bucket: my-team-sparkwing, prefix: state }
    cache: { type: s3, bucket: my-team-sparkwing, prefix: cache }
    logs:  { type: s3, bucket: my-team-sparkwing, prefix: logs }
```

Then pass it, with the AWS environment the profile does not carry (see
[what each runner needs](deployment-modes.md#what-each-runner-needs-beyond-the-profile)):

```yaml
      - name: Run sparkwing release pipeline
        env:
          AWS_ACCESS_KEY_ID:     ${{ secrets.AWS_ACCESS_KEY_ID }}
          AWS_SECRET_ACCESS_KEY: ${{ secrets.AWS_SECRET_ACCESS_KEY }}
          AWS_REGION:            us-west-2
        run: sparkwing run release-prod --sw-mode=ci-embedded --profile ci-team
```

Run and node state goes to the profile's `state:` surface, and per-node
log lines route to its `logs:` surface instead of
`~/.sparkwing/runs/<id>/`. An S3 state surface writes
`runs/<runID>/state.ndjson` continuously over the life of the run.

#### Watching from a laptop dashboard

After (or during) a ci-embedded run, point your local dashboard at
the same bucket:

```sh
sparkwing serve start \
    --profile ci-team \
    --read-only
```

The dashboard reads `state.ndjson` for run metadata, the LogStore
for per-node lines, and the ArtifactStore for any blobs the pipeline
saved. A profile whose `state:` surface is S3 streams run and node
records to `runs/<runID>/state.ndjson` continuously (flushed on a
sub-second interval, or earlier once enough envelopes buffer), so a
dashboard pointed at the same bucket follows the run live.

#### Fresh laptop, no SQLite (`--no-local-store`)

The default invocation above still opens `~/.sparkwing/state.db` so
locally-triggered runs can coexist with the remote ones. On a clean
machine that has *only* the bucket -- new hire, ephemeral
container, etc. -- pass `--no-local-store` to skip SQLite entirely
and have the dashboard list runs directly from
`<artifact-store>/runs/*/state.ndjson`:

```sh
sparkwing serve start \
    --profile ci-team \
    --no-local-store \
    --read-only
```

This mode is read-only by construction: the orchestrator's write
endpoints (cancel, retry, approvals) are not mounted, since there's
no local SQLite to persist to. Passing `--no-local-store` without
both `--log-store` and `--artifact-store` (directly or via `--profile`)
errors out -- the dashboard would have nowhere to read from.

#### S3 layout

```text
<bucket>/<prefix>/
    cache/                                  # ArtifactStore
        runs/<runID>/state.ndjson           # final run + node dump
        <user-keys...>                      # pipeline-saved blobs
    logs/                                   # LogStore
        <runID>/<nodeID>/<seq>.ndjson       # one object per flush
```

S3 has no native append, so every write is its own object. The logs
surface buffers each node's lines and writes one object per flush,
which keeps a chatty node at a handful of requests rather than one per
line. A flush lands when the buffer reaches `batch_bytes`, when
`batch_interval` elapses, when a reader asks for the node's log, and
when the node finishes. `max_log_objects` and `max_log_bytes` bound
what one node can write; past either the surface drops further lines
and ends the node's log with one marker line counting them. See
[storage backends](backends.md#object-store-log-batching) for the keys
and their defaults. Reads list+concat by prefix.

## Exit codes

- `0` if every pipeline node succeeds.
- `1` if any node fails or the orchestrator errors.

The exit code is what the wrapping CI job sees, so a failed sparkwing
node fails the CI step.

## Caveats

- **No webhooks**. ci-embedded mode is invoked by the host CI; let
  GitHub Actions / Buildkite handle the trigger.
- **Caching across runs** depends on stable `bincache.PipelineCacheKey`
  output (sha256 over source + go toolchain). Same source tree on the
  same Go version = warm cache. Without a shared profile the cache lives
  on the runner's disk, so a fresh VM starts cold unless the CI job
  persists the sparkwing home between runs.
- **Worker count vs CPU**. GHA hosted runners default to 2 CPUs.
  `--sw-workers=NumCPU` (the default) usually fits fine; larger numbers
  trade memory pressure for less queueing.

## Buildkite

```yaml
steps:
  - label: "release"
    command: |
      sparkwing run release-prod --sw-mode=ci-embedded --sw-workers=4
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
