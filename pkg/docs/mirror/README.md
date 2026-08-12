# Sparkwing Documentation

This directory contains user- and operator-facing documentation. The
web dashboard at `/docs` and `https://sparkwing.dev/docs` render
these pages; the CLI ships them embedded too (`sparkwing docs read
--topic <slug>`).

## Where to start

- **New here?** [`getting-started.md`](getting-started.md) -- install,
  scaffold, run.
- **Writing pipelines?** [`sdk.md`](sdk.md) and
  [`pipelines.md`](pipelines.md) cover the Go DSL.
- **Running in CI?** [`ci-embedded.md`](ci-embedded.md) -- `sparkwing
  run --sw-mode=ci-embedded` inside GHA / Buildkite / GitLab CI.
- **Self-hosting the dashboard?** [`architecture.md`](architecture.md)
  - [`deployment.md`](deployment.md).
- **Self-hosting without Kubernetes?** [`self-hosting.md`](self-hosting.md)
  -- single-host docker-compose + laptop-fleet runners.

## Map

```
docs/
  getting-started.md     install, quick start, run targets
  sdk.md                 Go DSL: Plan, Job, Work, Step, modifiers
  pipelines.md           pipeline YAML, registration, triggers
  authoring-pipelines.md idiomatic Plan/Work authoring
  artifacts.md           moving files between nodes
  cli.md                 sparkwing CLI command-group guide
  api.md                 controller HTTP API reference
  architecture.md        in-cluster deployment architecture
  deployment.md          deploy targets, gitops, ArgoCD, registries
  deployment-modes.md    the deployment shapes and what each gives you
  self-hosting.md        non-k8s self-host: docker-compose + laptop runners
  ci-embedded.md         run pipelines inside an existing CI job
  local-execution.md     how local vs remote execution interact
  native-mode.md         the laptop model (detached dashboard)
  hooks.md               triggers (webhooks + opt-in pipeline hooks)
  scheduling.md          runner labels, .Requires/.Prefers/.WhenRunner
  warm-pool.md           warm PVC pool
  caching.md             node-level Cache modifier (.Cache / MemoizeOption)
  backends.md            per-profile state / cache / logs destinations
  build-caching.md       Docker / BuildKit / proxy caching layers
  fast-builds.md         performance best practices
  gitcache.md            sparkwing-cache: git HTTP, blobs, package proxy
  sparks.md              spark library dependency management
  sparks-core.md         the canonical sparks-* helper bundle
  versioning.md          versioning policy, plugin compatibility, SDK extraction roadmap
  auth.md                principal + scope + argon2 token model
  security.md            transport, rate limiting, secret management
  observability.md       failure reasons, resource metrics, OTel
  mcp.md                 MCP server for AI agents
```

Generated reference (do not hand-edit; regenerated from code and
drift-gated):

```
docs/
  cli-reference.md       command-group index; cli-<group>.md pages hold every flag and argument
  config-reference.md    every YAML config field
  sdk-reference.md       the sparkwing package
  api-reference.md       controller + logs HTTP routes
```

## CLI surface

Every top-level verb is listed with a one-line synopsis in
[cli-reference.md](cli-reference.md), generated from the command
registry; `sparkwing commands` prints the same index offline (`-o json`
for the full records). Cross-repo registry lives under `configure
xrepo`; sparks library management under `pipeline sparks`. Run any verb
with `--help` for its full spec.

## Repo-local helpers vs sparkwing

Sparkwing is for DAG'd, triggered, or cached work that earns a durable
run record. One-shot repo-local shell chores -- formatters,
port-forwards, Makefile-style glue -- stay cheaper in whatever task
runner you already use; sparkwing does not try to replace it.
