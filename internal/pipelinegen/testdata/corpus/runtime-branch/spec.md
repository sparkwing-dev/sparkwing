---
shape: anti-pattern-runtime-branch
expect: fail
entrypoint: GenRuntimeBranch
---
A pipeline that builds a different DAG depending on a DEPLOY_ENV
environment variable read inside Plan(). This is a deliberately bad
generation: Plan() must be deterministic. The linter's
plan-runtime-branch rule must reject it.
