# Running inside an existing CI job

Run sparkwing pipelines **inside** an existing CI job (GitHub Actions,
Buildkite, GitLab CI, CircleCI, ...) without standing up a sparkwing
cluster. The run executes on the CI runner against that runner's local
SQLite store, logs, and cache, as `sparkwing run` does on a laptop, and
those records go away with the CI VM. A CI job that wants run history and
a dashboard in a shared place triggers the pipeline on a controller
instead; see [sharing results](#sharing-results).

Use the same local command as on a laptop. No mode flag is required. The
existing CI provider starts the run and receives its exit status. To execute
on controller-managed runners, use `sparkwing pipeline trigger` instead.

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
        run: sparkwing run release-prod --sw-workers=4
```

State, cache, and logs stay on the CI runner's disk under `~/.sparkwing`.

A pipeline job that fails fails the GHA job (exit code propagates).

## Local execution

`sparkwing run` uses the local controller and shared run host. The controller
owns local state and admission. The host executes the pipeline on this CI
runner. `--sw-workers=N` selects a positive foreground worker count and cannot
be combined with `--sw-detached`.

For standalone CI, `--sw-allow-unadmitted` starts an exclusive in-process
controller. It stays available through the here claim, execution and log
following, then shuts down before the command returns. It cannot detach or
share a home with another running controller.

```sh
sparkwing run release-prod --sw-allow-unadmitted --sw-no-update
```

Keep the pipeline module and any resolved sparks overlay in the checkout.
Use `sparkwing pipeline sparks update` to refresh declared libraries before
committing the overlay. The pipeline's SDK pin selects its matching engine.

### Using the committed sparks overlay

For a project with a `sparks:` block, `--sw-no-update` uses the committed
`.resolved.mod` overlay without refreshing library resolution during the run:

```sh
sparkwing run release-prod --sw-no-update --sw-workers=4
```

Refresh the overlay before committing a library update:

```sh
sparkwing pipeline sparks update
git diff .sparkwing/.resolved.mod
```

## Sharing results

A CI job whose runs belong in a shared run history triggers the pipeline
on a controller (Sparkwing Cloud or one you host) rather than running it
on the CI VM:

```sh
sparkwing pipeline trigger release-prod --profile prod
```

The controller dispatches the run to its own runners. The CI step follows
the run until it finishes and exits non-zero when the run fails. See
[deployment modes](deployment-modes.md) for the controller shapes.

## Exit codes

- `0` if every pipeline job succeeds.
- `1` if a job fails or execution errors.

The exit code is what the wrapping CI job sees, so a failed sparkwing
job fails the CI step.

## Caching

Local caches live on the CI runner. A fresh VM starts cold unless the CI
provider restores those cache files. To read authorized CI memo results,
artifacts and dependency caches from a controller, select
`sparkwing run --sw-cache-from PROFILE`. This option only reads verified
default-branch records. It never uploads local results or fetches pipeline
binaries.

## Buildkite

```yaml
steps:
  - label: "release"
    command: |
      sparkwing run release-prod --sw-workers=4
```

## GitLab CI

```yaml
release:
  image: alpine:latest
  before_script:
    - apk add --no-cache curl
    - curl -fsSL https://sparkwing.dev/install.sh | sh
  script:
    - sparkwing run release-prod --sw-workers=4
```
