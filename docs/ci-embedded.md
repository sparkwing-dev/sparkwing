# ci-embedded mode

Run sparkwing pipelines **inside** an existing CI job (GitHub Actions,
Buildkite, GitLab CI, CircleCI, ...) without standing up a sparkwing
cluster. The run executes on the CI runner against that runner's local
SQLite store, logs, and cache, as `sparkwing run` does on a laptop, and
those records go away with the CI VM. A CI job that wants run history and
a dashboard in a shared place triggers the pipeline on a controller
instead; see
[sharing results through a controller](#sharing-results-through-a-controller).

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
2. The orchestrator records run and node state in the runner's local
   SQLite store, per-node logs under `~/.sparkwing/runs/<id>/`, and cache
   entries on local disk.
3. The run records `mode` among its invocation flags.

## Flags

| Flag | Default | Description |
| ---- | ------- | ----------- |
| `--sw-mode=ci-embedded` | (off) | Enables this mode. |
| `--sw-workers=N` | `runtime.NumCPU()` | Caps the local dispatcher. A hosted runner has far fewer cores than a workstation, so a value picked for local use over-subscribes it -- pick deliberately. |

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

## Sharing results through a controller

A CI job whose runs belong in a shared run history triggers the pipeline
on a controller (Sparkwing Cloud or one you host) rather than running it
on the CI VM:

```sh
sparkwing pipeline trigger --profile ci-team release-prod
```

The controller dispatches the run to its own runners. The CI step follows
the run until it finishes and exits non-zero when the run fails. See
[deployment modes](deployment-modes.md) for the controller shapes.

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
  same Go version = warm cache. The cache lives on the runner's disk, so
  a fresh VM starts cold unless the CI job persists the sparkwing home
  between runs.
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
