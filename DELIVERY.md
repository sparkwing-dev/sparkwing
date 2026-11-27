# Delivery

Use the smallest evidence that covers the change. The author of a change owns the decision;
this file is a menu and checklist, not a command that every change must run.

## Xwing workflows

The tracked `.xwing-env.yaml` declares dependency setup, local builds, tests,
and foreground execution. Run `xwing app commands` for the available tasks.
`xwing env land` uses the existing repository gate before fast-forwarding and pushing
main, then applies Xwing's default archive retention. Public releases remain
separate. Stateful CLI commands use their normal configuration; use a candidate
launcher when testing isolated tool state.

## Landing a change

Agents and the lead submit every landing through main's trusted
`bin/queue-land.sh` entrypoint below. It invokes Sparkwing with main's pipeline
definition against the candidate checkout. Ordinary interactive Land remains
available for operator use from a clean work branch. Operators must fetch main
and pin its pipeline source too. Set `candidate` to the absolute worktree path:

```bash
candidate=/absolute/path/to/candidate
git -C "$candidate" fetch origin main
main_sha="$(git -C "$candidate" rev-parse --verify 'origin/main^{commit}')"
sparkwing run land -C "$candidate" --route local --pipeline-ref "$main_sha" --dry-run
sparkwing run land -C "$candidate" --route local --pipeline-ref "$main_sha" --stop-at verify --allow-risk destructive,prod
sparkwing run land -C "$candidate" --route local --pipeline-ref "$main_sha" --allow-risk destructive,prod
```

Land refuses any other step bound before a job runs, provided the installed engine
forwards the bounds into the plan's run context. A bound narrows only the job that
holds it, so the land job would still push a tree whose skipped checks never ran.
Only `--stop-at verify`, which stops before the push, and `--dry-run`
accept bounds. An engine that does not forward the bounds leaves them empty, and
Land then plans a bounded run as unbounded, so pass no other bound.

An unpinned ordinary Land compiles the candidate's definition. For a registry
change, the operator must also review its `land` entrypoint mapping and arguments:
the pipeline-source pin selects code, while candidate aliases still apply.

Lands run on owned hardware. Policy, gate, review and push use that machine.
The land queue invokes it with `--route local`.

### Queue focused lands

Extract the submission script from freshly fetched main, then invoke that copy.
This works even when a local checkout named `main` is behind the remote. Set
`candidate` to the absolute clean worktree path:

```bash
candidate=/absolute/path/to/candidate
(
  set -e
  git -C "$candidate" fetch origin main
  main_sha="$(git -C "$candidate" rev-parse --verify 'origin/main^{commit}')"
  queue_script="$(mktemp)"
  trap 'rm -f "$queue_script"' EXIT
  git -C "$candidate" show "$main_sha:bin/queue-land.sh" > "$queue_script"
  bash "$queue_script" -C "$candidate" --brief 'Describe the goal to review'
)
```

Track the returned run:

```bash
sparkwing queue list
sparkwing runs status --run <returned-run-id> --follow -o json
```

The script fetches `origin/main`, resolves its exact SHA, verifies its own blob
against that commit, and refuses a candidate `.sparkwing/sparkwing.yaml` that
differs from main. That registry determines the `land` entrypoint even when
pipeline code is pinned. Restore main's registry before using this queue.
Registry changes require a separately reviewed operator landing with main's
pipeline source pinned. The refusal prints that exact command, including the
resolved SHA, checkout and brief.
The submitted command is:

```bash
sparkwing run land -C <candidate> --detach --route local --pipeline-ref <main-SHA> --allow-risk destructive,prod -- --queued --queued-main <main-SHA> --brief 'Describe the goal to review'
```

`--pipeline-ref` compiles main's complete pipeline source and local module
dependencies. `WorkDir` remains the candidate checkout. The queued preconditions
compare freshly fetched main to `--queued-main` before merging and again when
capturing the gated HEAD, then refresh main again before the queued push. If main
moved while the entry waited or the checks ran, the run refuses
and names the submission script. Enqueue again to compile the new gate.
The remote update uses `--force-with-lease=refs/heads/main:<planned-base-SHA>`
as an atomic compare-and-swap. Before sending it, Land proves that the planned
base is an ancestor of the gated commit, so the update can only fast-forward
from that exact base. An already-at-target/no-op result is a refusal, because it
does not confirm an update from the planned base.
It never falls back to the branch's definition. Use `--foreground` on the script
to wait in the terminal, or `--dry-run` to preview without a push.

Changes to `land.go`, pre-push, or other pipeline Go files use this same path.
Main's gate judges the merged checkout, and its `affected-tests` job runs the
branch's own focused pipeline tests from the changed `.sparkwing/jobs` package.
Run the focused pipeline tests before submission too. The proposed pipeline
definition becomes the trusted gate only after it lands on main. A failing
proposed pipeline test blocks its landing even when main's definition plans.

This is a local delivery boundary. The SDK run context exposes the candidate
commit but no pipeline ref, so `--queued-main` cannot independently prove which
code the submitter selected. The verified script owns that selection. Bypassing
the entrypoint or making a direct push is not stopped locally. Server-side branch
protection with a status issued only by main's gate is separate enforcement.

Runtime gate inputs remain candidate-controlled. Main's compiled jobs execute
candidate scripts such as `bin/check-changelog.sh`, `bin/sync-docs.sh --check`,
`bin/gen-cli-docs.sh`, and `bin/gen-config-docs.sh`, and read candidate policy
such as `.review.yaml` and `.golangci.yml`. A branch can weaken a check by making
its script return success or relaxing its configuration. Review must inspect
those changes until the runtime checkers and their policy are separately pinned.
This change protects pipeline compilation, not all gate enforcement inputs.
Running a script from main's checkout alone does not fix that boundary: these
scripts derive their repository root from their own path, then compile checker
or generator code from that root. Trusted checker code needs an explicit
candidate input root, including its transitive helpers and configuration.

Each entry is a durable `land` run in the dashboard. Detached intake returns
its run ID; keep that receipt to wait for the entry, inspect logs and failures,
or cancel it with `sparkwing runs cancel --run <id>`. `runs status --follow`
exits successfully only for a successful run or completed dry run; failures
and cancellations exit 1. Successful run detail includes the landing record
and pushed commit. The local controller and runner wait for new work when the
queue is empty; no queue process or text file needs restarting.

All lands share one capacity-one run concurrency group, including ordinary
full-gate lands. Admission and operator priority determine ordering. A failed
entry releases its slot and leaves later entries eligible. Queued lands
require a non-empty explicit brief, such as `--brief 'Describe the goal to review'`.
Missing or whitespace-only briefs fail during planning before submission; the
review uses the supplied goal rather than inferring one from the branch name.
Ordinary interactive lands keep their objective fallback. Queued lands refuse
a changed submission or dirty checkout,
merge fetched `origin/main` before capturing HEAD, then run source policy and
the fast pre-push checks against that merged checkout. After both pass, the
inline `affected-tests` job runs review and the hosted Gate's affected short
and critical Go tests as independent steps under one fail-fast owner. A review
failure cancels in-flight tests through the existing step cancellation path,
which joins child exit and drains output. A test failure cancels review.
Push waits for policy, fast checks and both steps to pass on the same tree.
The fast job reserves its compiler allowance separately (nine cores on a
16-CPU host), releasing it before the combined job reserves seven test cores
plus 0.25 for review. These reservations are admission accounting, not CPU
limits. Fast checks have a 45-minute execution deadline and a 30-minute
no-progress deadline. The combined job has 45-minute execution and no-progress
deadlines, and each reviewer model call inside it stops after 30 minutes, so a
silent reviewer cannot hold the land slot past review's own bound. Review
starts after policy and fast checks; small-change measurements must include
that start delay.
They push only when the branch, HEAD and checkout still match. If main moves again,
the push fails rather than merging an unchecked tree; enqueue again after
inspecting the failure. A conflict aborts the merge and records its remedy in
the failed run.

Queued authorizing checks bind their SDK process to the original local owner
checkout, using the immutable repository path rather than an isolated checkout
of the submitted pre-merge commit. Preconditions capture the merged commit and
tree, the run ID and every required policy, fast, review and test check. Each
check verifies that owner branch, HEAD and cleanliness before and after its body,
and records the actual tree and outcome. Both verification and queued push
require every receipt for that run to pass on the tree being pushed. Missing,
unreadable, failed or mismatched receipts block landing. Receipts live under
`<git common dir>/sparkwing-land-checks/<run>/<role>/<step>.json`; logs also
record each full checked tree. Review uses the pinned `queued-main` SHA, so it
does not depend on a worker clone carrying `origin/main`. Ordinary standalone
review still compares against `origin/main`.

This mode uses the queue's owned-hardware landing policy: local affected Go
tests are the primary test gate. Hosted CI supplies additional verification.
It omits the broad desktop and Cloud gates. Run focused lane tests before submitting.
Ordinary `land` retains its full gate. This is a repository pipeline feature;
a different repository must define its own landing policy before using it.

Review prompts retain UTF-8 text patches unchanged. Text-like files with
invalid UTF-8 keep their patch with only invalid bytes escaped as `\xNN`,
marked "contains invalid UTF-8; bytes escaped". Files containing NUL or more
than 30% invalid/control bytes use binary path, byte size and SHA-256 metadata
for each side. Changed names use NUL delimiters and literal pathspecs for
strict-review routing. The prompt budget truncates at a UTF-8 rune boundary.

### Local affected Go tests

The pre-push hook runs its fast checks for every push. Updates targeting
`refs/heads/main` then run `affected-tests`; branch pushes finish after the fast
checks. Each non-deletion main update must name the checked-out HEAD's OID;
`git push origin topic:main` from another commit is refused with a checkout
remedy before test selection. Every ref update is inspected, including main updates
after other refs. Deletions add no affected tests. An explicit
`sparkwing run pre-push` also runs the test phase. Queued Land
runs those test bodies alongside review after policy and fast checks pass.
Ordinary Land already runs the full Gate and adds no duplicate test job.
The hook and queued Land read the complete committed push range, including deletions and
both paths of renames. An unrelated staged file cannot narrow the tests.

Queued Land stops admitting packages after its first observed test or build
failure. Packages already admitted finish all their selected bodies, and the
job reports every failure it observes before returning. Pending short and
critical packages are listed by module and phase, with exact local commands
to rerun all selected work. A successful run still executes every selected
short and full critical test. The pre-push hook, explicit PrePush, Gate and
ordinary Land keep their collect-all behavior.

Queued package commands compile with `-p 1` and retain the runtime width of
their assigned module or store lane. The coordinator shares the module and
store allocation with collect-all execution and bounds admission within each
lane. The three store shards form one admitted package costing three command
slots: a failure in one shard lets all three finish, and a pending store
package appears once in the skipped inventory. With store and other root
packages selected alongside pipeline work at allowance seven, pipeline uses
one slot, root's ordinary lane uses three and its store shards use one each.
Critical modules retain the full allowance after all short work joins.

The coordinator joins admitted commands and drains their output before
cleaning the shared test home. Explicit run cancellation and deadlines still
interrupt running work through the existing process-owner cleanup. Package
admission can change command startup and cache costs; the correctness proof
does not measure those costs.

Affected tests for main use the remote OID Git advertises on hook stdin,
independently of local `origin/main`. Main updates must fast-forward; a rewind
or unverified remote base is refused with a fetch remedy. Creating
main selects every committed Go module's short tests and the full critical set.
All changes count, including type changes and both paths of renames.

The job reuses the hosted Gate's package planner and command runner. It runs
`go test -short` over the reverse dependency closure, including test imports
and registered file readers, then the critical set without `-short` when Go
inputs changed. Changelog-only changes select no tests. Other documentation
runs its registered readers. The `affected-tests` job has a 45-minute execution deadline,
separate from the fast checks' one-minute warning. Each Go package test binary
retains its 25-minute timeout; this is not a deadline for the entire multi-package
Go command. The local test job uses `max(1, (NumCPU-1)/2)` for both
its scheduling reservation and combined Go command allowance: seven on a
16-CPU host, regardless of the submitter's `GOMAXPROCS` or the SDK's `CI` marker. This is
not an aggregate CPU cap: seven test processes can each use seven Go execution
threads, besides compiler and nested subprocess work. The job reports package
counts, critical-test counts, failures, package timings and test timings.

Repository-scanning source guards carry the existing `//sparkwing:critical`
tag because their filesystem reads are absent from the import graph. Every Go
input change, including a test in another committed module, runs the named Git
fixture, source ownership, ANSI, SQL ownership, configuration-directory,
environment documentation, source-hint, SDK documentation, SDK boundary and SDK sandbox guards
without `-short`. The guard inventory and its cross-module selection are pinned
by `TestAffectedTestsAlwaysSelectRepositoryScanningGuards`; an unsafe unrelated
Git fixture is exercised by
`TestAffectedTestsRejectUnsafeGitInUnrelatedTestPackages`. These tags share the
existing critical phase and deadline. Documentation-only selection is unchanged.

The shared affected-test runner sets nonempty `GOFLAGS=-mod=readonly` for
selection and execution. This overrides process and saved flags while keeping
other saved Go settings, including `GOPROXY`, `GOPRIVATE` and `GONOSUMDB`.
Hosted affected Gate and ordinary Land's merge recheck use that same flag
policy and retain their existing CPU policy. Other affected build and vet
checks keep their existing flag and runtime CPU policies.

When the root selection includes `internal/store`, its short tests use three
concurrent commands, each with `GOMAXPROCS=1` and `-p 1`. Other selected root
packages run alongside them with the remaining root-module command allowance. A seven-wide root allowance becomes
four for the other packages and one per store shard. Store-only selections need
an allowance of at least three; selections with other root packages need at
least four. Smaller allowances retain the unsplit short command. Each command
in collect-all execution keeps the existing `GOMAXPROCS`/`-p` pairing; queued
execution uses the singleton compiler width described above. Summed command
widths stay within the allowance, which is not an operating-system CPU cap.

Selected modules run their short commands together when the job's allowance
can give each active module one worker. Root receives the remainder. If root
has no short work, the first active module receives the remainder. At allowance
seven, pipeline and root short tests use one and six; when SDK short tests are
also selected, pipeline and SDK use one each and root uses five. Root's store
shards consume their three workers from that assigned root allowance. A lone
module keeps the full allowance. When there are more active short modules than
workers, the modules run sequentially, each with the full allowance.

All short commands across all modules join before any full critical command
starts. Critical modules run sequentially with the full job allowance.
Collect-all callers continue critical coverage after a short failure; queued
Land stops future admission while already admitted packages finish.

Queued short lanes start selected packages from a static hint list first,
in the order below. Other packages retain their relative order. Hints apply
within existing lanes: they select no additional work, move no package between
lanes and leave critical ordering unchanged. Store shards remain one admitted
package with their existing command order and slots. Hook and Gate callers
keep their collect-all ordering. The list uses no runtime-learned durations.

Compiler identity discovery stats a PATH candidate before resolving its symlinks.
A missing tool costs one stat per nonempty PATH directory. Existing tools retain
the resolved path, size and modification-time identity. There is no process memo,
so a newly installed higher-priority compiler is visible on the next call.
Pipeline and real-Go SDK installation fixtures restrict PATH to the directories
supplying their required tools when they do not test PATH discovery. Product
and job PATH remain unchanged.

The affected runner installs missing locked SDK dependencies with
`npm --prefix sdk/typescript ci --ignore-scripts --no-audit --no-fund` when
acceptance or the native TypeScript host package is selected. It also prepares
dashboard Node dependencies when acceptance needs them. `xwing app install`
includes both dependency roots.

Race tests remain a separate local or hosted lane, available with
`sparkwing run gate --only race-touched`. The broad shape selects five
packages for `-race -short`; store races remain at the release boundary.
Their additional wall time was not measured here. The lane retains its
70-minute execution ceiling and 52m30s package timeout.

When moving from an external queue, let its active land finish, stop that
worker, preserve any manual holds, and submit each remaining eligible worktree
with its goal. Never operate the external worker and the pipeline queue at
the same time: the external worker does not hold the pipeline's serial slot.

### Ordinary land checks

The `preconditions` job refuses a dirty checkout, a detached HEAD, `main`
itself, a CHANGELOG.md that repeats a `###` heading within a release, and a
branch whose merge with a freshly fetched `origin/main`
conflicts, and names the command that fixes each; a branch behind main that
merges cleanly goes ahead. The gate's `policy` job runs next, alongside review: gofmt over tracked Go files,
the formatters, changed-package Windows test compilation, em dashes, tracker IDs, comments, test sleeps, tracked
binaries, home resolution, the docs mirror, changelog lint and links, the
required changelog entry, the docs sidebar, the OpenAPI and API-snapshot checks and the
generated CLI reference (`bin/gen-cli-docs.sh --check`), Markdown lint, config-reference
drift, wire-cut coverage and the TypeScript SDK version. Each answers in
seconds with a warm build cache.

The `sdk-version` policy step runs only when the change touches the SDK's
release inputs: `sdk/typescript/`, `.github/workflows/release.yaml`, which the
release source digest covers, and `bin/typescript-release.mjs`, which computes
it. It verifies the signed public version index and runs the release cut's SDK
decision, so a change to SDK release input fails at land when package.json
names an already published SDK version with a different source digest. The
failure names the
version to bump in `sdk/typescript/package.json`, its lockfile and
`typeScriptSDKVersion`. Ordinary Land repeats the step on the merged tree. When
the index cannot be downloaded or verified, the step passes with an
`UNVERIFIED` warning, because an outage is not something the change can clear;
the release cut's `typescript-release` check still refuses without a verified
index. A change outside those paths does not fetch the index. When main already
needs a bump, every change that touches SDK release inputs fails, including one
that leaves the digest unchanged, until a change bumps the version or restores
the published source.

The `windows-compile` policy step uses the existing changed Go package and
committed module selection. It compiles tests with `GOOS=windows GOARCH=amd64`
when a touched package contains eligible Windows filename or build-tag source.
Source eligible for Windows arm64 but not Windows amd64 adds
`GOARCH=arm64`, including generic arm64 files beside Windows source. A package
with no eligible amd64 files compiles only for arm64.
Negative-only `!windows` constraints do not select a package. Outputs use disposable
scratch, and compilation shares the normal Go build cache. These are cross-compiles;
native Windows execution remains separate coverage.

Gate and land request full source history and tags. Wire-cut coverage refuses
a checkout without a reachable release tag before invoking the existing test.
The policy job keeps its ten-minute cold-build deadline. Once it passes, the rest of
the gate (the same jobs as `sparkwing run gate`) starts while review continues.
Review is the same job as `sparkwing run review`, under `.review.yaml`. A focused
policy/review run selects both with `--only '*[wy]'` (the job names ending in
`w` or `y`). Selecting review
alone includes preconditions but skips policy. The policy
job judges tracked files only, so ignored and untracked scratch does not change
its verdict. The fast push tier and merge-delta recheck use the same tracked
API snapshot check. Markdownlint retains its root and docs scope. Comments and test
sleep checks retain their changed-line scope. The hook tiers still inspect
untracked Go files in range mode. The policy job collects every finding before
it fails. The heavy `gate` job likewise lets
every heavy step report. Every change is reviewed, docs and
comment changes included; the review job never skips by size or path. A diff
whose every file matches `.review.yaml`'s `docs_only` paths (`docs/`, any
`*.md`, `CHANGELOG*`) runs the default review on the cheaper `docs` reviewer
(Claude Haiku) instead of the default model; any other file keeps the default.
The record's `reviewer.model` names the model that ran. Independent default and
security profiles run concurrently, and their findings and sessions are combined
in profile order after both finish. A failed profile leaves the prior review
record unchanged. Review logs time diff assembly, context gathering, each model
invocation (including failed session resumes), settlement and record writing.
Model time includes the reviewer's repository reads and any nested reviewers.
Prompts and findings never appear in these timing logs. Standards prompts keep
the complete rule bodies and omit card routing metadata and terminal vault
backlink lists.
The gate selects its checks from the change scope: vet, build, lint,
the critical tests and the TypeScript SDK suite run only when the change
reaches Go source, a non-Markdown file inside a Go package, module files,
`.golangci.yml`, the store schema or, for the SDK suite, `sdk/typescript/`. A
change that reaches only `docs/` Markdown runs just the SDK suite's type-check of
the docs' TypeScript blocks and its generated-reference check.
A docs, UI or process change runs its own tests (affected packages, frontend
suites when `web/` changed, the docs mirror and source-policy sweeps) plus the
review. `sparkwing run gate -- --full` keeps the broad verification. Pass
`-- --brief '...'` or `-- --skip 'F2=reason'` as for the review pipeline.
The brief is the explicit goal for this land: include the ticket key(s) and
short intent, for example `-- --brief 'Fix Windows shell escaping; leave the
other umbrella work for later'`. It wins over the branch ticket.
Without a brief, review keeps its configured objective sources: branch ticket,
commit ticket trailers or subjects, then commit messages. A cached verdict is
reused only when both the tree and resolved objective match. Clarifying the
brief or editing the resolved ticket text reruns review, even with the same
tree; the current objective supersedes earlier objectives and blocks are
reassessed against it.

The `land` job reads the review JSON from the shared Git directory instead of
resolving a review output blob. Its `verify` step requires the review record's tree to be HEAD's
tree with result `pass` and HEAD unchanged since the preconditions. Its `push`
step fetches `origin/main` and runs
`git push --no-verify origin <HEAD>:refs/heads/main` without force when the
pushed commit's tree is the tree the gate judged or the merged tree whose
delta passed its recheck. The gate and merge-delta recheck cover all fast
push checks and affected tests, so Land's own push skips the hook without
repeating those bodies. It carries the
`destructive` and `prod` risks, as release's `push-tag` does. After a refused
push, the step fetches main and checks its ancestry. It merges main only when
main is not an ancestor of the pushed commit. Otherwise it preserves the
remote's reason and says the push can be retried. The job's output is the
landing record (branch, the commit and tree pushed, the gated tree, base,
merges, gate run, review record path and result). The gate's `test` step reports the affected package count and critical tests.

When `origin/main` is not an ancestor of HEAD at push time, the `push` step
merges it into the branch in the checkout (`git merge --no-ff`, the default
git identity and signing; never a rebase). Independent changelog and migration
fragments merge as separate files. `CONTRIBUTING.md` table additions use union merge;
changes to the same rule need review. The push step
then builds the affected package closure and repeats the changelog, OpenAPI,
API-snapshot, TypeScript SDK version, default and qatrust vet, and fast lint
checks on the merged tree.
Vet and lint cover both the branch range and the merge delta, including new
packages from main. It runs affected tests once over the files that differ
between the gated tree and the merged tree, then pushes the merge. The review
is not run again: it judged the branch's own diff, and the re-check covers what the merge
brought in. It
merges at most three times per land. A conflict aborts the merge, leaves the
branch as the gate judged it and names `git merge origin/main` to run by hand.
A re-check failure leaves the merge on the branch. The `record-tree` pass
names only the gated tree; the merged tree records no gate pass, and the
landing record names both.

## Checks

### Cutting a release

A release is a tag push. The `release` pipeline resolves the version, renames
the collected `## [Unreleased]` section to the version, stages the release notes,
checks the section covers any schema or wire change since the previous tag,
aligns scaffold and pipeline SDK pins to the chosen version with local replacements,
then commits and atomically pushes that commit to `main` with an annotated
`vX.Y.Z` tag. Run it from the sparkwing checkout under a temporary home,
which the pipeline requires so a
newer embedded schema cannot migrate the operational runs store. The release
factory validates the home before intake opens the store, including for a dry
run. An unset `SPARKWING_HOME` or `$HOME/.sparkwing` is refused with
`isolated_home_required` and a temporary-home remedy. The host supplies that
fixed guidance even before secret discovery completes:

```bash
SPARKWING_HOME="$(mktemp -d)" sparkwing run release --dry-run
SPARKWING_HOME="$(mktemp -d)" sparkwing run release --allow-risk destructive,prod
```

The version defaults to a minor bump of the newest tag on origin; pass
`-- --bump patch` for a patch release or `-- --version vX.Y.Z` to name it
(pipeline flags follow `--`). A patch cut whose section carries a `(Breaking)`
entry is refused before it commits, because VERSIONING.md promises breaking
changes only in a minor release; cut the minor instead. An SDK `(Breaking)`
entry (scope `sdk` or `typescript`) must link a migration guide section, in
`[Unreleased]` and at the cut. Before cutting one, run `sparkwing pipeline
upgrade` on a pipeline pinned to the previous SDK and confirm it makes the
change the guide describes. The
pipeline refuses `v1.0.0` and above. The dry run resolves the version, runs the
read-only cut checks (version freshness, Go directives, SDK pins, changelog
links, docs sidebar, published docs) and previews the changelog and guide roll; build, lint and the short
tests run only in the real cut.

The published-docs check refuses the cut while the first line of
`https://sparkwing.dev/llms.txt`, `# sparkwing -- vX.Y.Z (latest)`, names a
release older than `latest` in the signed `https://sparkwing.dev/versions.json`
index. The hosted release workflow writes that index only after a stable GitHub
release publishes, so a prerelease, an unpushed tag, or a tag whose hosted build
failed never demands docs; repair a failed hosted build with a later patch cut.
Publish the indexed release's product site docs (its `versions.json` entry
as `latest`, pushed to the site's main) before cutting the next one.

The check fails closed: a site outage, a docs title in any other format, or an
index whose signature does not verify blocks every cut, and the pipeline has no
override for it. To recover, restore the site or publish the missing docs, confirm
`curl -fsS https://sparkwing.dev/llms.txt | head -n 1` names the release that
`curl -fsS https://sparkwing.dev/versions.json | jq -r .latest` prints, then
rerun the cut.

Manual releases reuse a successful exact-source CI receipt when available. Missing,
pending, failed or unreadable receipts run the local build, lint and short suite
in the detached source worktree. Local release build, lint, frozen frontend
dependency installation, the TypeScript SDK suite, short tests and published-version
checks run in sequence to bound temporary build storage. The dependency installs
supply dashboard and SDK acceptance tests in the detached checkout.
Weekly releases continue to wait for green CI.

The schema changelog gate compares the neutral `internal/store/schema.sql`
baseline with the previous tag. SQL changes at an unchanged schema version
require a version bump and a matching `(Breaking)` changelog entry.

Release lint prints that it is waiting for the box-wide golangci-lint lock,
then uses `--allow-serial-runners` to wait and lint in sequence. The release
job allows one hour for serial preparation and checks. Its lint deadline and
budget warning remain 40 minutes: the ten-minute check allowance plus 30 minutes
for contention. The tier measures combined wall time, including waiting. A serial
cut took 38m31s before the missing SDK suite was prepared; that suite took another
102s in focused verification. Other lint callers keep their five-minute deadline.

golangci-lint keeps one cache per worktree under that worktree's git
directory (`<git-dir>/sparkwing-cache/golangci-lint`), outside the tree the
pipeline binary's source key walks and removed with the worktree. A cache
shared between trees replays findings under the other tree's paths, so it is
never shared.

The full gate's `lint` step allows ten minutes. It runs golangci-lint with
`--allow-serial-runners`, so a neighbor's lint queues it on golangci-lint's own
lock, `$TMPDIR/golangci-lint.lock`, and that wait counts against the ten
minutes.
Affected landing lint keeps all configured linters, uses bounded workers and
its private worktree cache, and passes `--allow-parallel-runners` to avoid
queueing behind a full scan.

When the lint step fails with "no result after" its deadline, look for the
neighboring run with `ps -eo pid,etime,args | grep '[g]olangci-lint'`. Rerun
the gate once it finishes, or stop it if it is stuck. Nothing was learned about
the tree. An exit 4 saying "Timeout exceeded" is golangci-lint's own
`run.timeout` instead: raise it in `.golangci.yml`.

Version freshness reads tracked module manifests and excludes `testdata` fixture
modules, matching the committed-module checks.

`push-tag` declares the `destructive` and `prod` risk labels, so only the
second command tags and pushes. The tag push starts the hosted release below.

### After a stable tag

Once the tag is available through the Go module proxy, update main's release
pins from an attached branch whose HEAD is past that tag:

```bash
SPARKWING_HOME="$(mktemp -d)" sparkwing run pre-release --start-at sparkwing-pin --stop-at sparkwing-pin
```

The step bumps `pkg/scaffold/version.go`, `.apidiff/pkg_scaffold.txt`,
`.sparkwing/go.mod`, and `testdata/k8s-e2e/repo/.sparkwing/go.mod` together,
tidies both modules, adds a changelog entry, syncs the docs mirror, and commits
the result. Review that commit and deliver it to main before the next cut.
Run the follow-up even if hosted artifact publication failed, because the
module proxy can already serve the immutable tag. A skipped follow-up surfaces
only at the next cut, whose `version-freshness` step refuses with "scaffold
fallback pin ... is behind latest release"; the fix then is this same command
and a land before the cut, which is how v0.69.0 found v0.68.7's missing pin.

After hosted publication, check a first user's upgrade path with the published
CLI. `sdk-upgrade-e2e` scaffolds a Go and a TypeScript pipeline in private
homes, pins the newest listed SDK older than the scaffold's, runs, runs
`sparkwing pipeline upgrade`, and runs again, against the published engines,
the signed index and the public module and npm registries. It needs network
access and stays off the land path. Without `--binary` it builds the
checkout's CLI, which still selects published engines, so engine-side fixes
show only once released. A Go upgrade is a verified no-op while one Go SDK is
listed. On a shared Linux host it took 35-42 seconds of test time with warm
caches.

```bash
SPARKWING_HOME="$(mktemp -d)" sparkwing run sdk-upgrade-e2e -- --binary "$(command -v sparkwing)"
```

The scaffold fallback refuses pins ahead of the local release tags unless
the exact tag exists on origin. This also verifies hosted release fixtures
whose selected release tag was removed locally.

The local release pipeline ends at the tag push because the new module must
exist before the pins can resolve it. Pre-release leaves the tag's own commit
unchanged because a release cannot depend on itself before publication.
Hosted checks use detached checkouts and report stale pins rather than
committing them. They cannot perform this follow-up for main.

### Release build and publication

Sparkwing owns artifact publication through its native `release` pipeline and
`.github/workflows/release.yaml`. Platform deployment belongs to the platform
repository and runs separately from publishing a Sparkwing tag.

The hosted release builds the dashboard once and shares it with six target
jobs. Each target compiles its supported commands in one Go cache; all 16
binary outputs remain available. Linux image packaging copies those same
executables through the Dockerfiles' `release` stage. The runner seed reads the
selected engine source's SDK requirement from go.mod, while its cache directory
keeps the engine version used by bootstrap. The default Dockerfile target still
builds from source.

One final job assembles image manifests, signs the assets, and publishes the
complete release. Publication is serialized across tags; builds remain
parallel. Only a stable version above every published stable release may
become GitHub Latest or move the image `latest` tags. Failed release-list
lookups stop publication. Manual rebuilds use the selected tag's source and
the current workflow's publication tools and image recipes.

#### Signed release index

Every public stable release must finish the `release-index` job in
`.github/workflows/release.yaml`. It reads all GitHub release pages after asset
publication, preserves the verified existing policy, and signs the exact client
format with `SPARKWING_UPDATE_SIGNING_KEY`. Only published stable `v0.x` releases
are added. Twelve-month support windows use GitHub's `published_at`, not the
recovery date. The job shares `release-publication` with asset publication, so a
late older release cannot replace a newer index with an old snapshot.

The publisher writes only `versions.json` and `versions.json.sig` in the
website bucket the `SPARKWING_WEBSITE_BUCKET` repository variable names, then
invalidates both paths on the CloudFront distribution `SPARKWING_WEBSITE_DISTRIBUTION`
names; the script refuses to start without either. It reads back the exact bytes from S3 and the public hostname.
Objects use a 60-second cache lifetime. Clients can encounter a mismatched pair
during the two uploads and fail verification until the pair is complete.
Existing clients retain their last verified index.

AWS authentication uses GitHub OIDC and repository variable
`SPARKWING_RELEASE_INDEX_ROLE_ARN`. Its role needs GetObject/PutObject on those
two keys, ListBucket restricted to prefix `versions.json` to establish signature
absence, and CreateInvalidation/GetInvalidation on that distribution. An empty
role variable fails the job. The website deploy must exclude both root objects
before it is enabled. Website documentation manifests and QA indices cannot
supply production signed policy.

After these changes reach the default branch, repair publication without
rebuilding assets or creating a release:

```bash
gh workflow run release.yaml --repo sparkwing-dev/sparkwing --ref main \
  -f tag=v0.68.2 -F index_only=true -F verify_only=false -F publish_images=false
```

For the first migration from the legacy unsigned website manifest only, also pass
`-F bootstrap_index=true`. This requires `index_only=true`, a missing signature,
and the legacy manifest shape. It never bypasses verification of an existing
signature or a signed-policy-shaped document. Later runs require the signed
pair. Tags without public releases are refused. Failed builds, verification-only
runs and prereleases never publish this stable index.

A run retains the generated pair as `signed-release-index` for seven days,
including when an upload fails. An interrupted upload may leave mismatched
objects and block another dispatch. Pause other index dispatches, download
that artifact, verify it with `go run ./cmd/verify-release --verify
--index-out <dir>`, upload both objects with the publisher's content types and
cache lifetime, invalidate both paths, then rerun the index-only dispatch. Never bootstrap over a damaged signed policy.

Focused evidence for index changes is `go test ./cmd/verify-release`,
`go test ./bin -run 'TestReleaseIndex|TestTypeScriptReleaseRequiresBothNativeBridges'`,
the workflow publication/queue/pin contracts in `.sparkwing/jobs`, actionlint,
shellcheck on the publisher, and `bin/sync-docs.sh --check`. Generator tests use
the real client verifier. Publisher tests replace AWS and HTTP with local tools
and prove failed preflight performs no upload or invalidation. These checks do
not establish production IAM access or hosted publication success.

The five-minute target includes runner queueing and publication. It has not
yet been measured for this workflow; local fixtures establish its contracts,
not hosted latency.

`@sparkwing/sdk` has its own version in `sdk/typescript/package.json`.
An engine release verifies the signed version index, then compares its recorded
TypeScript SDK source digest with the selected source. The digest includes tracked
SDK paths by default, including new inputs and npm's automatically packed files
such as LICENSE. Package manifest version fields are omitted. The native/package
workflow slice is also included, as is all of `test/nativehost/`: Windows publication executes
that package and binds its whole fixture tree into verification receipts.
Known unit tests and their helpers, examples, implementation notes, formatter
config and generated output are explicitly excluded. Changing this
input set changes existing source hashes, so adopting the rule needs a new SDK
version once. Recoveries of pre-change SDK entries also refuse the new hash;
the signed identities are not rewritten. An unchanged digest retains the existing SDK entry
and skips native assembly and npm publication. Changed source requires a new
SDK version. Bump both package manifests before cutting the engine release.
The land gate's `sdk-version` policy step reports a missing bump when the change
lands. The first independent SDK version is `0.72.0`.

The decision emits `sdk-entry-typescript.json` for the signed language-neutral policy.
The immutable engine release retains that entry, including the existing version
when source is unchanged. A changed SDK publishes before the engine release
becomes public. Native verification can still be requested without publication. Engine
prereleases require an SDK entry already present in the signed mapping. Cut a
stable engine release to publish changed SDK source before an engine prerelease
uses it, because prerelease engines do not enter the signed index. A recovery
of an older tag retains its exact signed SDK entry when the source digest
matches, even when a newer SDK has since published.

TypeScript SDK publication also requires native Windows x64 and arm64 jobs.
Each uses matching Node, Go and MSVC tools, builds its protocol bridge, runs
`TestNativeProtocol` and `TestNativeConformance` without skips, and uploads the
bridge with its verification receipt. Both jobs check out the resolved release
source. SDK text uses LF endings so source digests agree across operating systems.
Assembly checks both receipts against that source and the independent SDK version,
then packs the SDK. The publication job depends on assembly and publishes that
tarball without rebuilding it. Stable SDK releases use the npm `latest` tag after an independent npm version
check, regardless of the engine version. Prereleases use `next`.

`bin/publish-typescript.mjs` refuses major versions above 0, a packed `repository.url`
other than this repository (npm provenance rejects it), a packed package name
other than `@sparkwing/sdk`, or a packed version different from the selected SDK
version. There is no major-version override. An explicit `latest` requires a
stable version newer than every published stable `0.x` from
`npm view @sparkwing/sdk versions --json`; only an E404 means no versions.
Other lookup failures stop publication. Prereleases must use `next`. These checks
prevent a new publication from moving `latest` backwards.

npm publication runs before the immutable GitHub release becomes public. A retry
accepts an existing SDK version only when its registry integrity matches the packed
tarball. Different bytes, missing integrity, failed lookup and failed publication
stop the job. Package lifecycle scripts run during assembly, with publishing scripts
disabled when uploading the checked tarball. An identical retry skips publishing
and leaves all npm tags unchanged. Before each real upload, the publisher runs
`npm publish --dry-run` with the same arguments and logs the JSON file list and
packed size. A failed dry run stops the upload.

The release job publishes `@sparkwing/sdk` through
[npm Trusted Publishing](https://docs.npmjs.com/trusted-publishers) with OIDC and
`id-token: write` on a GitHub-hosted runner. It installs pinned npm 11.6.2 and
prints its version. Trusted publishing requires npm 11.5.1 or newer and Node
22.14.0 or newer. npm generates provenance automatically for the public package
and repository.

In the package's npm settings, configure a GitHub Actions trusted publisher with
organization `sparkwing-dev`, repository `sparkwing`, and workflow filename
`release.yaml`. Leave the environment name unset and allow direct `npm publish`.
Publication needs no Actions secret. Do not set a Node auth token, even to an
empty value.

Native workflow definitions and local graph checks do not establish Windows
runtime support. Both Windows jobs must execute successfully before that
evidence is claimed.

After publication-tool fixes reach `main`, rebuild an existing tag with a new
dispatch, rather than rerunning an old workflow attempt:

```bash
gh workflow run release.yaml --repo sparkwing-dev/sparkwing --ref main \
  -f tag=v0.68.1 -F verify_only=false -F publish_images=false
```

This builds `v0.68.1` source, uses `main`'s publication tools (including the npm
guards), and publishes release assets with image publication skipped. It does
not create or move the Git tag. The trusted publisher must already be configured.

For an accidental npm publication, first move `latest` to a verified good `0.x`
version and check the result:

```bash
npm dist-tag add "@sparkwing/sdk@<version>" latest
npm view @sparkwing/sdk dist-tags --json
```

Within 72 hours, remove the bad version if the
[npm unpublish policy](https://docs.npmjs.com/policies/unpublish/) permits it
(including the dependent-package restriction):

```bash
npm unpublish "@sparkwing/sdk@<bad-version>"
```

After that window, or if npm refuses removal, warn installers instead:

```bash
npm deprecate "@sparkwing/sdk@<bad-version>" "Do not use; install @sparkwing/sdk@<version>."
```

Recheck dist-tags after recovery. The package/version pair can never be reused,
even after unpublishing; publish corrected bytes under a fresh `0.x` version.

For Windows verification without publication, dispatch `release.yaml` on the
branch to check with `verify_only=true`, `publish_images=false`, and a release
version in `tag` (for example, `v0.68.0`). This mode checks out the dispatched
commit, runs both Windows bridge jobs, and uploads their receipts. Dashboard,
release binary builds, package assembly, and publication are skipped. The tag
input supplies the SDK version only and no tag is created or moved. Captured
Go test output is printed when bridge verification fails.

### QA pre-releases

Cloud QA installs unreleased engines through the real signed-release path from
`https://qa-releases.sparkwing.dev`, a private S3 bucket behind CloudFront. Only binaries built with the `qatrust` Go tag trust it: the tag adds
the QA public key to `internal/releaseauth` and replaces the version index and
release download origin in `internal/engine`. A QA pre-release version such as
`v0.68.0-qa.1` enforces the QA index instead of passing as a development build.
`sparkwing update` refuses to run in a qatrust build; install the next QA
pre-release instead. `bin/build-release-target.sh` refuses a binary whose build
tags disagree with its version: `qatrust` exactly when the tag ends in `-qa.N`.
pre-push vets touched packages in both flavours.

`bin/qa-release.sh <repo> <commit> <vX.Y.Z-qa.N>` clones the commit, builds the
dashboard and the full release matrix with the tag, signs the assets with the QA
seed from the SSM parameter `SPARKWING_QA_SIGNING_KEY_PARAMETER` names, appends the version to
the signed QA `versions.json`, and uploads both. It also publishes the Go SDK at
that version in GOPROXY layout under `/goproxy`. The index and the module `list`
are written only if unchanged since they were read, so concurrent runs fail
rather than drop a version. A published QA version is immutable; use the next `-qa.N`.
The QA bucket is `SPARKWING_QA_BUCKET`. Keep both settings in
`~/.config/sparkwing-pipelines/env` and export them, with `AWS_PROFILE` for the
account that holds them, before running the script; it refuses to start without
either.

A qatrust engine compiling a pipeline pinned to a `-qa.N` SDK puts the QA proxy
ahead of the effective GOPROXY (as `go env` reports it, so `go env -w` and
`GOPROXY=off` hold) and adds the sparkwing module to the checksum-database
exemption, seeded from GOPRIVATE when GONOSUMDB is unset. Other pins are untouched.
A qatrust CLI scaffolds Go pipelines with its QA SDK pin and applies those
settings during `go mod tidy`, so `pipeline new` resolves the generated pin
without extra shell configuration. Production CLIs use public SDK pins.
A source build without a release stamp uses the published scaffold fallback.
A QA pipeline's `go.sum` can also be generated by hand
(`GOPROXY='https://qa-releases.sparkwing.dev/goproxy|https://proxy.golang.org,direct'
GONOSUMDB=github.com/sparkwing-dev/sparkwing go mod tidy`). The TypeScript SDK has
no QA registry package. For Linux/macOS dogfooding, `npm run pack:local --
--version <sdk-version> --destination <directory>` in `sdk/typescript` builds
a private local tarball without Windows bridges. npm refuses publication of it.
Published packages still require both verified native Windows bridges.

Accepted QA-only risks:

- A qatrust build also trusts the production key. It reads only the QA origin, but
  a production index cached in the same engine cache makes it fail closed; use a
  separate HOME for QA.
- The QA proxy sees every module path a QA-pinned pipeline fetches, including
  private ones, because `|` must fall through the 403s the bucket returns for
  modules it does not hold.
- The QA Go module zip is unsigned; `go.sum` on first fetch is its integrity.
- The signing seed and `AWS_PROFILE` are in the environment of tools from the
  commit being released, including `pnpm install` in `build-web.sh`; publish only
  trusted commits.
- Clients that read a new `versions.json` before its signature lands fall back to
  their cached index until it does.

QA images: `bin/qa-release.sh` also publishes the controller and runner images, through the
`publish-images` pipeline run on the CLI it just built. The pipeline appends each signed linux
binary, checked against SHA256SUMS, as one layer onto the matching platform of a multi-arch
runtime base, tags each architecture `vX.Y.Z-qa.N-amd64` and `vX.Y.Z-qa.N-arm64`, joins them
under one index tagged `vX.Y.Z-qa.N` (the tag to deploy), and reads every platform back to
check its binary. It is registry operations only, with no Docker daemon or emulation. Name the
bases by digest in `SPARKWING_CONTROLLER_BASE` and `SPARKWING_RUNNER_BASE`; the registry is
`QA_IMAGE_REGISTRY`, by default the AWS profile account's ECR in us-west-2. A base is each
Dockerfile's `runtime` stage, rebuilt only when that stage or its pinned images change, with
`sparkwing run build-images -- --runtime-base --registry <ecr> --push --tag <tag>` on a builder
that runs linux/arm64 natively or under emulation. The hosted release still builds its ghcr.io
images with buildx under QEMU and joins both architectures under one index per tag.

### Deploy toolchain

The `deploy` pipeline ships Sparkwing Cloud from runners its operators own.
This section is the toolchain those runners need.

Deploy CLI homes share only the runner's existing content-addressed pipeline
binary cache. Each invocation keeps its profiles, credentials and store in its
private job home. The cache key covers pipeline source and local dependencies,
toolchain identity, GOOS/GOARCH and build flags. Changed inputs rebuild, and
installation verifies the cached bytes before executing a private copy.
If linking the cache fails, submission uses its private cache. Job cleanup
removes the link, while existing cache limits and pruning own retained entries.

Deploys run only on runners carrying the deploy runner label declared in
`.sparkwing/sparkwing.yaml`. After installing the root-owned toolchain below,
enroll a runner with that label using `sparkwing runners add --label` and the
account session described in the [runners add docs](docs/cli-runners.md#sparkwing-runners-add).
Local `--dry-run` previews need no runner label.

A real deploy runs `aws`, `kubectl`, `helm`, `git`, `ssh`, `go`, `bash`, `node`, `pnpm` and `bitwing` only from
`/opt/sparkwing-deploy/bin`, and gives every child process `PATH=/opt/sparkwing-deploy/bin:/usr/bin:/bin`.
The directory must be owned by root and not writable by the runner's user, so another job on the same
runner cannot plant a binary that a later deploy runs with production credentials. When the directory is
missing, every deploy job refuses before it fetches a credential; there is no fallback to the runner's
`PATH`. A `--dry-run` preview holds no credentials and still resolves tools from `PATH`. Install the tools
as root, at the versions the deploy machines already use:

```bash
sudo install -d -o root -g root -m 0755 /opt/sparkwing-deploy /opt/sparkwing-deploy/bin
cd "$(mktemp -d)"
# Go, matching go.mod's toolchain line
curl -fsSLO https://go.dev/dl/go1.27.4.linux-amd64.tar.gz
curl -fsSL "https://go.dev/dl/?mode=json&include=all" | grep -A3 '"go1.27.4.linux-amd64.tar.gz"' | grep sha256  # compare
sha256sum go1.27.4.linux-amd64.tar.gz
sudo rm -rf /opt/sparkwing-deploy/go  # an older Go's files would otherwise survive the extract
sudo tar -C /opt/sparkwing-deploy -xzf go1.27.4.linux-amd64.tar.gz
sudo ln -sf /opt/sparkwing-deploy/go/bin/go /opt/sparkwing-deploy/bin/go
# kubectl
curl -fsSLO https://dl.k8s.io/release/v1.37.0/bin/linux/amd64/kubectl
echo "$(curl -fsSL https://dl.k8s.io/release/v1.37.0/bin/linux/amd64/kubectl.sha256)  kubectl" | sha256sum -c
sudo install -o root -g root -m 0755 kubectl /opt/sparkwing-deploy/bin/kubectl
# helm
curl -fsSLO https://get.helm.sh/helm-v4.2.4-linux-amd64.tar.gz
curl -fsSL https://get.helm.sh/helm-v4.2.4-linux-amd64.tar.gz.sha256sum | sha256sum -c
tar -xzf helm-v4.2.4-linux-amd64.tar.gz
sudo install -o root -g root -m 0755 linux-amd64/helm /opt/sparkwing-deploy/bin/helm
# AWS CLI v2
curl -fsSL https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip -o awscliv2.zip
unzip -q awscliv2.zip
sudo ./aws/install -i /opt/sparkwing-deploy/aws-cli -b /opt/sparkwing-deploy/bin
# Node and pnpm for bin/build-web.sh
curl -fsSLO https://nodejs.org/dist/v22.23.1/node-v22.23.1-linux-x64.tar.xz
curl -fsSL https://nodejs.org/dist/v22.23.1/SHASUMS256.txt | grep ' node-v22.23.1-linux-x64.tar.xz$' | sha256sum -c
sudo tar -C /opt/sparkwing-deploy -xJf node-v22.23.1-linux-x64.tar.xz
sudo mv /opt/sparkwing-deploy/node-v22.23.1-linux-x64 /opt/sparkwing-deploy/node
sudo env PATH=/opt/sparkwing-deploy/node/bin:/usr/bin:/bin npm install -g --prefix /opt/sparkwing-deploy/node pnpm@12.5.1
sudo ln -sf /opt/sparkwing-deploy/node/bin/node /opt/sparkwing-deploy/bin/node
sudo ln -sf /opt/sparkwing-deploy/node/bin/pnpm /opt/sparkwing-deploy/bin/pnpm
# Bitwing reporting uses the normal installed binary copied into the trusted toolchain
sudo install -o root -g root -m 0755 "$(command -v bitwing)" /opt/sparkwing-deploy/bin/bitwing
# git, ssh and bash from the root-owned system packages
for t in git ssh bash; do sudo ln -sf "/usr/bin/$t" "/opt/sparkwing-deploy/bin/$t"; done
sudo chown -R root:root /opt/sparkwing-deploy && sudo chmod -R go-w /opt/sparkwing-deploy
ls -l /opt/sparkwing-deploy/bin && /opt/sparkwing-deploy/bin/go version
```

### Deploy configuration

The public pipeline holds the deploy's shape and validation, not its account.
The account-specific values live in a private `sparkwing-deploy/deploy.yaml` in
a configuration repository the deploy runner names. Every key is required, an
unknown key or a second YAML document fails the load, repositories are GitHub SSH
URLs (`git@github.com:owner/repo.git`), `registry` must be the ECR registry of the
deploy role's account in its region, and `cluster` and every environment name are
DNS-1123 labels:

```yaml
registry: <account>.dkr.ecr.<region>.amazonaws.com
cluster: <EKS cluster name>
bases:                 # runtime bases, by digest, inside registry
  controller: <registry>/sparkwing-controller@sha256:<digest>
  runner: <registry>/sparkwing-runner@sha256:<digest>
repositories:
  gitops: <SSH URL of the GitOps repository the deploy pins>
  reference: <SSH URL of the reference pipeline repository>
parameters:            # SSM parameter paths, not values
  gitops_key: /<path>
  reference_key: /<path>
  staging_gate_token: /<path>
  smoke_submit_token: /<path>
environments:
  staging:             # and production, with the same keys
    gitops_dir: <directory in the GitOps repository>
    helm_release: <release>
    argo_app: <Argo CD application>
    controller_deployment: <Deployment name>
    launcher_deployment: <Deployment name>
    prepull_tag: <runner tag Cloud job nodes pull at boot>
```

The deploy runner's machine settings bootstrap that read. They live in
`~/.config/sparkwing-pipelines/env` as `NAME=value` lines, and a variable in
the ambient environment wins over the file:

| Name | Purpose |
| --- | --- |
| `SPARKWING_PIPELINES_DEPLOY_ROLE` | IAM role ARN the run's OIDC token assumes |
| `SPARKWING_PIPELINES_DEPLOY_REGION` | AWS region for that role and SSM |
| `SPARKWING_PIPELINES_DEPLOY_CONFIG_REPO` | SSH URL of the configuration repository |
| `SPARKWING_PIPELINES_DEPLOY_CONFIG_PARAMETER` | SSM parameter holding that repository's read-only deploy key |

Each deploy job assumes the role, reads the key from SSM and shallow-clones the
configuration repository once before it uses any account value, then deletes the
clone and the key; superseded commits still stop before that. Before its first
GitOps push to an environment, the deploy checks that the environment's Argo CD
application exists. A job missing a setting or a key names every
missing one. `--controller-base` and `--runner-base` override the settings'
bases. A `--dry-run` holds no credentials, so it reads a local copy of
`deploy.yaml` from `--deploy-config` and still needs the role and region
settings to print the actions.

The same file's `SPARKWING_PIPELINES_OWNED_SDK_OWNERS`, a comma-separated list,
adds GitHub owners whose repositories the `owned-sdk-main` pipeline updates beyond
`sparkwing-dev`.

### Check tiers

Current SDK verification runs the Go and TypeScript unit suites and the host
execution scenarios in `TestHostExecMatchesSDKExecution`. Helpers run through the
bound host service routes. Historical SDK acquisition and compatibility matrices
are removed. Old SDK pins require an upgrade to the current contract.

Canonical hosted jobs retain a `canonical-timings` JSON artifact for 30 days
on success and failure. It contains run and step timestamps, outcomes, available
node CPU/memory measurements, and source/workflow identity. It excludes raw
logs, invocation arguments, errors and local paths. Missing measurements remain
null. Pipeline compilation, hosted setup, cache temperature and exact admission
wait are not established by this artifact. A missing handle produces an explicit
unavailable record. Export/upload failures warn without changing the check verdict;
an absent artifact is missing evidence, not a successful export.

Failed canonical hosted jobs retain the full paged `runs logs` output in a
`canonical-run-log-<lane>-<workflow-run>-<attempt>` artifact for 14 days. The
console prints its last 500 lines. A failed log read retains partial output
and its error in the artifact. Missing handles and failed uploads warn without
changing the gate verdict. The artifact contains run output, not the runner's
Sparkwing home, database or controller key.

Pre-release records each check as a sequential WorkStep. A failed check does
not suppress later checks; the parent reports the first failure, while the run
logs and step states retain every individual failure. `--platform-cells` adds
the `macos` and `windows` cells as dispatched jobs requiring `os=darwin` and
`os=windows`; they run the `macos-verify` and `windows-verify` checks. A local
run cannot place them and hosted CI does not pass the flag. Trigger the run on
the team controller; each cell waits visibly for a team runner reporting its
operating system:

```bash
sparkwing pipeline trigger pre-release --profile prod -- --platform-cells
```

Enroll each machine once as a shared team runner scoped to this repository,
piping a dedicated administrator session to stdin, then keep its runner service
running. The service reports `os=darwin` or `os=windows` itself:

```bash
sparkwing runners add --runner mac-runner-1 --profile prod --repos github.com/sparkwing-dev/sparkwing --max-concurrent 1 --session-stdin
sparkwing runners add --runner windows-runner-1 --profile prod --repos github.com/sparkwing-dev/sparkwing --max-concurrent 1 --session-stdin
```

The canonical timing artifact retains those step outcomes and durations but excludes their error text.

Pinned actionlint v1.7.12 does not recognize GitHub's `concurrency.queue`.
Its config ignores only that exact diagnostic in `release.yaml`. The workflow
contract tests require both publication jobs' shared group, `queue: max`, and
`cancel-in-progress: false`, and reject uncovered queue use elsewhere in that
file. Other syntax and workflow checks remain active.

- **The three tiers:** `pre-commit` judges the staged change against this
  repo's source policy and nothing else; the git pre-commit hook runs it.
  `pre-push` begins with the fast phase the git pre-push hook runs, and it repeats
  nothing the commit tier already ran: the changelog, OpenAPI and API-snapshot
  gates, and `go build`, `go vet` and the fast linter subset over the packages
  the push touches, with no package-count cutoff. The linter includes the
  depguard dependency rules. A
  runner reserves the combined CPU demand of those three concurrent compiler
  tasks, up to 9 cores. Smaller runners divide their available cores across
  the tasks. After that phase passes, a separate `affected-tests` job runs
  the shared affected short and critical Go tests for main updates (and explicit
  manual runs). Branch pushes keep the fast checks alone. The
  [local affected-test policy](#local-affected-go-tests) defines its scheduling
  reservation, explicit command width and owned Go flags.
  The tiers shift left, so a
  push whose commits skipped the commit hook is judged by `gate` rather than a second
  time here. Rebase replay does not fire pre-commit, and automatic merges use
  pre-merge-commit; neither hook tier proves source policy over those resulting
  trees. Run `gate` when that evidence is required. Everything
  else is `gate` (the broad check) and `pre-release` (the release boundary),
  which `sparkwing run <name>` runs on demand; no git hook fires either. Hosted
  CI verifies, it does not gate: every pull request and push to
  main runs `gate` and `race-touched` over the change since the push's previous
  commit (or the pull request base), and the full suite, `pre-release`, the
  store race lanes and Postgres conformance run nightly and on dispatch.
  Native runner composition tests in
  `internal/cluster/production_runtime_integration_test.go` are ordinary tests,
  without the `rewrite_integration` build tag. Affected tests cover them when
  cluster or its dependencies change; full, nightly and release suites always
  include them, and touched-package race runs exercise them too. They use
  private fixture homes, fake HTTP transports and a private in-process daemon,
  not Docker containers or managed user services. Other packages' tagged
  `rewrite_integration` suites remain outside this coverage claim.
  The CLI’s `TestRunAnywhereRunnerHostsPlanEvaluationAndJobs` is also an
  ordinary Unix test. Full Gate and nightly/dispatch full suites run its
  personal-runner plan, evaluation, generated jobs and sealed-origin assertions
  without a build tag. Short-mode affected and release tests visibly skip this
  SDK-build case; it uses the unmanaged test backend and manager PATH guards,
  so no native service-qualification window is needed. Its fixture selects a
  private key file and a live controller clock for the real CLI’s signed origin
  proof. Other CLI integration cases retain their existing tags.
  On a shared Linux host its repaired test body took 37.05 seconds, including
  SDK fixture preparation and actual CLI/runner subprocess execution. This is
  an observed full-test cost, not a new deadline or an end-to-end gate budget.
  On a shared Linux host, these cases took 4.81 seconds of an 11.56-second
  cluster suite; a warm-cache command took 15.02 seconds wall time. Cold
  compilation of the baseline cluster suite took 202.90 seconds including
  10.34 seconds of test execution. These are observations, not new budgets.
  The full acceptance suite includes `TestFeatureE2E`, which exercises placement
  deadlines, failed-only retries with outputs and artifacts, and child-run
  cancellation against isolated local controller processes. Run just these with
  `sparkwing run feature-e2e --route local`. Its `--scenario` and `--repeat`
  arguments select one scenario or repeat fresh environments. The
  [acceptance runbook](tests/acceptance/README.md#feature-scenarios-against-a-local-controller)
  names the real-time budgets, trust setting and runner-actor boundary.
  `sparkwing pipeline hooks install` arms the two hooks in a checkout and
  `sparkwing pipeline hooks status` checks the active hook directory and compares
  each hook's presence, content, format version, and executability. Blocking
  gates are required; notifiers are optional. Status reports that verdict for
  the current repository. Installation writes hooks without running pipelines
  and refuses a conflicting `core.hooksPath`; compose hooks manually or clear
  the reported override. Reinstall stale or missing hooks; use a throwaway
  commit when a manual execution check is needed.
  A hook script names its pipeline, so a checkout whose hooks
  were installed when the pre-push hook ran `gate` keeps running `gate` until
  `sparkwing pipeline hooks install` rewrites them.
- **Budget warnings and execution deadlines:** `pre-commit` is the source
  policy at 3 seconds, the fast phase of `pre-push` has a one-minute budget,
  and the release cut, the `release-cut-checks` job the `release` pipeline runs
  before it tags, 40 minutes including 30 minutes of lint-lock allowance
  and ten minutes for build, full lint, the short test suite,
  published-version, Go-directive and SDK-pin checks, and changelog-link and docs-sidebar checks.
  The read-only `typescript-release` check verifies the published policy index's
  signature and runs the hosted release's SDK decision against the selected source.
  It rejects a changed SDK digest under an existing version before creating a tag,
  including in dry runs and cuts that reuse exact-source CI.
  Build, lint, dependency preparation, the SDK suite and short tests run in sequence.
  The job allows one hour to finish those serial checks while retaining the
  40-minute budget warning.
  The broad short-test phase reuses the affected-test command owner and its
  25-minute timeout for each package binary, including an unsharded store.
  It does not fall back to Go's ten-minute default under load. Module ordering,
  command width and the one-hour job deadline are unchanged.
  Each of those budgeted phases times its own steps and warns when its slowest step's
  own run time overruns the class, naming the step and its cost, so a class
  cannot regrow unnoticed. A tier whose checks all passed still passes: when
  the budget failed green tiers, authors under machine load committed with
  `--no-verify`, which skips every check, and lands then found the policy
  failures twenty minutes later.
  A tier whose scope holds no Go file still runs and passes every step, so it warns and
  names the scope it read: a push judged against an empty
  `origin/main..HEAD` range reports success over nothing, and uncommitted
  work is outside what `pre-push` reads. The
  budget judges the slowest step's own run time, not the admission wait, the
  time a step waits for a core or a step slot, or the 2.5 s the pipeline
  binary takes to recompile after a Go change. The verdict line reports both
  the slowest step's run time and the span. Pre-push's
  affected Go tests have their own 45-minute job deadline and are outside
  the fast phase's warning budget. The
  formatters are the per-file cost in the commit tier, and `goimports` inside
  `golangci-lint fmt` is all of it: over a hundred Go files `gofumpt` measured
  0.38 s, `goimports` 10.9 s and `gofmt` 0.09 s, so a Go file costs about
  0.11 s. `gate`
  and `pre-release` are the heavier classes: they carry no performance budget
  and run asynchronously, on demand and in hosted CI. The broad gate declares
  separate 70-minute execution deadlines for broad checks and touched-package
  race tests. Each gets 71 minutes with the dispatcher's drain margin, inside
  its own 80-minute hosted job. Hosted CI selects them with `sparkwing run gate
  --only gate -- --full` and `sparkwing run gate --only race-touched`. Each job
  owns a separate four-core runner, so race tests share no cores with the full
  Go or PostgreSQL suite. `--only` keeps a job's dependencies, so each lane
  runs the `policy` job first; on a fresh runner its first compile of the
  checkers and the CLI is the cost. Local Gate and ordinary Land make both
  jobs eligible once policy passes. Each reserves cores based on its Go
  test or shard width. They start together only when both reservations
  fit admission headroom. On a shared 16-core host, each asks for seven
  cores, so the two run serially under Auto admission's 20% headroom.
  Go, lint and frontend steps can overlap within `gate`, so the reservation
  describes Go parallelism and measured CPU can be higher. The
  `preconditions`, `review`, `record-tree` and `land` jobs declare a quarter
  core (`Cores(0.25)`), so they can start beside running gates.
  The `cli-docs` policy step compiles the CLI's registry, help metadata and
  Markdown renderer through `reference_generate.go`, without linking the
  execution runtime or embedding the docs it checks. `gen-cli-docs.sh --check`
  still regenerates every reference page and rejects changed, missing or stale
  generated pages. `TestCommandReferenceMatchesRegistry` also compares the
  committed pages with the renderer compiled into the full CLI.
  Both gate modes and pre-push run `build-published-sdk` after their workspace
  build, using the same compiler allowance. Affected Gate, pre-push and merge
  rechecks compile the host target plus linux/arm64, the deploy-image target.
  With at least two compiler cores, those builds overlap and split the existing
  allowance; otherwise they run sequentially. A linux/arm64 host builds once.
  Full Gate, including the nightly full tier, compiles all six release targets:
  Linux, macOS and Windows on amd64 and arm64. The remaining targets are caught
  at the full tier and at release, not per land. The CLI and runner build on
  every target; the controller builds on Linux and macOS.
  Each target uses `CGO_ENABLED=0`, `GOWORK=off`, `GOENV=off`, empty `GOFLAGS`
  and `-mod=readonly -trimpath`. The selected SDK must have a published version
  and no replacement. This also bypasses vendor copies and alternate modfiles.
  Go input changes, including SDK-only changes and package-owned files of any
  extension, select the check. Package-owned Markdown is included conservatively
  because it can be embedded, including deleted inputs. Unowned documentation
  still skips the check; full gate always runs it. Merge rechecks retain it over the
  combined branch and merge delta. Publish a standalone Go SDK API change first,
  wait for its module version to resolve, then bump the root `go.mod` requirement
  before the root uses that API. Workspace conformance remains a separate check.
  The fast
  phase retains its one-minute warning. The build follows the workspace build
  rather than increasing its compiler reservation.
  Without `--full`, vet and build cover the affected dependency closure. Its test step
  runs `go test -short` on changed packages
  and their reverse dependency closure, including production, internal-test
  and external-test imports. The critical set runs separately without
  `-short`, including critical tests in affected packages. A `go.mod` or
  `go.sum` change runs its whole module's fast class and critical set.
  A store schema change selects `internal/store` and its importers.
  Long integration bodies guarded by `testing.Short()` and the PostgreSQL
  suite run after deploy on our hardware with
  `sparkwing run gate --route local -- --full`, and remain in full nightly
  and pre-release checks. The deploy scan trigger must invoke that command
  outside the land and deploy critical paths. A
  file outside every package runs the packages whose tests read it, from the
  `nonPackageReaders` table in `.sparkwing/jobs/affected_tests.go` (`.github/`
  and `bin/` run `.sparkwing/jobs`, `docs/` runs `internal/doccheck` and
  `cmd/sparkwing`, and so on); `CHANGELOG.md` and generated `pkg/docs/` Markdown and schema mirrors stay with the docs-mirror
  and changelog steps. File readers select their own tests without charging
  importers whose code did not change. Changes under `pkg/wingwire/`, `internal/controllerwire/`,
  `internal/apispec/` or `api/` also run `.sparkwing/jobs`, including
  `TestRepositoryUnreleasedSectionDeclaresEveryWireCut`, because the release
  wire-cut check reads the protocol and API surfaces outside its package.
  Lint starts independently of the test step because it has no test-result
  dependency. Affected lint runs the full configured linter set with bounded
  workers and its existing checkout cache, without waiting on the global
  serial-runner lock. Full lint still waits for that lock. The real lint
  integration fixture stays in the full test class. Fixture-module dependency
  changes do not widen product lint. Its lint step lints only the packages holding changed Go
  files (every package when `go.mod`, `go.sum` or `.golangci.yml` changed),
  since `--new-from-rev` drops findings outside the change anyway. With
  `--full`, race tests cover every package but `internal/store` and the
  PostgreSQL suite always runs, so a full pass does not depend on the
  producer's `origin/main`. The broad job's 70-minute deadline already allows
  the full Go suite and the 30-minute PostgreSQL limit; hosted CI runs the race
  job without `--full` for pushes and pull requests, so it runs the fast race
  class on touched packages. Local post-deploy scans pass `--full` to retain long race tests across
  every package except the deferred store.
  Affected race runs use the stable private test home and native Go test cache,
  without `-count=1`. Each stable suite home is locked through setup, execution
  and cleanup, so overlapping runs of the same step in one checkout wait for
  each other. Full race runs keep the uncached shard path. The Go build
  cache already survives runs, and hosted jobs restore and save it through
  their existing cache steps. No successful policy or review check is skipped
  by a package cache hit. After
  both jobs run and pass on a clean checkout, `record-tree` outputs HEAD's tree, the
  `.sparkwing` definition fingerprint, the mode and the engine version. Each
  gate job reads that output through a `sw.FromProvenRunAtCommit()` reference
  and skips itself, naming the runner that proved it, when the controller finds
  a full-mode pass at the same commit from a run proven for this repository
  (from GitHub, or fetched by the controller) that selected every job without
  dry run, sealed no environment variables and ran every job on a trusted host
  (a fresh host, or a runner scoped to repositories with `--repos`), and that pass names the same tree, fingerprint and engine.
  `record-tree` reads both jobs' in-run outputs and records nothing when
  either skipped, and the lookup passes over a run whose `record-tree` did not
  succeed, so a run that skipped everything neither records a pass of its own
  nor hides the older pass it skipped on. A plain
  local run proves no repository, so it never lets another run skip;
  `sparkwing pipeline trigger gate --profile P --trusted -- --full` runs the
  same commit on this machine's repository-scoped runner instead, which can. The
  existing required `Canonical / gate` check succeeds only after the broad,
  race, pre-release and three `race-store-k-of-3` matrix jobs all succeed. A
  skipped, cancelled or failed matrix cannot pass that check. Pre-release
  declares a 120-minute execution deadline inside a 130-minute hosted job,
  leaving ten minutes beyond the node deadline for setup and cleanup; the
  race-store lanes run the same pipeline and keep the same deadlines.
  Explicit `source_ref` or `release_tag` overrides run that source's complete
  gate graph in one hosted job, because older refs do not declare a separate
  race job. Normal pull requests and pushes use the split jobs.
  These are liveness boundaries, not claims that either check normally takes
  that long. A failed hosted canonical run gets
  two minutes to print its stored status and the last 500 log lines from the
  run handle. On exactly four logical CPUs, the ordinary race command can run
  two package binaries while each keeps
  `GOMAXPROCS=1`. When touched and the host has capacity for two package
  processes, `internal/controller`, `internal/nodehost`, `cmd/sparkwing`,
  `internal/wingd/localcontroller` and `tests/acceptance` run after the
  module's ordinary packages. Measured long poles get a shard of their own,
  queued ahead of every round-robin shard: `TestStorage` in acceptance, and
  `TestLogTreeTraversesEveryTenantDescendantProperty` and
  `TestRetryHistoryTraversesLongChainsAndCycles` in controller. Controller
  splits its remaining tests into six round-robin shards and acceptance into
  three. Nodehost and the CLI use two. Localcontroller keeps its full suite in
  one queued process because it fits the package budget. A lead test that is
  no longer listed fails the step, so a rename cannot silently drop it from
  the plan. The runner compiles and lists each package once, then queues every
  selected package's shards through the host's package-process allowance:
  four workers immediately on the dedicated hosted four-core runner, two
  workers on a shared four-core host, and the existing Go step parallelism on
  larger hosts. A free worker can start the next package while
  another finishes a long controller shard.
  Each shard keeps `GOMAXPROCS=1` and runs its assigned tests once, including
  the slow class. Listing, coverage checks, failure cancellation and output
  collection use the same runner as the store's release shards. The runner
  logs the sorted list's count and SHA-256, plus each shard's count and hash,
  and rejects duplicate, missing or multiply assigned names.
  `race-touched` has its own 70-minute execution budget. Ordinary
  package processes and shard processes have an explicit 52m30s timeout,
  derived as three quarters of that budget. This raises the ordinary package
  timeout from 45 minutes and replaces nodehost's ten-minute shard timeout.
  The race node deadline bounds its builds, listing and every selected package.
  Broad checks have their own deadline. Parallel tests within a runtime
  share the same CPU bound. Hosts with capacity for one package retain the
  ordinary race command. On a four-core single-tenant host, the ordinary
  command uses one-thread, two-package bounds on its separate runner, and the
  PostgreSQL suite runs four one-thread shard processes. Fitting each hosted deadline and improving cold-start
  latency still require measured evidence. The full Go suite uses a 25-minute per-package timeout.
  Lint and the PostgreSQL suite, conditional outside `--full`, wait for the full Go suite.
  Touched-package race tests run as an independent job on every host. Existing
  per-command Go parallelism stays unchanged.
  `internal/store` is not among the gate's race targets. Its full race suite
  exceeded 80 minutes in every one of four shards on the hosted runner, so
  adding it to the gate would restore a check that cannot pass.
  `pre-release` runs it instead, as `race-store`, and no release ships past it.
  That step compiles one race-enabled `internal/store` test binary, lists its top-level
  Test, Example and Fuzz names, sorts them and assigns each exactly once across
  four processes per part, longest first onto the least loaded shard using the
  measured costs in `.sparkwing/jobs/store_race_costs.txt`. A name the table
  does not know costs the mean of the names it does, and a stale line is
  ignored. `--race-store-part k/n` (pipeline input) computes the same
  assignment over `4n` shards on every runner and runs only part k's four;
  the default runs all four shards of one part. On normal pull requests and
  pushes, hosted CI runs `--start-at race-store --stop-at race-store --
  --race-store-part k/3` in three `race-store-k-of-3` lanes, each on its own
  four-core runner, and the pre-release lane passes `--race-store-part none`,
  which runs no shard. `TestCanonicalStoreRacePartsCoverEveryPartOnce` fails
  unless the lanes cover parts 1 to n exactly once whenever pre-release passes
  `none`. Source and release-tag overrides keep the whole store race inside
  their one pre-release job. The store race runs without PostgreSQL: a job's
  environment never inherits `SPARKWING_TEST_*` names, so the PostgreSQL store
  tests skip under race and run unraced in `store-postgres` against its own
  embedded server. Every shard runs with `GOMAXPROCS=1`; the separate
  `internal/store/internal/storetest` package runs once, in part 1. The step stops sibling
  processes on a failure and retains each shard's output in the run log. It
  gives the suite a fresh `SPARKWING_HOME`, clears inherited runner credentials
  and service bindings, and passes `GOWORK=off` into Go commands in worktrees.
  The shards run
  the short class (`-test.short`); the tests that guard themselves as slow keep
  running without race in `gate` and on PostgreSQL in `store-postgres`. Later checks remain under the 120-minute node deadline and a
  130-minute hosted job deadline.
  The hosted broad and touched-package race
  jobs each keep an 80-minute deadline.
  A Go step's own parallelism depends on who else holds the
  box. A shared host bounds each step to `(cpus-1)/2`, so one gate cannot
  saturate a machine another gate is running on. `GITHUB_ACTIONS=true` selects
  dedicated sizing of `cpus/2`. The Cloud snapshot gate selects the same sizing
  with `--base`, with the CPU count capped at four. Sparkwing sets `CI=true`
  for local jobs too. At four CPUs a shared Go step gets one worker and a
  dedicated step gets two. Race commands keep `GOMAXPROCS=1` on either
  four-core host.
  That publication path
  stops at the parent runner rather than
  entering node processes, so nested Sparkwing commands choose their own
  output. `sparkwing runs stats
  --pipeline pre-commit --since 7d` reports what a class has cost over the
  week, and `sparkwing runs status --run <id> --view timeline --steps` breaks one run into
  its steps; the runs store is shared across repositories, so filter the runs
  by repo before reading a per-pipeline figure as this one's.
- **The short test class:** the release cut runs `go test -short`. Use
  200 ms as guidance for expensive test-body work beyond current-schema
  fixture setup. Such work should guard itself with
  `if testing.Short() { t.Skip("slow: ...") }` naming what costs the time.
  Real-store fixture setup remains in the short suite. The short class retains a
  real-store risk-admission test that requires
  an unapproved submission to leave the queue empty. First-run compilation,
  ref selection and pinned-binary risk fixtures remain in the full class.
  `gate -- --full` runs the suite without `-short`, which is where every
  guarded test is still judged. Affected landing gates use the short class.
  Tests that cost 1 s or more split by what they guard. A `testing/quick` property keeps running under
  `-short`, checking three fixed-seed cases through `internal/quicktest`; a
  test whose name or file names an authorization, fence, expiry, masking,
  tenant or secret boundary keeps running whole; the rest skip as slow.
- **Native recovery simulation:** `TestStartGateSeededRecovery` runs seed 1
  with and without compute under `-short`; the full class runs all eight seeds
  in both modes. Each case still covers lost plan capacity, an already released
  job, fresh work, approval, expansion and cleanup. The fixture advances external
  events from scheduling snapshots, forces admission retry completions to cancel
  waits while the competitor holds capacity, and fails after 64 scheduling passes.
  A wait callback cannot drive external events because retry completions may keep
  the owner from entering it. Keep its permutation coverage in the
  full class while retaining both compute paths in affected landing gates.
- **Which tier decides a merge:** the hooks judge what they can in seconds and
  nothing more, so main may go red. Follow the [release failure policy](#release-failure-policy).
  Main is where the broad checks run:
  `gate` and `pre-release` run in hosted CI on every pull request and every
  push to main. A tag re-runs nothing, so what main is green for is what ships.
  Run `sparkwing run gate` yourself when a change is broad enough that a hosted
  red would cost more than the wait.
- **What a test step inherits:** every step that starts a product suite
  (`test`, `race-touched`, `store-postgres`, and the release contract
  preflight) clears the bindings `internal/runners/local/env.go` injects into a
  node child and pins `SPARKWING_HOME` to an empty directory of its own, which
  holds no `dev.env` for a CLI verb to find a dashboard through. The gate's
  `test` step, the short suite and the `test` pipeline key that directory by
  checkout and step under `$TMPDIR` and empty it before and after each run, so
  `go test` can replay a passing package that reads the home; a fresh path per
  run defeated its cache. Every product test step also pins
  `SPARKWING_TEST_REPO_STATE` (HEAD's tree plus a hash of uncommitted changes
  and of untracked files' names and contents), which the pipeline module's
  `TestCheckoutStateKeysTheTestCache` reads: go test never hashes files above
  the module and records a variable only while a test runs, so without it a
  change to `.github/` or `bin/` would replay that module's cached pass. Every
  product test step also pins `GIT_CONFIG_GLOBAL=/dev/null` and
  `GIT_CONFIG_NOSYSTEM=1`, so suites read no global or system git config, as
  on a hosted runner: a fixture that commits without its own identity fails
  locally the way it fails in hosted CI. A gate runs inside a sparkwing node, which hands its children the
  machine's admission socket, the dispatcher's service URLs and the run's own
  credentials, so a suite that read one would reach a live service and fail
  only under the gate. The node consumes its WorkStep window before the body
  starts, so a nested Sparkwing command selects its own steps. A variable that
  injector gains and the scrub does not handle fails a contract test in the
  pipeline module.
- **Why the whole-tree vet, test and lint are in neither hook:** the house
  standard puts them in the pre-commit chain, and this repo runs them in `gate`
  on purpose: the broad tier takes many minutes. A hook that
  long is a hook everyone passes `--no-verify`, and it loses the fast-forward
  race whenever a co-maintainer lands first. Hosted CI runs `gate` and
  `pre-release` on every pull request and push to main, so a landing pays for
  them there. What the push tier keeps of the four is the packages the change
  touches: `go build`, `go vet`,
  and the fast linter subset, the whole-tree linters minus the type-and-SSA
  family, which costs minutes. Every touched package is checked even on a
  wide push; the file-count waiver relaxes only elapsed time. No test suite
  runs in a hook tier:
  the fast test class belongs to the release cut, and the hooks run formatters,
  sweeps, contract gates and linters. The suites are the wrong shape for a
  ten-second tier: several hundred `internal/store` tests each spend tens of
  milliseconds in fixture setup.
- **Cheap:** format touched Go files and run the affected package tests, for
  example `go test ./internal/orchestrator -run RunAndAwait`. The `lint`,
  `test`, and `build` pipelines are focused checks when their whole boundary is
  relevant; invoke one with `sparkwing run <name>`.
- **Experimental semantic lint:** `sparkwing run jev-lint` compares changed
  non-test Go functions with deterministically selected candidates across the
  repository. It also reads `.sparkwing/jev-invariants.yaml`, selects invariants
  whose scopes contain changed files, and asks one independent Jev question per
  invariant over a shared bounded diff. Jev judges those contracts, duplicate
  responsibility and package ownership, unnamed complex conditions,
  unexplained magic values, and whether a large change builds a mechanism
  around a reversible premise instead of fixing the cause. Cross-package
  findings may recommend either existing package or a focused shared package.
  Store the API key as the masked `TYPESAFE_API_KEY` Sparkwing secret. Use
  `--dry-run` to inspect every bounded request without a key or network call.
  Jev skips symlinked Go candidates and refuses changed files or invariant
  config reached through a symlink, so a request reads only checkout files.
  Findings are advisory; configuration, analysis, transport, and
  response-schema failures fail the run. Cached exact requests can be read
  without the key.
- **Whole-codebase semantic sweep:** `sparkwing run jev-sweeper` is a manual,
  heavier companion to `jev-lint`. It parses every non-test Go function,
  combines high-signal ranking with a reproducible shuffled sample across
  packages, and batches independent Jev judgments for mixed responsibilities,
  Open/Closed and dependency-inversion violations, abstraction-boundary
  leakage, misleading contracts, primitive obsession, feature envy,
  unnecessary indirection, and duplicate responsibility. The shuffled sample
  rotates daily by default; `--shuffle-seed` reproduces it. `--max-functions`,
  `--max-pairs`, `--sample-functions`, and `--sample-pairs` control breadth;
  `--dry-run` prints every request. Findings remain advisory and do not replace
  deterministic gates.
- **Scripts under `bin/`:** editing one means running `GOWORK=off go test
  ./bin`, about ten seconds. Go tests there pin what the scripts do, including
  the exact argv `bin/install.sh` builds with, so a change to a shell script
  reds a package no other check points at.
- **Orchestrator iteration:** `GOWORK=off go test -short
  -timeout=5m ./internal/orchestrator` keeps the inexpensive `RunLocal` and
  daemon coverage. It skips the process-per-node binary fixtures, scaffolded
  headless module, and tests that exercise sustained contention or real
  timeout windows. Run without `-short` when changing those boundaries. The
  normal `test` and `gate` pipelines retain those tests.
- **Goroutine leaks:** every package with tests carries a `leak_test.go` whose
  `TestMain` hands the suite to `internal/testleak`, so a package fails when a
  goroutine outlives its tests. A new test package needs that file too; copy an
  existing one. A package that must tolerate a goroutine passes its own
  `goleak` option from its `TestMain` and says why in a `safety:` comment. A
  helper process a test spawns by re-executing the test binary runs without the
  check, so a goroutine such a helper leaves behind fails nothing; the check
  covers the suite, not the children it starts. The same package moves HOME,
  SPARKWING_HOME and XDG_CONFIG_HOME into a private sandbox. Before it does,
  it puts the suite's own `go env GOROOT`/bin first on PATH and pins asdf and
  mise to the developer's real data dir, config and tool versions
  (`ASDF_DATA_DIR`, `ASDF_<TOOL>_VERSION`, `MISE_DATA_DIR`, `MISE_CONFIG_DIR`),
  so `go` and `helm` started through a shim still resolve. A variable already
  set is left alone.
- **Frontend dependencies:** the gate's `frontend-deps` step runs
  `pnpm --dir web install --frozen-lockfile --prefer-offline --ignore-scripts`
  before the frontend steps and the Go `test` step, whose `tests/acceptance`
  drives the dashboard client through `web/node_modules`. It is a no-op when
  `web/node_modules` matches the lockfile. The affected gate skips it when the
  change touches no frontend input, unless `web/node_modules` is missing, and
  the land re-check after merging `origin/main` installs a missing tree before
  its tests. Without pnpm and without `web/node_modules` it fails naming that
  command.
- **Go result caching:** ordinary `test` and `gate` checks preserve the
  caller's temporary-directory settings so Go can reuse passing results.
  Test fixtures own cleanup through `t.TempDir`, `t.Cleanup`, or deferred
  removal. Forced race and Postgres runs retain their per-run scratch roots.
- **Heavy packages:** run these by name rather than reaching for `./...`, and
  give the command a timeout the package actually fits in:

  | Package | Suggested `-timeout` |
  | --- | --- |
  | `./cmd/sparkwing` | 10m |
  | `./internal/orchestrator` | 10m |
  | `./internal/cluster` | 2m |

  SDK fixtures share a package-owned source snapshot and binary cache because
  installer re-execs otherwise enter fresh test homes. Source changes,
  ignored embedded inputs, uncached debug builds, and offline compilation
  retain real compiler coverage. Independent subprocess tests run in parallel.
  Contention can still extend these samples; the suggested timeouts leave
  room above the measurements.

- **Normal broad check:** `sparkwing run gate` covers every committed Go
  module, the dashboard TypeScript unit, full ESLint, production build,
  and browser smoke suites, formatting, vet, build, tests, documentation
  mirrors, and source policy. It also runs `go test -race` on the packages
  whose Go files changed (staged, or since origin/main when nothing is
  staged), so a change never reaches main without the race detector having
  seen its own package, and runs `store-postgres` when that change touches
  `internal/store`. `internal/store` itself is the one exception to that race rule: it
  cannot finish inside the step's budget, so `pre-release` races it and the
  gate names the deferral in its log rather than skipping it silently. Unit and ESLint run in parallel; the production build then
  feeds the browser suite.
- **Gating beside other agents:** run independent checks concurrently in separate
  development worktrees when CPU and memory headroom allow. Use `sparkwing run`
  for declared pipelines so admission can account for their reservations;
  focused commands outside a run also consume host resources. Coordinate tests
  that share mutable services, same-repository landings, and production cutovers.
  The broad `gate` job and queued Land's `fast` and `affected-tests` jobs share
  the capacity-one `machine-suite` box group. It queues members on the same
  execution host within one controller and team. Admission accounts for other
  jobs, including `race-touched`, by their declared cores. Gate and race size
  their requests from Go command and shard parallelism, with the existing
  four-core reservation and dedicated execution width for Cloud `--base`.
  `GITHUB_ACTIONS=true` selects the GitHub runner's parallelism.
  The group is released between `fast` and `affected-tests`.
  `affected-tests` holds it until review and tests both finish.
  `sparkwing queue list` shows running and queued work.
- **Gating a branch beside a released daemon:** when the branch's pipeline
  binary carries a newer runs-store schema than the sparkwing hosting this
  machine's admission daemon, admission refuses the run. The refusal names both
  versions, `sparkwing daemon status` for the build the daemon runs, and the
  upgrade. A branch schema that no release carries yet is the case here, so
  install this checkout with `SKIP_WEB_BUILD=1 bash bin/install.sh` and run
  `sparkwing daemon restart`; `sparkwing update` is the answer only once the
  schema ships in a release. Either way the machine keeps one daemon and every
  run keeps its place in `sparkwing queue` and the dashboard.
- **Running against a home of your own:** `SPARKWING_HOME=DIR` points one
  command's state, cache and logs at DIR, which is isolation for work
  that must not touch the operational runs store, the release preview under
  Decisions before landing being the case that needs it. A run started that way
  is arbitrated by whatever daemon lives in that home rather than the machine's,
  so it is invisible to `sparkwing queue` and the dashboard and contends with
  every other run on the OS. It is not a way around a full queue. It does not
  move the user config directory, which answers to `XDG_CONFIG_HOME` and the
  per-file overrides, because what lives there is machine-wide and outlives any
  one home. Config writes outside a redirected home are refused unless a
  per-file override selects their destination. The refusal names both paths
  and the override, such as `SPARKWING_CONFIG` for `config.yaml`. Local keys
  follow the state home and are created there on first need. Without an
  explicit selector, the default home keeps its existing config-directory key
  even if a home-local key also exists. `SPARKWING_SECRETS_KEY_FILE` selects a
  different key file. The local secrets themselves live in the home's own
  `state.db`, and the home
  does not import the machine's `secrets.env` or `config.env`. No variable
  moves `version-hold`.
- **Lint rules:** golangci-lint judges only code new since origin/main. Uploaded
  working trees may have no remote refs or parent history. In that case, the
  gate compares against an empty Git tree, judges every tracked file, and
  reports that wider scope. The same baseline drives scoped source checks
  and lint. The enabled linters also reject `_ = call()` on an error-returning call, nil
  returned after an error was observed, and work started on a context that is
  not the caller's. Drop an error only through a helper that logs why.
- **Dependency boundaries:** `.golangci.yml` keeps the depguard layering rules
  with the other linters, judging changes since `origin/main`. The fast push
  tier includes depguard over the packages the push touches.
- **No sleeps or wall-clock waits in tests:** `internal/sleepcheck` fails any
  `_test.go` that calls `time.Sleep`, `time.After`, `time.Tick`,
  `time.NewTimer` or `time.NewTicker`, or that reads `time.Now` or `time.Since`
  as a wait: an ordering comparison, a loop condition, or a
  `context.WithTimeout` or `WithDeadline` argument. A `time.Now()` that only
  stamps a fixture value is allowed. Write the test to wait on the channel,
  condition, or state the code under test signals, to drive a clock the test
  injects, or to run under `testing/synctest`, whose clock advances once every
  goroutine is blocked. A file that dot-imports `time` cannot be judged and
  fails for that reason. The `test-sleeps` step runs the checker in
  `pre-commit` and `gate` over the staged change, or the change since
  origin/main when nothing is staged, so a new offender fails from the first
  run while the tests written before the rule keep passing until they are
  edited. `GOWORK=off go run ./internal/sleepcheck .` judges every test file in
  the tree, which is how to size the remaining purge. The external-boundary
  allowlist covers the paced runner status polls in
  `internal/controller/direct_source_e2e_test.go` and
  `cmd/sparkwing/local_runner_restart_linux_test.go`, and the OS pipe-drain
  deadline in `internal/nodehost/host_exec_stdin_unix_test.go`. Runner cleanup
  uses a context hang bound and needs no exception.
  Each `time.After` has a `// sleepcheck:external-boundary` reason immediately
  above its select receive arm. The checker allows only the listed path,
  function, duration, select arm and exact marker, and fails if an exception
  disappears or changes or a marker has no approved wait. New waits and sleeps
  still fail.
- **Go directive policy:** the release cut and `pre-release`, beside its version
  checks, reject patch-level requirements in the published root module and
  template registry modules: `go 1.26` and `go 1.26.0` pass; `go 1.26.3` fails.
  Our modules select the patched Go 1.27.4 toolchain separately.
  Pipeline and fixture modules are excluded from this release check. Init and
  pipeline scaffolding share a generator that takes only the running Go
  version's major and minor; this step runs a focused scaffold test against a
  patch-version toolchain to check the generated module through pipeline creation.

- **Expensive or release-boundary:** `sparkwing run pre-release` adds race, chaos,
  vulnerability, dependency-freshness, API, and Terraform gates. Use
  `integration`, `template-verify`, `static-analysis`, and image builds only when
  the change touches those boundaries. `sparkwing run security-scan` runs gosec,
  source-mode govulncheck, gitleaks, and `npm audit`. The Security workflow runs
  it on every pull request and uploads gosec findings to code scanning. The
  release workflow runs neither it nor CodeQL against the tagged commit; the
  run that covered that commit on main is the scan of record. CodeQL reports
  alerts; gosec, govulncheck,
  gitleaks, and `pnpm audit` fail the gate. The advisory scanner retries a
  registry that times out or answers 5xx, and reuses a recorded pass for a day
  when `web/pnpm-lock.yaml` and `web/package.json` are byte-identical to the pass,
  so an unreachable registry fails as its own error rather than as an advisory
  and an unchanged dependency set is still re-asked daily. Run the local
  pipeline when a change touches an HTTP handler, auth, file paths built from
  input, subprocess arguments, or a dependency. Verify dashboard changes
  against real local state with `bash bin/dev-start.sh` (dashboard backend on :4343, `next dev` on :3100)
  and stop it with `bash bin/dev-stop.sh`; the browser gate uses deterministic
  API fixtures on OS-assigned local ports and does not replace that product
  exercise or exercise Kubernetes.
- **Template verification proofs:** `template-verify` scaffolds, builds, lints,
  and explains all registry templates, and runs the runnable ones. The build-cli
  job renders template text and verification manifests through the discovered
  local sparks-core templates module (SPARKWING_SPARKS_CORE_DIR, go.work, or a
  sibling checkout), or the pipeline module's published templates pin when no
  checkout is found. Its annotation names the selected source. The selected
  registry generates the verification jobs, so local additions and removals need
  no pipeline module replacement.
  A local
  run reuses a recorded proof for any template whose proof inputs are
  byte-identical to a previous pass. The digest covers, in one hash per
  template: the template's registry files (every file under its directory in
  the embedded `templates.FS`); the manifest's verification fields (tier,
  fixture, `verify_params`, `verify_tools`); the state of the sparkwing
  checkout, which is what supplies the SDK every scaffold replaces, the CLI
  each step invokes, and this verifier's own source; the same for the local
  sparks-core checkout; `go env GOVERSION GOOS GOARCH`; and the resolved path
  and `--version` output of every host tool the template's fixture and
  `verify_tools` need, plus, for Docker, whether the daemon actually answers
  `docker info`, because the binary being on PATH is not what the run step
  needs. A proof-format constant sits in the same hash, so widening or
  narrowing that list invalidates every recorded proof.

  A checkout's state means `git rev-parse HEAD`, `git diff --binary HEAD`, and
  the contents of every non-ignored untracked file, plus two gitignored inputs
  that change what gets verified and would otherwise be invisible: `go.work`
  (which steers the plain `go build` that produces the verify CLI, and the
  sparks-core discovery that pins a scaffold's modules) and
  `internal/web/next-out` (embedded into that CLI). Any other gitignored file
  that reaches a build is outside the digest; add it to that list when one
  appears.

  Reuse is fail-closed. Any input that cannot be established refuses reuse and
  verifies the template again: a checkout that is not a git repository, an
  unreadable untracked file or build input, a template that will not digest,
  and in particular the absence of a local sparks-core checkout, because
  without one a scaffold's `go mod tidy` resolves published module versions
  that no digest covers. A partial verification is never recorded: when a
  runnable template's toolchain is missing the run step is skipped, the gate
  stays green, and no proof is written, so the template is verified again once
  the toolchain appears. Dry runs read the admitted run's job and step status.
  Any skipped execution is partial verification, including a body skipped as
  `no_dry_run_defined`. It reports no completed run step and records no proof.
  Missing or unreadable execution evidence fails verification.
  Reuse never changes the generated fan-out: every selected template keeps its
  node, and the decision is made inside it, so a digest miss cannot be lost in
  plan shaping. Third-party module resolution is outside the digest either way,
  which is the standing reason the boundary stays exhaustive: the release
  pipeline passes `--exhaustive`, and so should any manual run being used as a
  release proof.

  Recorded proofs live in `template-verify-proofs/` under the sparkwing
  directory of the OS user cache directory (`~/Library/Caches/sparkwing` on
  macOS, `${XDG_CACHE_HOME:-~/.cache}/sparkwing` on Linux), one JSON file per
  digest naming the template, its tier, and whether the run step executed. A
  file that does not parse or carries another proof format is ignored, writes
  are atomic, and recording prunes anything older than 14 days. Delete the
  directory to force full verification without `--exhaustive`.

- **Store dialect matrix:** `sparkwing run store-postgres` runs the
  `internal/store` tests that exercise PostgreSQL with
  `SPARKWING_TEST_STORE=postgres`, so every store test that opens through
  `internal/store/internal/storetest` exercises the Postgres dialect instead of a SQLite
  file. It compiles one `internal/store` test binary, lists its top-level
  Test, Example and Fuzz names, and runs every one except two kinds. The
  internal tests in the files named by
  `.sparkwing/jobs/store_postgres_sqlite_only.txt` are skipped: the internal
  package cannot import `storetest`, so those tests and fuzz seeds only ever
  open SQLite, the full Go suite already runs them, and they cost 432 s of a
  24m32s hosted-profile run. `TestClaimSecurityRegressions` follows the suite's
  dialect and so repeats `TestPostgresClaimSecurityRegressions` here; only the
  PostgreSQL copy runs. Any other test runs, including every test in a new
  file, so a PostgreSQL path the source scan cannot see, such as a non-test
  helper, is still exercised. The step logs the count and SHA-256 of the
  listed, selected and skipped names, and fails when a listed name has no
  declaration or is placed other than exactly once, or the duplicate pair is
  no longer listed. It also fails, as does
  `TestStorePostgresSelectionCoversTheStoreInventory` in `.sparkwing/jobs`,
  when a named file is missing, declares no internal test, or reaches
  `SPARKWING_TEST_PG_URL` or the `pgx` driver directly or through a helper or
  constant in the internal test files; remove such a file from the list.
  Ready-store fixtures (`storetest.Open` and `OpenPostgres`) reuse one initialized
  PostgreSQL template per test process/server and clone a database for each
  fixture. Their TestMain closes the cache and drops its templates after every
  fixture has closed. Servers without CREATE DATABASE permission use the
  existing fresh-schema path. `New` and `NewPostgres` still reserve empty
  schemas for migration, reopening and cases with multiple schemas in one database;
  the named fresh-open and schema-lock tests use those constructors explicitly.
  A killed process on a configured server can leave `sw_fixture_clone_*` and
  `sw_fixture_template_*` databases; remove them only after their owner is gone.
  The embedded server's disposable cluster is removed at run teardown.
  The sorted selection is assigned round-robin, each
  name exactly once, to one `GOMAXPROCS=1` shard process per core a Go step
  may hold: four on the hosted four-core runner, the Go step bound
  (`(cpus-1)/2` shared, `cpus/2` under `CI`) elsewhere. Each shard gets a
  database of its own, created on the server before the shards start and
  dropped `WITH (FORCE)` afterwards, because raw-target schema initialization uses a
  database-scoped migration advisory lock. The shard URL drops `dbname` and `database`
  query options, which pgx reads over the URL path, and a `service` reference
  is refused. A run killed outright can leave `sparkwing_store_shard_*`
  databases on a configured server, and so can a `CREATE DATABASE` whose
  success reply is lost: the server committed it, but the job saw an error and
  never tracked the name. Both are rare and fail the run; drop the leftovers
  by hand with `DROP DATABASE <name> WITH (FORCE)` once no run is using them. A failing shard
  stops the others. `internal/store/internal/storetest` and
  `internal/store/teststore` run whole. Round-robin leaves
  `TestAdmissionDeterministicSimulation` (about two minutes) in the longest
  shard. It reuses `SPARKWING_TEST_PG_URL` when
  that is set and otherwise starts an embedded Postgres on a free port (data
  under a temporary root, binaries cached in `$TMPDIR`), then stops it and
  removes the data directory whether the suite passes, fails, or is
  interrupted. A run killed outright before its teardown finishes, as a
  cancelled run is one second after it is asked to stop, leaves a
  `sparkwing-store-postgres-*` directory under `TMPDIR`; the next run removes
  every such directory whose owning process is gone. No Docker. A failing suite prints the tail of the server log,
  which embedded-postgres only makes available once the server has stopped.
  A server that will not start is retried once on a fresh port and then
  fails the step. `pre-release` runs it after the race gate, under a
  thirty-minute timeout. Each shard has a 25-minute Go timeout, so a hung test
  reports its own stack only when the work before the shards start (embedded
  server start or download, the helper packages, compiling, listing and
  creating the databases) finishes within the step's other five minutes; past
  that, the step's thirty-minute deadline cancels the shards without a stack. The first run may also download the Postgres binaries.

- **Hosted pre-release PostgreSQL:** the canonical pre-release job supplies a
  PostgreSQL 17 service through `SPARKWING_TEST_PG_URL`, so `store-postgres`
  uses that server without downloading embedded binaries. The separate
  Postgres conformance job verifies the backup drill with a matching client
  and rejects skipped tests. Its service shares `/var/run/postgresql` with the
  runner so the Unix socket identity test exercises a real socket connection.
  Its no-skip selection also includes
  `TestCLITokenMintUsesCommittedMembershipAuthority` in `internal/controller`,
  retaining the PostgreSQL bearer-token regression for demotion and removal.
  It also retains both SQL literal regressions, which verify that run filters
  and secret mutations treat SQL metacharacters as data and preserve team
  isolation. `sparkwing run integration` runs them through its full `./...`
  suite against the local PostgreSQL fixture.

- **Native Windows CLI smoke:** `TestWindowsLocalRunStartsControllerAndFinishes`
  (`cmd/sparkwing`) builds the CLI and runs a Go pipeline through standalone
  and shared controllers in private homes. Both modes require a successful
  `run_finish` receipt and a zero process exit status. Run it on Windows with
  `go test -p 1 ./cmd/sparkwing -run TestWindowsLocalRunStartsControllerAndFinishes`.
  `TestWindowsLocalHTTPKeepsRequestAliveAcrossReuse` in `internal/wingd`
  guards the named-pipe deadline contract beneath startup and runner claims.

- **Standalone client shutdown:**
  `TestStandaloneRunClosesUnusedClientDialBeforeControllerDrain` (`cmd/sparkwing`,
  Unix) cancels a request while its dial completes, then shuts down the real
  standalone runtime. It requires the CLI to close the unused connection before
  controller draining, without changing the API drain deadline. Native Windows
  verification repeats standalone pipeline runs and requires both successful
  `run_finish` receipts and zero process exits.

- **Local controller smoke:** `TestLocalControllerSmoke` (`cmd/sparkwing`,
  part of the full suite) builds the CLI and drives every local controller
  command against a fresh local controller in a private home. It fails when
  the host refuses unix sockets; a sandbox that is known to refuse them sets
  `SPARKWING_TEST_NO_LOCAL_SOCKETS=1` to skip it instead.

- **Native user-manager qualification:** ordinary Go tests deny unexpected
  `systemd-run`, `systemctl`, and `launchctl` calls. Workflow fixtures use the
  existing unmanaged runner backend through a compile-time overlay; release
  binaries never receive that overlay. `TestLocalControllerSmoke` and local-run
  workflows remain in the full suite, and
  `TestReleaseBuildExcludesUserManagerFixtureInjection` checks the release boundary.
  Managed installation, cgroup and restart tests skip visibly unless
  `SPARKWING_TEST_USER_MANAGER=1` is set. This dedicated manual tier must run in a
  quiet window for changes to managed runner startup, upgrades, cgroups or service
  lifecycle. Use one test at a time, with no concurrent lands, deploys or service
  trials; private homes do not isolate the user manager. Never enable the gate
  globally in shared full-suite or race jobs. Schedule the tier on a dedicated
  service host for nightly qualification. Its initial budget is 20 minutes,
  excluding cold compilation; record measured runtime before changing that budget.

  On Linux, run the named cases sequentially with the gate set, private
  `HOME`/`SPARKWING_HOME`, and disposable Go temporary state. The binary input
  must be the absolute CLI artifact, not its state-binding candidate launcher:

  ```sh
  export SPARKWING_TEST_USER_MANAGER=1
  export SPARKWING_LOCAL_E2E_BINARY=/absolute/built/sparkwing
  go test -count=1 -p 1 ./internal/wingd -run '^TestManagedUnitsPrivateConfigWithSystemd$'
  go test -count=1 -p 1 ./internal/wingd/client -run '^TestServiceDaemonLeavesCallerCgroup$'
  go test -count=1 -p 1 ./cmd/sparkwing -run '^TestDetachedLocalRunnerSurvivesControllerRestartAndRollsBuild$'
  go test -count=1 -p 1 ./cmd/sparkwing -run '^TestDrainingLocalOwnerAwaitsChildWhileSuccessorRunsDependencies$'
  go test -count=1 -p 1 ./cmd/sparkwing -run '^TestManagedControllerPreservesKeyAcrossRestart$'
  go test -count=1 -p 1 ./bin -run '^TestDesktopUpdaterSystemdLifetimeAndControllerKeys$'
  go test -count=1 -p 1 ./bin -run '^TestDesktopUpdaterRealIntakeRaceAndRollback$'
  go test -count=1 -p 1 ./bin -run '^TestDesktopUpdaterProvidedBinaryControllerInspection$'
  go test -tags=rewrite_integration -count=1 -p 1 ./cmd/sparkwing -run '^TestReviewProductionExecutionBindings$'
  ```

  The provided-binary local-controller feature scenarios also need this gate;
  follow `tests/acceptance/LOCAL_FEATURES.md`. On macOS, use the same gate for
  native launchd coverage in the platform qualification window. List user units
  before and after each case and verify every test-owned unit is gone. The
  package-level guard remains enabled for all ordinary runs, even if a test
  handles and ignores the manager's error.

- **Dedicated-user runner isolation:** `runners add --service-user` creates an
  OS user, a system unit and a root-owned enrollment record, so its live proof
  never runs on a workstation. Dispatch the manual
  `.github/workflows/runner-isolation-proof.yaml` (`gh workflow run
  runner-isolation-proof.yaml --ref main`) after changing the account, unit,
  enrollment, pause or deletion paths. It runs `bin/runner-isolation-proof.sh`
  on a discarded GitHub-hosted VM. Before any write the script requires
  GitHub's root-owned runner-image record and the Actions `Runner.Worker` among
  its ancestors, which no environment variable supplies. A self-hosted runner
  built from GitHub's image would still pass, so never point the workflow at
  self-hosted labels, and never run the script by hand. `--preflight` runs only
  that check and writes nothing. The controller is built with a per-run license
  key so `admin session` works without secrets. A personal service-user runner claims a real job that
  reports its uid and what it could read, because shared runners take only
  repository-proven work. The run prints PASS or FAIL per check and uploads
  `runner-isolation-evidence-<run>-<attempt>`.

- **Postgres conformance:** the store, backend, and orchestrator Postgres
  suites skip when `SPARKWING_TEST_PG_URL` is unset, and fail when it is
  set to a database they cannot reach. Start one with `docker run --rm -d
  --name sw-pg -e POSTGRES_PASSWORD=postgres -p 5433:5432 postgres:17`,
  then run `SPARKWING_REQUIRE_PG=1
  SPARKWING_TEST_PG_URL=postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable
  go test -v -run 'Postgres|Pg' ./internal/store ./internal/backend
  ./internal/orchestrator`. `SPARKWING_REQUIRE_PG` turns an unset URL and
  `SPARKWING_INTEGRATION_DISABLE=1` into failures. That is not proof on
  its own, so both lanes that promise Postgres coverage run those suites
  verbosely and fail on any `--- SKIP` line: the hosted `Postgres
  conformance` job in `.github/workflows/ci.yaml` on its nightly and manual
  dispatch tier, and `sparkwing run integration` against its
  Dockerized Postgres and MinIO. The hosted job runs the three packages
  under `-short`, leaving the names its no-skip step selects to that step, because the canonical gate already runs them in full on
  SQLite and the full store suite overran Go's ten-minute package limit on
  a hosted runner; the tests that switch to the service database stay in
  the short class.
- **Postgres client on the hosted lane:** the `Postgres conformance` job
  installs `postgresql-client-17` from the PostgreSQL Apt repository and
  points `SPARKWING_PG_BIN` at `/usr/lib/postgresql/17/bin`, because
  Ubuntu's own repository tops out at 16 and a `pg_dump` older than its
  server refuses the dump outright. `SPARKWING_PG_BIN` names the real
  binaries rather than `/usr/bin/pg_dump`, which is postgresql-common's
  wrapper and picks a version from the clusters the image happens to
  have. A step prints both client versions and fails when the major is
  below 17, so a repository that stops shipping 17 fails the job instead
  of quietly sending the tests back to skipping. `sparkwing run
  integration` installs nothing and takes whatever client the developer's
  machine has.
- **Backup and restore drill:** `TestBackupRestoreDrill` in `internal/store`
  checks durable state through a backup and restore. Its SQLite
  subtests always run. Its Postgres subtest shells out to `pg_dump` and
  `pg_restore`, taking them from `SPARKWING_PG_BIN` or `PATH` and
  skipping when neither supplies them. Run it against a server with
  `SPARKWING_TEST_PG_URL=... SPARKWING_PG_BIN=/usr/lib/postgresql/17/bin
  go test -run TestBackupRestoreDrill -v ./internal/store`. The drill's name
  carries no dialect, so the hosted no-skip step selects it by name:
  `-run 'Postgres|Pg|BackupRestoreDrill'`. That is what turns a missing
  or too-old client into a red lane rather than a green one with a
  skipped subtest. `sparkwing run integration` still selects
  `Postgres|Pg` only, so the drill can skip there unnoticed.
- **Kubernetes product path:** `sparkwing run k8s-e2e` proves authenticated
  uploaded-source intake, runner execution, logs, cancellation, retry, restarts, and
  retained state against an explicit Kubernetes context and caller-supplied
  image set. It requires a signed multi-team license through
  `SPARKWING_K8S_E2E_LICENSE_FILE` and an exact namespace/release cleanup
  allow-list. It never creates or deletes cluster infrastructure. GitHub webhook
  intake and remote Git fetching need separate evidence. This check is manual and
  opt-in because it uses a real cluster. The fixture's SDK require pin follows
  the release pins, with a local replace to the checkout. The harness bundles
  the committed SDK source from HEAD and rewrites that replace inside the
  uploaded repository so runner compilation needs no caller checkout.

The hosted canonical gate installs `sdk/typescript` dependencies with
`npm ci --ignore-scripts` before Go tests, because the native protocol and
conformance tests compile the TypeScript SDK. Local runs need the same install.

## Decisions before landing

- **Review:** use an independent reviewer for public SDK/CLI contracts, cache or
  admission invariants, concurrency, process lifecycle, persistent schemas,
  security boundaries, or release machinery. Otherwise state why it was not
  valuable.
- **Documentation:** keep README, CLI help, source docs, examples, generated
  references, and `pkg/docs/mirror/` aligned with behavior. Run the owning drift
  check instead of editing generated mirrors by hand.
- **Generated files:** `bash bin/regen-all.sh` rewrites every derived file
  (cli/config/sdk/api references, `api/openapi.yaml`, `.apidiff/`,
  `pkg/docs/mirror/`, and the vendored runner-bundle tarball) in one pass and
  is a no-op on a clean tree. Resolve a merge conflict in any of them by
  taking either side, running that script, and committing the result.
- **Changelog:** notable adopter-facing behavior belongs in a unique
  `docs/changes/<slug>.md` with a `###` category and scope-prefixed bullets.
  Breaking guides go in `docs/migrations/unreleased/<slug>.md`, starting with
  an H2. Follow `CHANGELOG-STYLE.md` and run `bash bin/sync-docs.sh` to mirror
  the fragments. Keep shared unreleased aggregates empty. Embedded docs,
  weekly-release and release gates collect fragments; the release cut writes
  the versioned notes, repoints guide links and deletes consumed fragment pairs.
- **Tests:** record the focused checks selected, or why execution was waived.
  Do not run every race, Docker, or integration suite by default.
- **Release:** merging is not a release; a release is a tag push. Select an
  exact source with `sparkwing run release -- --commit SHA` (default `HEAD`).
  The source must be an ancestor of freshly fetched `origin/main`.
  The release owns a detached worktree and compiles `release-cut` from
  the selected source, using a private `SPARKWING_HOME`. The source must
  contain the `release-cut` entrypoint. The invoking checkout stays untouched,
  including uncommitted work. Lands continue on main throughout the cut.
  Verification follows the manual fallback and weekly CI policy described
  above. Incremental push CI is not full-suite evidence. Published module
  freshness, Go directive policy, coherent SDK
  pins, changelog links and docs sidebar checks still run. The cut prepares
  notes, migrations and SDK pins, then checks schema/wire coverage before
  committing those paths with `--no-verify`.
  The annotated tag points to that prepared release commit, whose source
  parent remains the selected SHA. A separate detached worktree merges that
  commit into freshly fetched main. It removes only unchanged selected entries
  from main's `[Unreleased]` section, keeps newer or edited notes there, and
  copies the release section unchanged. The merge preserves main's newer code.
  Publication atomically pushes the merge to main and the tag to origin. If
  main moves during the push, only the metadata merge and push are retried,
  with the same release commit and tag. A conflict or repeated contention
  stops publication for manual recovery; it does not require freezing main.
  Failed cuts retain their worktrees at the paths printed in the logs.
  The publication push passes `--no-verify` and logs that it skips the
  checkout's pre-push hook: the cut already ran its gates, and the hook's
  gate output never reaches the release log. Each git command in `push-tag`
  logs before it runs and has its own deadline, five minutes for fetch,
  ls-remote and push and two for local commands, so a hang fails naming the
  command. A retry replaces a local tag that origin lacks when, after a fresh
  fetch of every origin branch, no remote ref contains its commit, which is
  what an interrupted cut leaves. It refuses any other existing local tag,
  keeps the tag when that fetch fails, and never touches a tag on origin.
  Preview with
  `SPARKWING_HOME="$(mktemp -d)" sparkwing run release --dry-run -- --commit SHA`,
  then use `--allow-risk destructive,prod` for publication. Within one controller, manual and weekly
  releases share a release concurrency group. Release owners serialize cuts
  across isolated homes; lands use their own path.
  From the tag push on, `.github/workflows/release.yaml` resolves the tag,
  builds, signs and publishes artifacts, and creates the GitHub release.
  Notes come from the tag's changelog section, with the annotated tag message
  and a pointer to CHANGELOG.md as fallbacks. Handle failures through the
  [release failure policy](#release-failure-policy).
- **Independent verification:** for user-facing local-execution changes, build
  the intended revision with `SKIP_WEB_BUILD=1 bash bin/install.sh` when the web
  bundle is unchanged, then exercise the installed CLI and daemon. To exercise a
  branch against real runs without replacing the binary every other repo and
  timer resolve, install it under its own name beside the real one:
  `SPARKWING_INSTALL_NAME=sparkwing-crons bash bin/install.sh` writes
  `~/.local/bin/sparkwing-crons`, which shares the home and store, so it must
  be built at the same store schema as the real one. Verify SDK,
  templates, integrations, browser behavior, or release assets when those
  surfaces changed.

## Weekly SDK and CLI delivery

`weekly-release` runs Monday at 09:00 America/Denver. It selects freshly
fetched `origin/main` by default, or `--commit SHA`, and uses the same detached
worktree cut and exact-source CI check as `release`. The inner cut pins its
pipeline to the caller tooling commit with `--pipeline-ref`, while jobs
operate on the selected source checkout. SDK verification executes from that
current pipeline code, so an older source cannot supply older release tooling.
The weekly run skips when the selected source has no `[Unreleased]` entries. Its default minor bump stays on v0.x.
Release cuts are pre-approved. The scheduled entrypoint supplies the cut's
`--allow-risk destructive,prod` authorization. Main keeps accepting lands
while the cut runs. Publication merges the prepared metadata into fresh main
and atomically pushes that merge with the tag on the separate release commit.

Go SDK releases use independent `sparkwing/v0.x.y` tags. Before preparing
engine release metadata, the cut hashes sorted tracked SDK module paths and
committed bytes, including go.mod, Go source, embeds, test fixtures and the
inherited root license when the SDK has no license file. Root build/dist output
directories, vendor/cache directories and root workspace files are excluded. Tracked fixture inputs remain source inputs. The annotated SDK
tag records `SDK-Source-SHA256`; the cut verifies that record against the tag's
source. An unchanged digest reuses that SDK version without creating a tag.
The first SDK version is v0.1.0. Any source change advances the v0 minor and
resets patch to zero, because pre-1.0 source changes may break the API. Engine
version selection does not affect SDK version selection.

A changed SDK runs its module tests and Go/TypeScript protocol conformance in
the SDK publication job, then tags the selected tested source and publishes
that SDK tag first. A fresh module cache and the public Go proxy must resolve
the SDK before the cut prepares the engine requirement and SDK pins. A proxy
failure stops the engine cut; retry reuses the immutable SDK tag only after
comparing its recorded digest with its source. A conflicting tag fails rather
than being moved. Dry runs compute and report the decision without publication.
The engine metadata commit updates root go.mod/go.sum and SDK pin artifacts to
the independently selected SDK version. The SDK source digest excludes that
engine metadata.

Hosted publication runs `bin/go-sdk-release-entry.mjs` from its selected
publication tools. It reads the engine source's SDK requirement and that SDK
tag's recorded digest and protocol, then uploads `sdk-entry-go.json` with the
engine artifacts. The signed policy publisher retains that SDK entry across
engine releases. Older-tag recovery uses current publication tools while the
SDK identity still comes from the selected source and published SDK tag.

The engine tag starts the existing
`.github/workflows/release.yaml` publication steps for signed CLI binaries,
images and the TypeScript package. Before cutting, the weekly pipeline starts
or resumes the Bitwing `sparkwing-release` workflow using the version as its
idempotency key. It records the cut's immutable tag and commit in a blocked
`cut` handoff. The release owner resumes with `bitwing workflow next --run WR-N`
to confirm hosted publication, publish the matching product site docs, verify
production through the platform repository, and reconcile downstream pins. The checklist
stays open until every required step has evidence. A failed cut retains the
detached worktree at the path printed in its job log.

Run the same path sooner, or preview the cut:

```sh
SPARKWING_HOME="$HOME/.local/state/sparkwing-delivery" sparkwing run weekly-release
SPARKWING_HOME="$HOME/.local/state/sparkwing-delivery" sparkwing run weekly-release -- --bump patch
SPARKWING_HOME="$HOME/.local/state/sparkwing-delivery" sparkwing run weekly-release -- --version v0.70.0
SPARKWING_HOME="$HOME/.local/state/sparkwing-delivery" sparkwing run weekly-release --dry-run
```

`owned-sdk-main` runs daily at 08:00 America/Denver. It resolves green
Sparkwing main to an exact v0 Go module version. For each eligible repository,
it sets both the SDK requirement and a versioned replacement to that version,
tidies the pipeline module, commits only its go.mod and go.sum, and invokes
`xwing env land`. The replacement lets development pipelines use the installed
host without requesting an unpublished signed engine release.

Eligible repos are immediate children of `~/code` with a Go pipeline module,
a GitHub origin whose owner `ownedOrigin` in `.sparkwing/jobs/owned_sdk.go`
allows, and an Xwing policy
with `landing.branch: main` and a verification command. Pull-request policies
and Sparkwing itself are excluded. Xwing applies the repo's committed landing
policy and verification. An unchanged main pin is skipped. A retained candidate
that already has the target pin still retries landing and its verification. A failed update retains
its Xwing environment for repair, while the pipeline tries the remaining repos
and reports all failures. TypeScript repos keep their published SDK pins and
can use `sparkwing pipeline upgrade` after a release.

```sh
SPARKWING_HOME="$HOME/.local/state/sparkwing-delivery" sparkwing run owned-sdk-main --dry-run -- --repos-root "$HOME/code"
SPARKWING_HOME="$HOME/.local/state/sparkwing-delivery" sparkwing run owned-sdk-main -- --repos-root "$HOME/code"
```

`desktop-update` runs daily at 07:00 America/Denver. It hands activation to
`sparkwing-desktop-update.service` because the scheduling run must finish before
its controller can restart. The user unit clones origin/main, requires green
push CI at that SHA, and builds the CLI, runner and dashboard in a private
directory beside the installed binaries. It then waits up to 30 minutes for
runs, queued triggers and held leases in both the default and scheduling state
homes, plus active systemd land units, to finish. A failed inspection or timeout
fails the unit and leaves the installed binaries unchanged. Once idle, it
renames the staged binaries into `~/.local/bin` and restarts the controllers
that were running. The standalone runner service keeps its running process.
Its replacement binary takes effect at that service's next restart.
Builds and restart commands stay in the updater's cgroup and stop with its unit.
Replacement controllers start in separate scopes so they survive updater exit.
Before replacing binaries, the worker reads each running controller's key and
config selectors from its process environment. It restores those selectors only
for that controller's restart, keeping the default and delivery stores on their
existing keys. An unreadable environment refuses the update before either swap.
If the CLI rename fails after replacing the runner, the updater restores the
previous runner and exits with a retry message. A failed restore names the
retained binary and destination for manual repair. Replayable keyed run intake
can wait up to 30 seconds across a controller restart, covering land starts
that arrive after the idle snapshot.
Install the intake retry fix while the queue is idle before arming unattended
updates. Processes already running an older CLI keep that client's retry budget.

The pipeline result confirms the handoff. Read the activation result with
`journalctl --user -u sparkwing-desktop-update`. Use
`systemctl --user status sparkwing-desktop-update` while it runs. Concurrent updater attempts
refuse rather than replacing an active updater. Failed attempts retain their
staging directory, printed in the journal, for inspection. The updater never
cancels work or stops land units. Continuous activity defers the update to a
later invocation. New work can still arrive while it waits.

Preview the handoff without building, installing or restarting anything:

```sh
SPARKWING_HOME="$HOME/.local/state/sparkwing-delivery" sparkwing run desktop-update --dry-run
```

Arm the declarations from the main checkout on a host with Git push access,
GitHub CLI authentication for CI reads, Go, Bitwing, Xwing and Sparkwing on PATH:

```sh
export SPARKWING_HOME="$HOME/.local/state/sparkwing-delivery"
export SPARKWING_SECRETS_KEY_FILE="$SPARKWING_HOME/secrets.key"
sparkwing schedules install -C "$HOME/code/sparkwing" --schedule weekly-release/weekly-sdk-cli,owned-sdk-main/owned-sdk-main,desktop-update/desktop-main --unpinned
sparkwing schedules list
```

The dedicated state directory keeps release work out of the operational runs
store. The private key-file provider also works on headless hosts. Use those
same exports for manual runs. Keep HOME at the operator's home so Git signing and GitHub authentication
remain available. Sparkwing owns cadence and overlap handling through its
schedule evaluator and managed timer. `--unpinned` compiles the main checkout
on each fire so later pipeline fixes take effect. This installation command
selects only these schedules and preserves other installed schedules. The desktop
updater also needs a systemd user manager, `jq`, `flock`, `timeout`, pnpm and Go.

## Release failure policy

Main may be red. If a change breaks it and the fix is not obvious, revert that
change first. Fix local preflight refusals on main, rerun the affected checks
after the correction, and retry the cut. Fix a failed hosted build forward on
main and publish a later patch tag. Never re-cut or move a published release tag.

Do not rerun an unchanged failing check merely to obtain green. A demonstrated
environment or tool setup correction can justify rerunning the same source.
Retain the original failure and correction as evidence.
