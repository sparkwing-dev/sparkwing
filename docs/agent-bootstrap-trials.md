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

### Round 3 — 6 agents (postgres-integration, release-on-main, backup-pipeline, test-shards, approval-deploy, notify-on-failure)

Result: 6/6 ran, avg ease 3.67/5 (↓ from 4.17) — not a regression: the
harder shapes (service containers, approval, sharding, recovery) exposed
deeper *functional* bugs, not just docs. Confirmed win: `release-on-main`
used `SkipIf(run.Git.Branch != "main")` "exactly as the SDK doc
recommends" — the round-2 Git-fields doc worked.

### Round 4 — fixes for round-3 findings

| Theme (agents) | Change | File(s) | Effort |
|----------------|--------|---------|--------|
| `WithServices` uses `--network host`, no published ports → unreachable on macOS/Windows Docker Desktop (postgres-integration; I documented it round 2) | publish `127.0.0.1:<Port>:<Port>` when `Port` set; host-net only as Linux fallback; fixed `Port` doc + sdk section | `sparkwing/services/services.go`, `docs/sdk.md` | small |
| `runs approvals list` 100% broken (flags parsed as subcommands); `approve`/`deny` hidden from `--help`; `sparkwing approve` example doesn't exist (approval-deploy) | router defaults to `list` + dispatches approve/deny directly (no double-dispatch); removed dead `runApprovals`; help lists approve/deny; fixed examples + comments | `cmd/sparkwing/main.go`, `approvals.go`, `help_registry.go` | small |
| `ContinueOnError` vs `Optional` undocumented — the distinction *is* the recovery task; `ContinueOnError` misleadingly doesn't flip run status (notify-on-failure) | added a comparison table + the `OnFailure`+`Optional` combo note + documented the `Failure` struct/`Stage` | `docs/sdk.md` | small |
| `.Verify(fn)` signature, `Bash` has no implicit `set -e`, WorkDir=repo-root, `Git.Branch` empty on unborn HEAD (backup-pipeline, release-on-main) | documented all four | `docs/sdk.md` | trivial |
| `pipeline new --hidden` wrote a `hidden:` key the parser rejected | added `Hidden` field + known-field + wired `--all` filtering (was `_ = includeHidden`) | `pkg/pipelines/pipelines.go`, `cmd/sparkwing/action.go` | small |
| stale `pipelines.yaml` in explain | → `sparkwing.yaml` | `cmd/sparkwing/action_explain.go` | trivial |

Validation: rebuilt/reinstalled; `runs approvals` (bare / `-o json` /
`--help`) all work; `--hidden` parses + filters; build+vet clean.
WithServices port fix verified by build — behavioral re-test queued for
round 5 (postgres/redis shape).

### Round 5 — 6 agents (redis-cache-test, gated-prod-deploy, docker-image-build, scheduled-report, diamond-dag, env-promote)

Result: 6/6 ran, avg ease 3.83/5 (↑ from 3.67). **Confirmed win:**
`gated-prod-deploy` used `runs approvals approve` smoothly end-to-end —
the round-4 CLI repair landed. **Key negative result:** the round-4
`WithServices` port fix did NOT reach the agent — redis-cache-test still
hit `--network host` / connection-refused and had to hand-roll a
host-network test container.

### Critical insight: what reaches agents without a release

A scaffolded pipeline is a Go program that **links the released
`sparkwing` SDK** (pinned in its `.sparkwing/go.mod`, currently v0.8.0).
So:

- **Reaches agents via `bin/install.sh` rebuild (no release):** CLI
  behavior (`info`, `pipeline new`, `runs approvals`, flags, breadcrumb),
  the **embedded docs** (`pkg/docs/content/*`), and **template content**
  (rendered by the binary). Validated working: approvals, Git docs,
  breadcrumb, JobFn template fix, naming.
- **Does NOT reach agents without a sparkwing release:** anything in the
  `sparkwing/...` **SDK packages** a pipeline imports — `services`
  (the WithServices networking fix), the orchestrator `Failure`
  serialization, the `kube`/`gitops` APIs the two rollback templates
  need. These are correct in the working tree but stranded at v0.8.0
  for agents.

Consequence: the WithServices doc was corrected to describe the
**released** behavior + the macOS host-network workaround (the code fix
stays staged). The reachable, no-release work below is where ongoing
effort goes.

Round-5 doc fixes (reachable, shipped): honest WithServices networking
caveat; file helpers (`WorkDir()`/`Path()`/`WriteFile`) clarified as
package-level funcs taking no `ctx` (two agents hit `RunContext has no
method WorkDir` / `WorkDir(ctx) too many args`); marked the `Verify`
proposal doc IMPLEMENTED (it contradicted the SDK reference).

## Deferred / larger asks (current)

**Release-gated** (need a sparkwing SDK release to reach agents; on hold
per "don't push releases yet"):

- `WithServices` port-publishing fix (staged in `sparkwing/services/services.go`).
- Controller verify-recovery `Failure` serialization (repro test on branch `test/controller-verify-recovery-repro`).
- `go-test-build-deploy-k8s` + `go-test-migrate-deploy-argo` (need post-v0.24.0 `kube`/`gitops` released).

**Reachable now (no release) — next builds, in priority order:**

Top of the list — these are now the dominant remaining feedback, cited
across multiple rounds:

- **`sparkwing run` has no human-readable summary** — JSONL-only, no final PASSED/FAILED line; an exit-0-with-skipped-node reads as success. Cited rounds 1, 2 & 3. Add `--sw-pretty`/TTY autodetect with a per-node outcome table. (medium) **← next up**
- **No local-runnable templates** — every registry template targets cloud infra; common local shapes (test-matrix/shards, build-and-publish-binary, local Postgres/Redis integration via `WithServices`, branch-conditional, check-with-recovery, approval-gated deploy, generic archive+checksum) have no starter, so agents hand-roll every time. Cited every round. (medium each) **← next up**

Smaller / specific:

- **`go-test-build-deploy-k8s` + `go-test-migrate-deploy-argo` don't compile against released deps** — call `kube.Apply/SetImage/RolloutUndo` + `gitops.Revert`, post-`v0.24.0` and unreleased. Needs a sparks-core release (on hold).
- **Positive `verify_start`/`verify_pass` log** — a passing `.Verify` is invisible in run output (verify-rollback, backup-pipeline). (small–medium)
- **`unknown pipeline` should hint** "compiled but no `Register(\"X\")` found". (small)
- **Scaffold-time compile check** — `go build` at `pipeline new` time. (small)
- **`-C/--sw-cd` is inconsistent** — works on `run`/`new`/`explain`, not `list`/`describe`/`runs *`. Make it uniform. (small)
- **`run_start` logs `cwd=.sparkwing/`** but steps run from repo root — clarify the log field. (small)
- **Local-run heartbeat/orphan ~60s timeout is undocumented** and orphans a foreground `run` blocked on an approval gate. (medium)
- **Failure error payload inlines the whole bash script 3×** — buries the real `FAIL` line. (medium)
- **Scaffold pins SDK `v0.8.0`** regardless of the installed binary version; sync it. (small)
- **Scaffolder `.gitignore` omits `dist/`** so first run dirties the tree. (trivial)
- **Run-level "recovered" status** when an `OnFailure` node succeeds (today still exit 1). Design question. (medium)
- **`WithServices` ReadyCmd lifecycle logging** — log the probe + exit code; don't print "ready" unless it passed. (small)
