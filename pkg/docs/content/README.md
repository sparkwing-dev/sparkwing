# Sparkwing Documentation

This directory contains user- and operator-facing documentation. The
web dashboard at `/docs` and `https://sparkwing.dev/docs` render
these pages; the CLI ships them embedded too (`sparkwing docs read
--topic <slug>`).

## Where to start

- **New here?** [`getting-started.md`](getting-started.md) — install,
  scaffold, run.
- **Writing pipelines?** [`sdk.md`](sdk.md) and
  [`pipelines.md`](pipelines.md) cover the Go DSL.
- **Running in CI?** [`ci-embedded.md`](ci-embedded.md) — `sparkwing
  run --mode=ci-embedded` inside GHA / Buildkite / GitLab CI.
- **Self-hosting the dashboard?** [`architecture.md`](architecture.md)
  + [`deployment.md`](deployment.md).
- **Self-hosting without Kubernetes?** [`self-hosting.md`](self-hosting.md)
  — single-host docker-compose + laptop-fleet runners.

## Map

```
docs/
  getting-started.md     install, quick start, run targets
  sdk.md                 Go DSL: Plan, Job, Work, Step, modifiers
  pipelines.md           pipeline YAML, registration, triggers
  cli.md                 sparkwing + wing CLI reference
  api.md                 controller HTTP API reference
  architecture.md        in-cluster deployment architecture
  deployment.md          deploy targets, gitops, ArgoCD, registries
  self-hosting.md        non-k8s self-host: docker-compose + laptop runners
  ci-embedded.md         run pipelines inside an existing CI job
  local-execution.md     how local vs remote execution interact
  native-mode.md         the laptop model (detached dashboard)
  hooks.md               triggers (webhooks + opt-in pipeline hooks)
  scheduling.md          runner labels, taints, tolerations, runs_on
  warm-pool.md           warm PVC pool
  caching.md             node-level CacheKey modifier
  build-caching.md       Docker / BuildKit / proxy caching layers
  fast-builds.md         performance best practices
  gitcache.md            sparkwing-cache: git HTTP, blobs, package proxy
  sparks.md              spark library dependency management
  sparks-core.md         the canonical sparks-* helper bundle
  auth.md                principal + scope + argon2 token model
  security.md            transport, rate limiting, secret management
  observability.md       failure reasons, resource metrics, OTel
  mcp.md                 MCP server for AI agents
```

## CLI surface

The top-level commands: `info`, `pipeline`, `run`, `runs`, `version`,
`dashboard`, `cluster`, `secrets`, `configure`, `debug`, `docs`,
`commands`, `completion`. Cross-repo registry under `configure
xrepo`; sparks library mgmt under `pipeline sparks`. Run any verb
with `--help` for its full spec, or `sparkwing commands -o json` for
the agent-readable surface dump.

## Repo-local helpers vs sparkwing

For repo-local bash chores (formatters, port-forwards, the small
Makefile-style stuff) use `dowing`; sparkwing is the Go-pipeline
platform. The two coexist on the same laptop: dowing handles
single-shell tasks, sparkwing handles DAG'd / triggered / cached
work that benefits from a real run record.
