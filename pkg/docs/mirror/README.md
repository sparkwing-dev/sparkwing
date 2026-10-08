# Sparkwing Documentation

This directory contains user- and operator-facing documentation. The
web dashboard at `/docs` and `https://sparkwing.dev/docs` render
these pages; the CLI ships them embedded too (`sparkwing docs read
--topic <slug>`).

## Where to start

Sparkwing has two paths. **Local** is a program on your machine and the
machines you own; **Sparkwing Cloud** is the hosted controller that gives a
team one dashboard and one run history, and the command that connects to it
reaches any controller you can reach, including one a team runs itself.
[`getting-started.md`](getting-started.md) walks both.

- **New here?** [`getting-started.md`](getting-started.md) -- install,
  scaffold, run, then connect to a controller.
- **Writing pipelines?** [`sdk.md`](sdk.md) and
  [`pipelines.md`](pipelines.md) cover the Go DSL.
- **Running in CI?** [`ci-embedded.md`](ci-embedded.md) -- `sparkwing
  run --sw-mode=ci-embedded` inside GHA / Buildkite / GitLab CI.
- **Hosting the bucket, database, or controller yourself?**
  [`getting-started.md`](getting-started.md#advanced-deployments) names
  each shape in a paragraph;
  [`deployment-modes.md`](deployment-modes.md#advanced-shapes) is the
  reference and [`self-hosting.md`](self-hosting.md) is the Helm path.
  [`architecture.md`](architecture.md) and
  [`deployment.md`](deployment.md) cover the in-cluster picture.

Generated reference (do not hand-edit; regenerated from code and
drift-gated):

```
docs/
  cli-reference.md       command-group index; cli-<group>.md pages hold every flag and argument
  config-reference.md    every YAML config field
  sdk-reference.md       the sparkwing package; sdk-<name>.md per subpackage
  api-reference.md       controller + logs HTTP routes
```

## CLI surface

Every top-level verb is listed with a one-line synopsis in
[cli-reference.md](cli-reference.md), generated from the command
registry; `sparkwing commands` prints the same index offline (`-o json`
for the machine-readable index, one record per line -- narrow with
`--path` or cut with `head`). The cross-repo registry lives under `repos`;
sparks library management under `pipeline sparks`. Run any verb
with `--help` for its full spec.

## Repo-local helpers vs sparkwing

Sparkwing is for DAG'd, triggered, or cached work that earns a durable
run record. One-shot repo-local shell chores -- formatters,
port-forwards, Makefile-style glue -- stay cheaper in whatever task
runner you already use; sparkwing does not try to replace it.
