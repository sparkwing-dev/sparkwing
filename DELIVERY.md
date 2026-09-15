# Delivery

Use the smallest evidence that covers the change. A lead agent owns the decision;
this file is a menu and checklist, not a command that every change must run.

## Xwing workflows

The tracked `.xwing-env.yaml` declares dependency setup, local builds, tests,
and foreground execution. Run `xwing app commands` for the available tasks.
`xwing env land` uses the existing repository gate before fast-forwarding and pushing
main, then applies Xwing's default archive retention. Public releases remain
separate. Stateful CLI commands use their normal configuration; use a candidate
launcher when testing isolated tool state.

## Checks

### Release build and publication

The hosted release builds the dashboard once and shares it with six target
jobs. Each target compiles its supported commands in one Go cache; all 28
binary outputs remain available. Linux image packaging copies those same
executables through the Dockerfiles' `release` stage. The default Dockerfile
target still builds from source.

One final job assembles image manifests, signs the assets, and publishes the
complete release. Publication is serialized across tags; builds remain
parallel. Only a stable version above every published stable release may
become GitHub Latest or move the image `latest` tags. Failed release-list
lookups stop publication. Manual rebuilds use the selected tag's source and
the current workflow's publication tools and image recipes.

The five-minute target includes runner queueing and publication. It has not
yet been measured for this workflow; local fixtures establish its contracts,
not hosted latency.

### Check tiers

Pinned actionlint v1.7.12 does not recognize GitHub's `concurrency.queue`.
Its config ignores only that exact diagnostic in `release.yaml`. The workflow
contract tests require the publication job's shared group, `queue: max`, and
`cancel-in-progress: false`, and reject uncovered queue use elsewhere in that
file. Other syntax and workflow checks remain active.

- **The three tiers:** `pre-commit` judges the staged change against this
  repo's source policy and nothing else; the git pre-commit hook runs it.
  `pre-push` is the fast tier the git pre-push hook runs, and it repeats
  nothing the commit tier already ran: the changelog, OpenAPI and API-snapshot
  gates, and `go build`, `go vet` and the fast linter subset over the packages
  the push touches, with no package-count cutoff. A
  runner reserves the combined CPU demand of those three concurrent compiler
  tasks, up to 9 cores. Smaller runners divide their available cores across
  the tasks.
  The tiers shift left, so a
  push whose commits skipped the commit hook is judged by `gate` rather than a second
  time here. Rebase replay does not fire pre-commit, and automatic merges use
  pre-merge-commit; neither hook tier proves source policy over those resulting
  trees. Run `gate` when that evidence is required. Everything
  else is `gate` (the broad check) and `pre-release` (the release boundary),
  which `sparkwing run <name>` runs on demand and which hosted CI runs on every
  pull request and every push to main; no git hook fires either.
  `sparkwing pipeline hooks install` arms the two hooks in a checkout and
  `sparkwing pipeline hooks status` is the proof they fire; a definition alone
  proves nothing. A hook script names its pipeline, so a checkout whose hooks
  were installed when the pre-push hook ran `gate` keeps running `gate` until
  `sparkwing pipeline hooks install` rewrites them.
- **The three check classes and their budgets:** `pre-commit` is the source
  policy at 3 seconds, `pre-push` the fast tier with a one-minute hard limit,
  and the release cut, the `release-cut-checks` job the `release` pipeline runs
  before it tags, 5 minutes for build, full lint, the fast test class,
  published-version and SDK-pin checks, and changelog-link checks in parallel.
  Each of those jobs times its own steps and fails when the class overruns,
  naming the slowest step and its cost, so a class cannot regrow unnoticed.
  Above 25 changed Go files, the two hook tiers waive only their time budgets
  and still report the span and slowest step. Every selected check still runs.
  At or below 25 files, both hook budgets are enforced. The release cut always
  enforces its five-minute budget, regardless of change size. The
  budget judges the span the job's own steps cover, not the admission wait or
  the 2.5 s the pipeline binary takes to recompile after a Go change. The
  formatters are the per-file cost in the commit tier, and `goimports` inside
  `golangci-lint fmt` is all of it: over a hundred Go files `gofumpt` measured
  0.38 s, `goimports` 10.9 s and `gofmt` 0.09 s, so a Go file costs about
  0.11 s. `gate`
  and `pre-release` are the heavier classes: they carry no budget and run
  asynchronously, on demand and in hosted CI. Measured on this 16-core Linux
  host with a warm cache, the release cut's three members cost 9 s (build),
  92 s (the full linter over both modules) and 81 s (`go test -short`), so the
  class costs about 95 s of its 5 minutes; the same suite without `-short`
  costs 289 s, which is what the fast class removes. `sparkwing runs stats
  --pipeline pre-commit --since 7d` reports what a class has cost over the
  week, and `sparkwing runs timeline --run <id> --steps` breaks one run into
  its steps; the runs store is shared across repositories, so filter the runs
  by repo before reading a per-pipeline figure as this one's.
- **The fast test class:** the release cut runs `go test -short`. A test whose
  own runtime passes 200 ms guards itself with
  `if testing.Short() { t.Skip("slow: ...") }` naming what costs the time, so a
  new slow test is either cheap enough for the fast class or says why it is
  not. The short class retains a real-store risk-admission test that requires
  an unapproved submission to leave the queue empty. First-run compilation,
  ref selection and pinned-binary risk fixtures remain in the full class.
  `gate` runs the suite without `-short`, which is where every guarded test is
  still judged.
- **What the two hooks cost:** measured on this 16-core Linux host. `pre-push`
  retains a ten-second target when the compiler cache is warm. Run records
  expose total and per-step durations, but they carry no compiler-cache
  temperature signal, so the target is measured rather than enforced. A warm
  integrated run over 13 changed Go files took 10.678 s and missed the target;
  fast lint took 10.678 s and build took 9.782 s. A controlled cold run of the
  same functional checks took 24.432 s, and broader cold parallel samples took
  51.182 s and 56.923 s. These observations fit the one-minute hard limit but
  do not establish a ceiling for every cold input. A change to a Go file also
  invalidates the cached `.sparkwing/` pipeline binary, so the first hook run
  after one pays about 2.5 s to recompile it outside the timed job span.
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
  node child, pins `SPARKWING_HOME` to a fresh directory of its own, and
  exports `SPARKWING_DEV_ENV_DISABLE=1` to close the `dev.env` fallback behind
  every URL. A gate runs inside a sparkwing node, which hands its children the
  machine's admission socket, the dispatcher's service URLs and the run's own
  credentials, so a suite that read one would reach a live service and fail
  only under the gate. A variable that injector gains and the scrub does not
  handle fails a contract test in the pipeline module.
- **Why the whole-tree vet, test and lint are in neither hook:** the house
  standard puts them in the pre-commit chain, and this repo runs them in `gate`
  on purpose. The broad tier takes 12 to 24 minutes through the shared
  admission daemon (the Postgres suite 401 s, the race tests 401 s, the full
  unit suite 315 s, lint 117 s), a hook that long is a hook everyone passes
  `--no-verify`, and it loses the fast-forward race whenever a co-maintainer
  lands first. Hosted CI runs `gate` and `pre-release` on every pull request
  and every push to main, so a landing pays for them there. What the push tier
  keeps of the four is the packages the change touches: `go build`, `go vet`,
  and the fast linter subset, the whole-tree linters minus the type-and-SSA
  family, which costs minutes. Every touched package is checked even on a
  wide push; the file-count waiver relaxes only elapsed time. No test suite
  runs in a hook tier:
  the fast test class belongs to the release cut, and the hooks run formatters,
  sweeps, contract gates and linters. The suites are the wrong shape for a
  ten-second tier -- the short suite of `pkg/store` alone measured 53 s,
  because several hundred of its tests each cost between 50 and 200 ms in
  fixture setup.
- **Cheap:** format touched Go files and run the affected package tests, for
  example `go test ./internal/orchestrator -run RunAndAwait`. The `lint`,
  `test`, and `build` pipelines are focused checks when their whole boundary is
  relevant; invoke one with `sparkwing run <name>`.
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
  covers the suite, not the children it starts.
- **Go result caching:** ordinary `test` and `gate` checks preserve the
  caller's temporary-directory settings so Go can reuse passing results.
  Test fixtures own cleanup through `t.TempDir`, `t.Cleanup`, or deferred
  removal. Forced race and Postgres runs retain their per-run scratch roots.
- **Heavy packages:** run these by name rather than reaching for `./...`, and
  give the command a timeout the package actually fits in. Measured alone on an
  idle 16-core Linux box, `GOWORK=off go test -count=1 <pkg>`:

  | Package | Alone | Suggested `-timeout` |
  | --- | --- | --- |
  | `./cmd/sparkwing/...` | 174s | 10m |
  | `./internal/orchestrator` | 121s | 10m |
  | `./internal/cache` | 6s | 2m |
  | `./internal/cluster` | 4s | 2m |

  Under contention from parallel agents the two heavy packages have been
  observed at two to four times those figures, so the suggested budgets sit
  well above the measurement.

- **Normal broad check:** `sparkwing run gate` covers every committed Go
  module, the dashboard TypeScript unit, full ESLint, production build,
  and browser smoke suites, formatting, vet, build, tests, documentation
  mirrors, and source policy. It also runs `go test -race` on the packages
  whose Go files changed (staged, or since origin/main when nothing is
  staged), so a change never reaches main without the race detector having
  seen its own package, and runs `store-postgres` when that change touches
  `pkg/store`. Unit and ESLint run in parallel; the production build then
  feeds the browser suite.
- **Gating beside other agents:** run independent checks concurrently in separate
  development worktrees when CPU and memory headroom allow. Use `sparkwing run`
  for declared pipelines so admission can account for their reservations;
  focused commands outside a run also consume host resources. Coordinate tests
  that share mutable services, same-repository landings, and production cutovers.
  There is no blanket single-gate lock. `sparkwing queue list` shows running and
  queued work; keep each pipeline's required checks and budgets in force.
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
  command's state and config at DIR, which is deliberate isolation for work
  that must not touch the operational runs store, the release preview under
  Decisions before landing being the case that needs it. A run started that way
  is arbitrated by whatever daemon lives in that home rather than the machine's,
  so it is invisible to `sparkwing queue` and the dashboard and contends with
  every other run on the OS. It is not a way around a full queue.
- **Lint rules:** golangci-lint judges only code new since origin/main. Among
  the family set it also rejects `_ = call()` on an error-returning call, nil
  returned after an error was observed, and work started on a context that is
  not the caller's. Drop an error only through a helper that logs why.
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
  the tree, which is how to size the remaining purge.
- **Expensive or release-boundary:** `sparkwing run pre-release` adds race, chaos,
  vulnerability, dependency-freshness, API, and Terraform gates. Use
  `integration`, `template-verify`, `static-analysis`, and image builds only when
  the change touches those boundaries. `sparkwing run security-scan` runs gosec,
  source-mode govulncheck, gitleaks, and `npm audit`. The Security workflow runs
  it on every pull request and uploads gosec findings to code scanning. The
  release workflow runs neither it nor CodeQL against the tagged commit; the
  run that covered that commit on main is the scan of record. CodeQL reports
  alerts; gosec, govulncheck,
  gitleaks, and `npm audit` fail the gate. The npm scanner retries a registry
  that times out or answers 5xx, and reuses a recorded pass for a day when
  `web/package-lock.json` and `web/package.json` are byte-identical to the pass,
  so an unreachable registry fails as its own error rather than as an advisory
  and an unchanged dependency set is still re-asked daily. Run the local
  pipeline when a change touches an HTTP handler, auth, file paths built from
  input, subprocess arguments, or a dependency. Verify dashboard changes
  against real local state with `bash bin/dev-start.sh` (dashboard backend on :4343, `next dev` on :3100)
  and stop it with `bash bin/dev-stop.sh`; the browser gate uses deterministic
  API fixtures on OS-assigned local ports and does not replace that product
  exercise or exercise Kubernetes.
- **Template verification proofs:** `template-verify` scaffolds, builds, lints,
  and explains all 37 registry templates, and runs the runnable ones. A local
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
  the toolchain appears. Reuse never changes the plan: every template keeps its
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

- **Store dialect matrix:** `sparkwing run store-postgres` runs
  `go test ./pkg/store/...` with `SPARKWING_TEST_STORE=postgres`, so every
  store test that opens through `pkg/store/storetest` exercises the Postgres
  dialect instead of a SQLite file. It reuses `SPARKWING_TEST_PG_URL` when
  that is set and otherwise starts an embedded Postgres on a free port (data
  under a temporary root, binaries cached in `$TMPDIR`), then stops it and
  removes the data directory whether the suite passes, fails, or is
  interrupted; a run killed outright before its teardown finishes can leave a
  `sparkwing-store-postgres-*` directory under `TMPDIR`. No Docker. A failing suite prints the tail of the server log,
  which embedded-postgres only makes available once the server has stopped.
  A server that will not start is retried once on a fresh port and then
  fails the step. `pre-release` runs it after the race gate, under a
  thirty-minute timeout. Roughly 80 seconds on a warm cache and an idle box;
  the first run downloads the Postgres binaries.

- **Postgres conformance:** the store, backend, and orchestrator Postgres
  suites skip when `SPARKWING_TEST_PG_URL` is unset, and fail when it is
  set to a database they cannot reach. Start one with `docker run --rm -d
  --name sw-pg -e POSTGRES_PASSWORD=postgres -p 5433:5432 postgres:17`,
  then run `SPARKWING_REQUIRE_PG=1
  SPARKWING_TEST_PG_URL=postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable
  go test -v -run 'Postgres|Pg' ./pkg/store ./internal/backend
  ./internal/orchestrator`. `SPARKWING_REQUIRE_PG` turns an unset URL and
  `SPARKWING_INTEGRATION_DISABLE=1` into failures. That is not proof on
  its own, so both lanes that promise Postgres coverage run those suites
  verbosely and fail on any `--- SKIP` line: the hosted `Postgres
  conformance` job in `.github/workflows/ci.yaml` on every pull request
  and push to main, and `sparkwing run integration` against its
  Dockerized Postgres and MinIO.
- **Kubernetes product path:** `sparkwing run k8s-e2e` proves authenticated
  webhook intake, runner execution, logs, cancellation, retry, restarts, and
  retained state against an explicit Kubernetes context and caller-supplied
  image set. It requires an exact namespace/release cleanup allow-list and
  never creates or deletes cluster infrastructure. This check is manual and
  opt-in because it uses a real cluster.

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
  `CHANGELOG.md` and the mirror `pkg/docs` embeds resolve themselves instead:
  `.gitattributes` marks both `merge=union`, so a merge of two branches that
  each added `[Unreleased]` bullets keeps both sides with no conflict. Union
  merge gets two cases wrong. Two branches that reword the same bullet produce
  both wordings, and two branches that each rename `[Unreleased]` to their own
  version stack both headings on adjacent lines -- `bin/check-changelog.sh`
  fails on the stacked pair, and the reworded pair needs a reader. Where both
  sides opened the same `###` heading, `bash bin/check-changelog.sh --fix`
  collapses it into one block and re-syncs the mirror.
- **Changelog:** notable adopter-facing behavior belongs in `[Unreleased]` and
  follows `docs/changelog-style.md`. Mark breaking changes and supply migration
  guidance before release. Keep the embedded changelog mirror byte-identical.
- **Tests:** record the focused checks selected, or why execution was waived.
  Do not run every race, Docker, or integration suite by default.
- **Release:** merging is not a release; a release is a tag push. The local
  `release` pipeline checks the chosen version against origin tags, checks the
  clean tree, and verifies published module freshness, coherent SDK pins and
  changelog links in the fixed five-minute build/lint/short-test class. It then
  renames `[Unreleased]`, rolls the migration guide, validates the resulting
  section and guide before committing, and checks schema/wire change coverage.
  All refusals precede push-tag. Freshness checks published versions, not the
  position of local replacements against origin/main, so a cut may use an older
  commit. Preview with
  `SPARKWING_HOME="$(mktemp -d)" sparkwing run release --sw-dry-run`, then
  `SPARKWING_HOME="$(mktemp -d)" sparkwing run release --bump patch --sw-allow
  destructive,prod`; the isolated home keeps prerelease state out of the
  operational runs store, which the release runner refuses to touch. From the
  tag push on, `.github/workflows/release.yaml` owns the release, and it checks
  nothing: it resolves the tag to a commit, builds the binaries and images,
  signs and publishes them, and creates the GitHub release. `git tag vX.Y.Z &&
  git push origin vX.Y.Z` from any commit therefore publishes a release. The
  notes come from that tag's changelog section, and fall back to the annotated
  tag message and then to a pointer at CHANGELOG.md when the tagged source has
  no section. State-dependent source checks belong in the local cut, before
  the tag exists. Handle refusals and failed builds through the
  [release failure policy](#release-failure-policy).
- **Independent verification:** for user-facing local-execution changes, build
  the intended revision with `SKIP_WEB_BUILD=1 bash bin/install.sh` when the web
  bundle is unchanged, then exercise the installed CLI and daemon. To exercise a
  branch against real runs without replacing the binary every other repo and
  timer resolve, install it under its own name beside the real one:
  `SPARKWING_INSTALL_NAME=sparkwing-crons bash bin/install.sh` writes
  `~/.local/bin/sparkwing-crons`, which shares the home and store; an additive
  store migration keeps the older `sparkwing` working on the same database. Verify SDK,
  templates, integrations, browser behavior, or release assets when those
  surfaces changed.

## Release failure policy

Main may be red. If a change breaks it and the fix is not obvious, revert that
change first. Fix local preflight refusals on main, rerun the affected checks
after the correction, and retry the cut. Fix a failed hosted build forward on
main and publish a later patch tag. Never re-cut or move a published release tag.

Do not rerun an unchanged failing check merely to obtain green. A demonstrated
environment or tool setup correction can justify rerunning the same source.
Retain the original failure and correction as evidence.
