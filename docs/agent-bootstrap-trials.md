# Agent bootstrap eval

A running log of an iterative effort to make the `sparkwing` CLI
self-explanatory enough that an AI agent, given a one-line task and no
hand-holding, can discover how to build, run, and ship a pipeline. Each
round spawns agents with minimal direction, collects their bootstrap
feedback + footguns, and folds the cheap/reasonable fixes back into the
CLI, templates, and docs. The goal is to drive substantive feedback to
zero across a variety of pipeline shapes.

## Method

- **Prompt shape** (per agent, minimal): "You have the `sparkwing` CLI.
  In `<dir>`, build a pipeline that does `<X>`. Make it run
  successfully (`<locally | on the controller>`). Then report how easy
  it was to bootstrap, where you got stuck, footguns, and changes
  you'd want. Use only the binary's own help/docs — don't ask."
- **Sandboxing**: each agent works in its own throwaway dir so runs
  don't collide and the real repos stay clean.
- **Feedback schema**: ran_ok, bootstrap_ease (1-5), commands_used,
  stuck_points, footguns, requested_changes (with effort guess).
- **Fix bar**: implement a change only if it's reasonable and cheap
  (help text, info/commands output, docs, template descriptions, error
  messages, scaffold defaults). Larger asks get logged, not built.
- **Cadence**: one batch per round; triage + implement + rebuild the
  binary between rounds; re-test fixed pain points in later rounds.

## Controller-run policy

The only controller profile on this machine is `prod`
(`api-sparkwing.rangz.dev`); there is no local-controller `serve`
command. Blind agents are therefore kept **local-first**: rounds run
`sparkwing run` against the laptop profile, which is where most
bootstrap/discoverability friction lives and carries zero prod risk.
Controller runs (`pipeline trigger --profile`, or push-to-okbot →
webhook) are validated by hand on one controlled, non-destructive
pipeline before any agent is pointed at them, and only layered into
later rounds with sandboxed (echo/no-op or kind-scoped) deploys.

## Baseline discoverability (CLI `v0.8.1-dev`, before round 1)

Already in good shape — the cold-start path mostly exists:

- `sparkwing --help` lists `info`, `commands` (full surface as JSON for
  agent self-discovery), `docs` (offline), and `pipeline`
  (list/describe/discover/new/templates/explain/plan/run/trigger).
  Examples block points agents straight at `sparkwing info -o json` and
  `sparkwing pipeline list -o json`.
- `sparkwing pipeline templates` lists the sparks-core registry with
  rich "use this when / use that instead" guidance: AWS + GCP twins
  (`static-deploy-s3-cloudfront` / `-gcs-cloudcdn`,
  `docker-deploy-ecr-eks` / `-gar-gke`) and k8s
  (`go-test-build-deploy-k8s`, `go-test-migrate-deploy-argo`), each
  with required/optional params.
- `sparkwing pipeline new --template <name> --param k=v` scaffolds from
  the registry; `--help` cross-references `pipeline templates`.

What round 1 is probing: whether agents actually *traverse* that path
unprompted, and where the experience past "scaffolded" (edit → run →
read errors) still confuses.

## Change log

| Round | Shape | Feedback theme | Change | File(s) | Effort |
|------:|-------|----------------|--------|---------|--------|
| _baseline captured; round 1 pending_ | | | | | |

## Deferred / larger asks

_(logged here when an agent requests something beyond the cheap-fix bar)_
