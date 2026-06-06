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

### Round 1 — 6 agents (ci-go, parallel-checks, verify-rollback, build-artifact, migrate-db, matrix-test)

Result: 6/6 ran successfully locally, avg bootstrap ease 4.0/5. Discovery
path was consistent and healthy — every agent went
`sparkwing --help` → `info` → `pipeline templates` / `docs read --topic sdk`
unprompted, which validates the top-level affordances. Friction was
concentrated in stale generated code/docs and a few CLI gaps.

Fixes implemented (cheap, high-frequency, clearly correct):

| Theme (agents) | Change | File(s) | Effort |
|----------------|--------|---------|--------|
| Scaffolds don't compile: removed `sparkwing.JobFn` still emitted (critical) | drop the `JobFn(...)` wrapper; pass the closure directly to `Job` | sparks-core `templates/{lint-test-go,docker-deploy-gar-gke,static-deploy-gcs-cloudcdn,next-build-and-push}/pipeline.go.tmpl` | small |
| `pipelines.yaml` vs `sparkwing.yaml` naming (5/6) | rename all current-context refs to `sparkwing.yaml` | `cmd/sparkwing/action_new.go`, `help_registry.go`, `docs/getting-started.md`, `docs/sdk.md` | trivial |
| No `-C`/`--sw-cd` on `pipeline new` (6/6) | add `-C/--sw-cd` flag + chdir; document it | `cmd/sparkwing/action_new.go`, `help_registry.go` | small |
| Stale SDK in `getting-started` doc (`JobFn`, `Work() *sw.Work`, `w.Step`) | rewrite to current API (`Work(w) (*WorkStep,error)`, `sw.Step`, closure `Job`) | `docs/getting-started.md` | small |
| `JobFanOut` doc example doesn't compile (`(string, sw.Workable)`) | fix to `(string, any)` | `docs/pipelines.md` | trivial |
| `minimal` stub ships `Log("TODO")` (trips no-TODO lint) | placeholder `Info("replace this stub…")` + help text | `cmd/sparkwing/action_new.go`, `help_registry.go` | trivial |
| `--json` advertised "every verb" but errors on `new` | soften claim to discovery verbs only | `help_registry.go` | trivial |
| (tooling) no scaffold-and-build regression guard | `build-all-templates.sh`: scaffold every registry template + `go build` | `.claude-scratch/` (sparks-core) | small |

Validation: rebuilt + reinstalled the binary (go.work pulls local
templates); `build-all-templates.sh` confirms 6/8 templates compile and
`-C` works for all scaffolds.

### Round 2 — 6 agents (ci-hygiene-go, lint-many-dirs, scheduled-cleanup, retry-flaky-fetch, branch-conditional, redis-integration)

Result: 6/6 ran, avg bootstrap ease 4.17/5 (↑ from 4.0). Round-1 fixes
confirmed landed — `lint-test-go` was scaffolded and rated 5/5 with zero
`JobFn`/`sparkwing.yaml`/`-C` complaints. New friction was docs/schema
lies and undocumented APIs.

Fixes implemented:

| Theme (agents) | Change | File(s) | Effort |
|----------------|--------|---------|--------|
| `info`/`run` silently attach to an ancestor `.sparkwing` (5/6) | `info` now prints a breadcrumb + `-C` hint when the project was found by walking up | `cmd/sparkwing/info.go` | small |
| `tags:` (and `env`/`secrets`/`runs_on`/`hidden`) documented but rejected by the parser (trust) | rewrote the pipeline-entry field list to the real schema (name/entrypoint/description/on/guards/args/profile/requires); added required `entrypoint` to examples | `docs/pipelines.md`, `docs/getting-started.md` | small |
| `Git.Branch` undocumented → branch-conditional shelled out to git | documented the `Git` struct fields + the `SkipIf`-vs-`Plan`-purity guidance | `docs/sdk.md` | trivial |
| `WithServices` mentioned once, no signature → redis agent hand-rolled docker | new "Service containers" section: signature, `Service` struct, Redis example, teardown guarantee; called out why `AfterRun`/`Needs`-teardown leak | `docs/sdk.md` | small |
| `Retry` semantics ambiguous (n vs total, opt signatures) | clarified "additional attempts (n+1 total), re-runs whole Work()", documented `RetryBackoff`/`RetryAuto` | `docs/sdk.md` | trivial |
| `ExecResult` fields undocumented | documented `Command/Stdout/Stderr/ExitCode` | `docs/sdk.md` | trivial |
| stub still ships `TODO:` (ShortHelp + build-test-deploy echoes) | replaced all scaffold `TODO`s with neutral placeholders | `cmd/sparkwing/action_new.go`, `init.go` | trivial |
| `pipeline new --help` doesn't surface registry templates (5/5 agent almost missed `lint-test-go`) | help now leads with a `pipeline templates` pointer | `cmd/sparkwing/help_registry.go` | small |

Validation: rebuilt/reinstalled; breadcrumb fires from a repo subdir; the
new `docs read --topic sdk` sections render; `pipeline new --help` shows
the templates pointer.

## Deferred / larger asks

- **`go-test-build-deploy-k8s` + `go-test-migrate-deploy-argo` don't compile against released deps** — they (and the `rollback` module) call `kube.Apply/SetImage/RolloutUndo` and `gitops.Revert`, which exist on sparks-core HEAD but are post-`v0.24.0` and unreleased. Fixing this needs a sparks-core module release (deferred: releases are on hold). Until then these two flagship rollback templates can't be 1-shot by agents against the proxy.
- **`sparkwing run` has no human-readable summary** — JSONL-only, no final PASSED/FAILED line (cited rounds 1 & 2). Add `--sw-pretty`/TTY autodetect. (medium) — rising priority.
- **`pipeline new --hidden` writes a `hidden:` key the parser rejects** — latent bug found while auditing the schema (`hidden` isn't a valid field). Either add the field or have `--hidden` record it elsewhere. (small)
- **Positive log signal for a passing `.Verify`** (verify-rollback): a successful health check is invisible in run output. Needs a `verify_start`/`verify_pass` event in the runner. (small–medium)
- **`unknown pipeline` should hint** "compiled but no `Register(\"X\")` found" when the name isn't registered (parallel-checks). (small)
- **Missing local-runnable templates** for very common shapes with no on-ramp: test-matrix (fan-out), build-and-publish-binary (generic/local), local Postgres migration (non-ArgoCD), per-directory lint fan-out, http-fetch-retry, integration-test-with-service. (medium each)
- **Scaffold-time compile check** — run `go build` at `pipeline new` time so broken scaffolds surface immediately, not on first `sparkwing run`. (small)
- **Run-level "recovered" signal** when an `OnFailure` node succeeds (today a successful rollback still exits non-zero / status=failed). Legit design question. (medium)
- **`sw.Bash`/`sw.Exec` run from repo root (WorkDir), but `run_start` logs `cwd=.sparkwing/`** — misleading; clarify the log field. (medium)

- **`go-test-build-deploy-k8s` + `go-test-migrate-deploy-argo` don't compile against released deps** — they (and the `rollback` module) call `kube.Apply/SetImage/RolloutUndo` and `gitops.Revert`, which exist on sparks-core HEAD but are post-`v0.24.0` and unreleased. Fixing this needs a sparks-core module release (deferred: releases are on hold). Until then these two flagship rollback templates can't be 1-shot by agents against the proxy.
- **Positive log signal for a passing `.Verify`** (verify-rollback): a successful health check is invisible in run output. Needs a `verify_start`/`verify_pass` event in the runner. (small–medium)
- **`unknown pipeline` should hint** "compiled but no `Register(\"X\")` found" when the name isn't registered (parallel-checks). (small)
- **Missing local-runnable templates** for very common shapes with no on-ramp: test-matrix (fan-out), build-and-publish-binary (generic/local), local Postgres migration (non-ArgoCD), and a cluster-free `.Verify`+`.OnFailure` rollback demo. (medium each)
- **Human run output** (`--sw-pretty`/TTY autodetect) — JSONL is agent-friendly but hostile to humans. (medium)
- **Scaffold-time compile check** — run `go build` at `pipeline new` time so broken scaffolds surface immediately, not on first `sparkwing run`. (small)
- **Docs**: state the 3-way name binding (`name:` == `Register("name")`, `entrypoint:` == struct type) and that a failed `.Verify` reaches `.OnFailure` with `Stage == StageVerify`. (trivial)
- **Run-level "recovered" signal** when an `OnFailure` node succeeds (today a successful rollback still exits non-zero / status=failed). Legit design question. (medium)
