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

The gate tests only the changed packages, with results cached by input hash (Go's test cache) so unchanged packages cost nothing. Importers still build and vet before merge; their tests, and every other suite, run after deploy under `sparkwing run gate -- --full`, which hosted CI also runs (lead, delegated by Korey, 2026-11-02). A change to a `go.mod`, a `go.sum` or `internal/store/schema.sql` runs its whole module. A small set of tests tagged `//sparkwing:critical` always runs regardless of what changed: team and run isolation, billing and refund invariants, trust and source credentials, secret masking, store baseline checks. The tag lives on the line above the test so the list cannot drift from the code.

Measured on the WSL workstation under other load (2026-11-02): a one-line change to a lightly imported package took 148 s for `sparkwing run gate` (gate job 1 m 56 s, lint 64 s the long pole), against about 15 minutes for the full gate. A change to a package that the store, controller, CLI or acceptance suites import took 14 minutes with direct importers included, which is why importers moved after deploy.

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
- The result is trusted because the run was proven for the repository, which Korey's repository-scoped runners claim; the record still names the runner, so a result from an unexpected machine is visible.
- Which results may satisfy main's skip (lead, delegated by Korey, 2026-11-02, under decision 84, after a security review): only a full-mode pass from a run proven for the repository, meaning its commit came from GitHub (an App delivery, an Actions push, a schedule) or the controller fetched it from GitHub, whose sealed options selected every job and step: no `--sw-only`, `--sw-start-at` or `--sw-stop-at`, and no dry run. The controller enforces this in the lookup and again when the output is read. Korey's machines get no special trust: they are repository-scoped runners that claim proven runs. A pass from another team, another repository or an asserted (plain local) run never satisfies the skip. An earlier draft let an operator designate trusted machines whose published local runs counted; it was cut because the designation named runners any member could claim, and no working path published a run under a machine credential. The same review found that a proven run's submitter could narrow it with `--sw-start-at` and still record a pass, which the clean-options rule closes.
- Trusted hosts and eligible runs (decision 88, simplified by Korey on 2026-11-04 after the 2026-11-04 security sweep): every job of a counted run that did not skip must have been claimed on a trusted host, and one predicate (`hostTrusted` in `internal/store/claimant_facts.go`) decides that for both `nodes.trusted_host` and the cache scope a job may write. A host is trusted when its provider attests it fresh (Sparkwing Cloud, GitHub-hosted Actions) or when it is a machine an admin scoped with `--repos`, which takes only repository-proven work. `--trust verified` was cut because `--repos` already gave every shared runner that rule. A team's own launcher pods take their credential's trust and are no longer fresh. An untrusted host never writes the shared verified cache, even under `high_trust`, so a counted pass never restores bytes an untrusted host wrote. The claim alone records `trusted_host`. Intake seals `proof_eligible` from an allow-list of run options that only place, schedule or present a run (`runspec.Request.ProofEligible`); any other option, including one added later, keeps the run from counting, replacing the earlier list of forbidden options checked in SQL.
- How it works: the gate's `record-tree` job outputs the tree, the gate fingerprint, the engine version and the mode. Each gate job holds a `sw.RefToLastRun` reference to that output declared with `sw.FromProvenRunAtCommit()`, and the controller answers with the newest successful clean `gate` run at the reader's own sealed commit proven for its repository. The job skips only when that pass is full mode and names the same tree, fingerprint and engine. Affected-mode passes never let another run skip, because they judged a change against a baseline the reader cannot check. A controller that predates the filter is refused, not ignored.
- The landing flow: the agent pushes its branch, a repository-scoped runner on Korey's machine runs `sparkwing run gate -- --full` fetched from GitHub, and main, fast-forwarded to that commit, skips its gate. A plain local `sparkwing run gate` still gives the developer fast feedback but never lets main skip.
- A trusted pass from your own machine (decision 89, built 2026-11-04): `sparkwing pipeline trigger gate --profile P --trusted -- --full` runs the gate on this machine's own registered runner and produces a pass CI counts. The CLI reads this machine's runner from its config, refuses unless that runner is enrolled with `--repos` against the profile's controller, the checkout is clean, and HEAD is the head of the same branch on origin, then pins the run to that runner by name and submits no environment, so intake seals it proof eligible. Everything that decides whether the pass counts stays on the controller, unchanged: the controller fetches the commit from GitHub (repository proof and `fetched_sha`), the runner checks out that fetched commit itself, `hostTrusted` marks each claim, and `record-tree` names the tree it judged. No new proof class, provenance kind or trust rule exists, so a lying or modified CLI gains nothing: an unenrolled machine, a personal runner, an unpushed commit or a dirty tree it failed to refuse still yields a run that does not count, and CI still compares the recorded tree with its own. Considered and rejected: a CLI-hosted run (`--runner here`) claiming with the machine's runner credential over the developer's own checkout. It would let a run count without a push and reuse the developer's checkout directly, but it needs a new proof path for here runs in intake and the claim, which is the shape the earlier trusted-machine draft took before its security review cut it. The cost of the chosen design: the commit must be pushed first (CI needs it anyway), and the gate runs in the runner's checkout. The trust boundary is the OS account the runner service runs as, not the run (security review, 2026-11-04): any process running as that user (the developer, coding agents, tests and tools from asserted local runs) can read the runner's credential and report a pass directly, and shares its HOME, Go configuration and build cache, Sparkwing home and PATH with the runner. Korey's runners run as his own user (decision 86), which decision 89 accepts. For customers the docs say to enroll a workstation with `--repos` only for people the team would let bypass the required gate check, or to run the runner service under a dedicated OS user with its own HOME and SPARKWING_HOME; a dedicated-user install path is follow-up work. Such a runner also runs any pushed branch of its repositories as that user. `--trusted` does not report whether the finished run counted, because the member run view exposes neither `proof_eligible` nor `trusted_host`; adding them is a wire change left for later. A `sparkwing run gate --sw-trusted` spelling and a dirty-tree file-hash proof are follow-ups if wanted.
- Known limits: the lookup is keyed by commit, not tree, which fits fast-forward landing; a rebase that reproduces a tree under a new commit runs the gate again. Jobs cannot see `--sw-only`, `--sw-start-at` or `--sw-stop-at`, so the pipeline refuses only a dry run itself; the controller is the guard for the others. Self-serve designation and typed approvers wait for the agentic-orchestration extraction (decision 84).

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
- Trust decides which machine may deploy. Personal machines never claim repository-proven runs, so a job that needs deploy secrets or OIDC cannot run on a personal enrollment. Both of Korey's machines (the WSL workstation and the Mac) are enrolled as repository-scoped runners for `sparkwing-dev/sparkwing`, so both may deploy (Korey, decision 82). Deploy jobs get short-lived OIDC credentials minted per run rather than long-lived keys on disk, so nothing durable sits beside the agents that also run on the workstation.
- Cost reporting shows how many job-minutes ran on owned machines versus Cloud, which is the number worth posting.

## Exceptions that keep a gate

- Schema changes: a change to `internal/store/schema.sql` runs the full suite before deploy, because a store change is not undone by redeploying.
- CLI and SDK releases: customers install them, so a rollback cannot reach their machines. Releases publish immediately, but the signed index's `latest` pointer advances only after verification passes.
- Race tests move after deploy and must keep running there.

## Open decisions

1. Does a P2 AI-review finding block, or only P0 and P1? Recommendation: only P0 and P1.
2. Which reviewer model? Recommendation: Codex Sol, with Opus for security-sensitive paths (billing, isolation, trust, secrets).
