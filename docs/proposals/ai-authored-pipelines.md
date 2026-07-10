# AI-authored pipelines: readiness spike

Status: spike (scoping only). No code change; output is an acceptance
definition, an empirical result, and a follow-on work list.

## Question

Can an AI agent, given only sparkwing's authoring docs and a template,
produce a valid pipeline that passes the gate on the first or second try?
And if templates and a guide already exist, what is actually missing to make
AI pipeline authoring a first-class flow?

## The authoring surface that already exists

An agent authoring a pipeline has all of this without new work:

- `sparkwing pipeline new` scaffolds `jobs/<name>.go` plus a `sparkwing.yaml`
  entry and auto-bootstraps `.sparkwing/`. Five built-in starters: `minimal`,
  `build-test-deploy`, `ci-pr-check`, `release`, `scheduled-report`.
- `sparkwing pipeline templates` lists 14 task-shaped registry starters
  (static-site and container deploys for AWS and GCP, Go CI hygiene, DB
  migrate-and-deploy, approval-gated deploy, test sharding, scheduled
  cleanup). Scaffold one with `new --template <name> --param k=v`.
- `docs/authoring-pipelines.md` is the idiom guide, keyed one-to-one to the
  linter rules (`plan-io`, `plan-runtime-branch`, `runner-label`,
  `unused-ref`, `guard-misuse`), each with a do/don't pair.
- `docs/sdk-reference.md` and `docs/pipelines.md` document the full SDK and
  the `sparkwing.yaml` model.
- `sparkwing pipeline lint` parses each `Plan` body and the `guards:` blocks,
  reports violations by rule name, and exits non-zero. It gates a push today.
- `sparkwing pipeline explain` and `sparkwing pipeline plan` render the DAG
  without running it, so an author can preview the shape.

## Pipeline shapes a template must express

Drawn from the repo's own pipelines, the shapes that recur:

- Single-job gate that aggregates check results (`Test`, `Lint`).
- Parallel independent steps under one node (`PreCommit`: gofmt, vet, regex
  sweeps as sibling steps with no `Needs` edges).
- Sequential gate that collects every failure before reporting (`PrePush`).
- Typed data flow: a job `Produces[T]`, downstream jobs read it through a
  `Ref[T]` (`Example`: build to publish to deploy).
- Fan-out over a list (`JobFanOut` to a node group).
- Fixture-with-readiness: start a backend, gate on a `Verify` postcondition,
  tear down in `AfterRun`/`OnFailure` (`Integration`).
- Cross-pipeline call (`RunAndAwait`) and an approval gate (`JobApproval`).

Modifiers a template selects from: `Needs`, `Inline`, `Retry`, `Timeout`,
`SkipIf`, `OnFailure`, `BeforeRun`/`AfterRun`, `Optional`, `Annotate`,
`Summary`. This set is already covered by the `example` pipeline and the
built-in starters, so a template library does not need new shapes.

## The acceptance check

Define acceptance as a four-oracle harness. Given a spec, the authoring
guide, and a scaffolded template, an agent writes `jobs/<name>.go`. The
pipeline is accepted when all four oracles pass:

1. `gofmt -l` reports no files.
2. `go vet ./...` exits 0.
3. `go build ./...` exits 0.
4. `sparkwing pipeline lint --all` exits 0.

"First or second try" means the agent may read one round of failing oracle
output and revise once. Oracle 3 requires the SDK dependency to resolve: a
released tag or a local `replace` to the checkout.

## Empirical result

One run of the harness. Spec: a `nightlybackup` pipeline running three jobs
in sequence (dump to upload to prune) via `pg_dump`, `aws s3 cp`, and a
`find -mtime +30 -delete`, with the database URL read at dispatch. The
authoring agent was given only `authoring-pipelines.md`, `sdk-reference.md`,
and a scaffolded `minimal` starter, and was told not to read other pipeline
sources.

Result: accepted on the first try. All four oracles green. The output was
idiomatic: a pure `Plan` wiring three jobs with `Needs` edges, the `PGURL`
env var read inside the step body and passed through `Bash(...).Env(...)` so
it stays out of the literal command string. No revision round was needed.

## Gaps the run surfaced

The authoring agent reported four points where the docs were thin. Each is a
small, high-leverage fix:

- No worked multi-job sequence sample in the guide. `Needs` wiring was
  inferred from the `unused-ref` example.
- The guide never states outright that `os.Getenv` in a step body is
  sanctioned; it is only implied by "reads at dispatch on the runner".
- No guidance on `Bash` versus `Exec` for untrusted values.
- The `Work`/`Step` return contract (`return nil, nil` for an untyped job)
  is only inferable from the scaffold, not stated.

The harness surfaced one environment gap: a freshly scaffolded repo pins a
development pseudo-version of the SDK that the module proxy cannot resolve,
so oracle 3 (`go build`) fails until a released tag or a local `replace` is
in place. Lint (oracle 4) is pure AST analysis and passes regardless.

## Recommendation

Templates and a guide already exist and are sufficient for a capable agent to
author a valid pipeline first-try on a representative spec. The missing pieces
for AI-generated pipelines are not more templates; they are:

1. A repeatable acceptance harness (a pipeline or a `bin/` script) that runs
   the four oracles against a generated pipeline, so "an agent authored a
   valid pipeline" is a green/red check rather than a judgment call.
2. Closing the four doc gaps above: one worked sequential example plus one
   affirming sentence each.
3. A decision on the SDK-pin story for freshly scaffolded repos, so oracle 3
   is green out of the box (scaffold a dogfood `replace`, or document the
   released-tag requirement in the `new` tips).

The product direction still owned by a human: which repos and pipeline shapes
matter most. The deploy templates already span AWS and GCP for static sites
and containers, so the open call is whether the next investment is more
templates or the harness plus doc closure above.
