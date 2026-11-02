# Fast release: production in about ten minutes

Status: proposal for Korey (2026-11-02). Short tests gate; long tests verify.

## Goal

A change goes from commit to production in about ten minutes, mostly on the developer's own machine, through Sparkwing itself. Long suites stop blocking deploys and instead verify every deploy soon after it lands. The pipeline is also the public demo of how to split short and long tests.

## Path

| Stage | Where | Budget | Blocks |
| --- | --- | ---: | --- |
| Lint, build, affected tests, critical tests | local (`sparkwing run gate`) | ~2 min | yes |
| AI review | Sparkwing job | ~2 min | yes |
| Staging deploy and integration gate | Sparkwing Cloud, staging | ~4 min (cold node accepted) | yes, auto-rollback |
| Production deploy and smoke | Sparkwing Cloud, production | ~2 min | yes, auto-rollback |
| Verification after deploy | Sparkwing Cloud cron | ~10 min wall, parallel | pages, does not block |

### Affected and critical tests

The gate tests changed packages and their direct importers, with results cached by input hash so unchanged packages cost nothing. A small set of tests tagged critical always runs regardless of what changed: team and run isolation, billing and refund invariants, trust and source credentials, secret masking, store baseline checks. The tag lives next to the test so the list cannot drift from the code.

### AI review

A pipeline job reviews the diff with Codex Sol. P0 or P1 findings fail the job and are its output; lower findings are reported, not blocking. The reviewer's session id is a job output keyed by branch, so a re-run after fixes resumes the same reviewer and checks that its earlier findings were addressed.

### Staging integration gate

The repository's `example` pipeline becomes the integration test: it already tours the SDK (jobs, fan-out, approvals, groups, optional and skip-if jobs, failure policy and backoff, timeouts, child starts, summaries). It grows to cover the remaining features: matrix, outputs between jobs, dynamic children at another commit, scoped secrets, OIDC to the staging role, dependency caches, memoized steps, artifacts, prepared restore, failed-only retry, cancellation, Docker on Cloud, mixed local and Cloud jobs, logs and history through the API, and an armed schedule that fires once. Each feature asserts its own result so a regression fails by name. It then triggers `sparkwing-ci-smoke` as a reference pipeline in a second repository. That is a separate run started through the trigger API, because cross-repository children are refused by design. A TypeScript twin of the example runs in the same gate.

No warm node is kept on staging (Korey, 2026-11-02); the gate accepts one cold start.

### Promotion

A green staging gate deploys production automatically, then runs a production smoke in the QA fixture teams. A failed smoke rolls production back to the previous digests.

### Verification after deploy

A Sparkwing Cloud cron runs only when there has been a deploy since its last run, debounced so a burst of deploys costs one run. It fans the long suites out across Cloud nodes: full race, the PostgreSQL store suite, acceptance, and a production grid sample. A failure pages and names the deploy, with a one-command rollback. Stormwing keeps probing production independently.

## Where testing happens: on the tree, not on main

A gate result belongs to a tree, not a branch. Sparkwing records "tree T passed gate G on runner R at engine E". A commit whose tree equals T inherits that result, wherever and on whichever branch it was produced.

- An agent rebases its branch onto current main, runs the gate on its own machine, and lands by fast-forwarding main to that commit. Main's tree is then exactly the tree that passed, so main is tested without running anything again.
- If main moved before the fast-forward, the fast-forward is refused, the agent rebases again, and the tree changes, so the gate runs again. There is no "tested on an older main" case.
- The result is trusted because Korey trusts his own machines; that is the point. The record still names the runner, so a result from an unexpected machine is visible.
- Main's own pipeline, on push, first asks whether its tree already has a passing gate result. If yes it skips straight to deploy; if no (a direct push, a merge commit, a hand edit) it runs the gate itself.
- The engine and the gate definition are part of the result key, so changing the gate or upgrading Sparkwing invalidates old results instead of silently reusing them.

## One DAG, two entry points

There are no pull requests. One pipeline definition serves both moments; jobs skip themselves when their result is already proven.

| Job | Before merge (agent's branch, own machine) | After merge (main) |
| --- | --- | --- |
| Lint, build, affected and critical tests | runs | skipped when the tree already passed; runs otherwise |
| AI review | runs; P0 or P1 fails the run | never runs: review happens before merge only |
| Staging deploy and integration gate | not run | runs |
| Production promotion and smoke | not run | runs |
| Verification after deploy | not run | the debounced cron picks it up |

Skipping uses `SkipIf` with the tree-keyed lookup, so the run record still shows every job and says why it was skipped. A merge without a prior passing tree (a hand edit on main) runs the gate before deploying, with no review.

## The agent landing workflow (Bitwing)

Sparkwing executes and records; Bitwing makes agents follow the landing procedure and refuses to finish without evidence. A `sparkwing-land` Bitwing workflow, beside the existing `sparkwing-release` workflow:

1. `rebase`: rebase onto current main. Evidence: the base commit.
2. `gate`: `sparkwing run gate` on the rebased commit. Evidence: the run id and tree hash.
3. `review`: the AI review job for the same tree, resumed from the branch's reviewer session if one exists. Evidence: review run id with no P0 or P1 findings.
4. `land`: fast-forward main to the tested commit. Evidence: main's new head, which must equal the tested commit.
5. `watch`: the staging gate, production promotion and smoke for that commit. Evidence: the deploy run ids.

If any step fails, the workflow points back to the failing step; a review failure sends the agent back to fix, then re-run gate and review with the same reviewer. `workflow finish` succeeds only when every step has its evidence, which makes "did the agent actually run the pipeline after rebasing" a recorded fact instead of a hope.

Several agents landing at once serialize naturally: only one fast-forward can win, the others rebase and re-gate. A cheap affected-test gate keeps that retry short.

## Compute placement: your own machines first

The whole path runs the same way locally and on Cloud; only placement changes. The point is the product's promise: CI compute on machines you already own, with Cloud as overflow.

- Lint, build, affected and critical tests, and the AI review run on the developer's machine through the local controller, as they do for `sparkwing run gate` today.
- The staging gate, deploy jobs and the verification cron prefer Korey's own runners (the WSL workstation and the Mac), and overflow to Sparkwing Cloud only when those are busy or offline.
- Trust decides which machine may deploy. Personal machines never claim repository-proven runs, so a job that needs deploy secrets or OIDC cannot run on a personal enrollment. Enrolling the same hardware as a repository-scoped runner for `sparkwing-dev/sparkwing` gives it proven runs, so deploys can run at home without widening the trust model.
- Cost reporting shows how many job-minutes ran on owned machines versus Cloud, which is the number worth posting.

## Exceptions that keep a gate

- Schema changes: a change to `internal/store/schema.sql` runs the full suite before deploy, because a store change is not undone by redeploying.
- CLI and SDK releases: customers install them, so a rollback cannot reach their machines. Releases publish immediately, but the signed index's `latest` pointer advances only after verification passes.
- Race tests move after deploy and must keep running there.

## Open decisions

1. Does a P2 AI-review finding block, or only P0 and P1? Recommendation: only P0 and P1.
2. Which reviewer model? Recommendation: Codex Sol, with Opus for security-sensitive paths (billing, isolation, trust, secrets).
