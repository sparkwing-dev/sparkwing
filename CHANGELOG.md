# Changelog

All notable changes to **sparkwing-sdk** are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Docs
- **Surface dowing as the chore-tier sibling from inside sparkwing's
  CLI (IMP-012).** `sparkwing info` now prints a SEE ALSO block under
  NEXT STEPS pointing to dowing (https://github.com/koreyGambill/dowing)
  for repo-local bash chores -- formatters, port-forwards, and other
  single-script tasks where sparkwing's compile cycle / DAG / run
  records are over-tax. The same pointer lands at the bottom of the
  `sparkwing info --first-time` onboarding card and as a "See also"
  paragraph in `sparkwing pipeline new --help`. Pure CLI-text change;
  no behavior or flag surface added. Sparkwing remains Go-pipeline-only
  -- the bridge fixes a discoverability gap, not a capability one.

### Added
- **Built-in `--start-at` / `--stop-at` wing flags for resume-from-step
  (IMP-007).** Every pipeline now gets per-step resume support without
  the author writing a stepOrder slice + skipBefore predicate. Flags
  are wing-level (parsed in `cmd/sparkwing/wing_flags.go`, forwarded
  via `SPARKWING_START_AT` / `SPARKWING_STOP_AT` env to the pipeline
  binary on the local-exec path; via `Trigger.Env` on the `--on`
  remote-dispatch path so behavior is identical across venues), so
  `wing cluster-up --start-at install-argocd` skips every WorkStep
  upstream of `install-argocd` in the same Job's Work and resumes
  from there. Window is inclusive on both bounds; `--start-at X
  --stop-at X` runs only X. Set semantics — descendants(start) ∩
  ancestors(stop) plus the bounds — make parallel branches
  parallelism-aware: `--start-at end` on a diamond DAG skips both
  sides. Unknown ids fail the run before any step dispatches with
  the same Levenshtein-suggesting message format IMP-008 introduced
  ("`--start-at "fetchh"` references unknown step "fetchh"; did
  you mean "fetch"? available steps: compile, fetch"). User-authored
  `SkipIf` predicates still apply on top of the range filter (a
  step the range would run can still be skipped by the user's
  predicate). Range-skipped items emit `step_skipped` events with
  `Attrs.reason` carrying the operator-readable bound, so renderers
  can distinguish operator intent from author intent. New SDK
  surface: `sparkwing.WithStepRange(ctx, startAt, stopAt)`,
  `sparkwing.StepRangeFromContext(ctx)`,
  `sparkwing.ValidateStepRange(plan, startAt, stopAt)`,
  `Work.TopologicalStepOrder()`. Pinned by 11 unit tests in
  `sparkwing/step_range_test.go` (linear / branching / inclusive
  endpoints / SkipIf interaction / no-op contracts / topo
  stability) plus 2 integration tests in
  `orchestrator/step_range_integration_test.go` (full Options ->
  validation -> ctx install -> RunWork skip slice; unknown bound
  fails the run BEFORE any step dispatches). Caveat documented in
  the flag help: if an upstream step populates in-memory state for
  downstream ones, `--start-at` past it leaves state empty —
  mitigation pattern is "lazy-load inside the step body if state
  isn't populated."
- **Plan-time validation of string-typed step references (IMP-008).**
  After a pipeline's `Plan(ctx, plan, in, rc)` returns successfully,
  the SDK now walks every Node and every materialized Work and
  panics with a "did you mean X?" suggestion (Levenshtein-driven)
  if a `Needs("step-id")` string doesn't resolve to a registered
  step, spawn, or plan node. Mirrors SDK-002's panic-with-pointer
  pattern: typo'd `Needs("install-karpentr")` calls used to silently
  make the dependency edge a no-op so the step would run
  immediately instead of after its intended predecessor; now they
  fail at pipeline registration / `sparkwing pipeline explain` time
  with a message that names the offending call site (`node "build"
  WorkStep "compile" .Needs("fetchh")`) and either suggests the
  closest match or lists available IDs when no match is close
  enough. Handle-typed `Needs(*WorkStep)` / `Needs(*Node)` paths
  are unaffected — the validation only fires when the resolved ID
  doesn't match the registry, which can only happen via the string
  overload. Levenshtein implemented inline (~25 LOC, two-row DP) to
  avoid a new module dep. SkipIf predicate closures are
  intentionally out of scope (their bodies can't be introspected
  without ast walking; tracked separately).

### Fixed
- **`git push && wing X --on prod` no longer races the gitcache's
  background fetch loop (IMP-005).** Two-part fix for the cryptic
  `fatal: remote error: upload-pack: not our ref <sha>` operators
  used to hit when dispatching against a SHA the cache hadn't
  mirrored yet (the cache fetches every ~30s, so dispatching within
  ~25s of a push raced). (1) `cmd/sparkwing` `dispatchRemote` now
  POSTs `/git/refresh?repo=<url>` to the profile's gitcache before
  creating the trigger, with a 5s timeout — best-effort, falls
  through with a warning if the cache is unreachable so dispatch
  itself never blocks on it. New endpoint in `cmd/sparkwing-cache`
  runs a synchronous `git fetch --prune origin '+refs/heads/*:refs/heads/*'`
  on the named bare mirror under the existing per-repo lock so a
  webhook burst doesn't fan out N concurrent fetches. (2) the
  warm-runner's source fetch in `internal/cluster/trigger_loop.go`
  now retries `bincache.FetchPipelineSource` up to 3 times spaced
  10s apart, but only on substring match `"not our ref"` — auth /
  network / malformed-URL errors fail fast (we never delay an
  obviously-broken state by 30s). On exhausted retries the operator
  sees `SHA <sha> not yet in gitcache after 3 attempts; the
  background fetch may not have completed since the push` instead of
  the raw upload-pack message, and `errors.Is` against the original
  fetch error still works. The retry attempt count and delay are
  package-level vars so tests shrink them; the substring marker is
  documented at the call site because git's wording could change.
  Pinned by 4 unit tests in `internal/cluster/fetch_retry_test.go`
  (recovery / exhaust+rewrite / fail-fast on unrelated err / ctx
  cancel) plus 4 HTTP-level tests in
  `cmd/sparkwing-cache/refresh_test.go` (real bare-repo round-trip /
  missing args / uncached repo / GET rejected).
- **Pipeline `flag:"..."` tags that collide with wing-owned flags
  now panic at `Register` time with a clear, full-context message
  (IMP-003).** Previously, declaring an Args field like
  `From string \`flag:"from"\`` would silently lose the value to
  wing's own `--from REF` parsing and surface as a confusing
  downstream error (`git worktree add invalid reference: ...`).
  `sparkwing.Register[T]` now walks the resolved Inputs schema and
  panics if any flag name appears in the wing-reserved set
  (`from`, `on`, `config`, `retry-of`, `full`, `no-update`,
  `verbose`/`v`, `secrets`, `mode`, `workers`, `change-directory`/`C`,
  `start-at`, `stop-at`). The panic body names the pipeline, the Go
  field, the colliding flag, and the full reserved list so authors
  don't have to re-discover the contract elsewhere. New SDK
  surface: `sparkwing.ReservedFlagNames()` returns the canonical
  sorted list — single source of truth shared with
  `cmd/sparkwing/wing_flags.go` so the two parser surfaces can never
  drift. `flag:",extra"` bag fields are exempt (they accept
  arbitrary keys at runtime; wing-owned flags are stripped before
  the bag sees them).
- **`sparkwing run -C <path> <pipeline>` no longer treats `-C` as
  the pipeline name (IMP-006).** The hand-rolled wing-flag parser
  used to take `args[0]` as the pipeline-name positional
  unconditionally, so `sparkwing run -C /path lint --on prod`
  silently dispatched a pipeline literally named
  `--change-directory` against the *current* repo. The run-verb
  now finds the pipeline-name positional via a strict-ordering
  scan that consumes wing flag-VALUE pairs first; `-C /path` is
  recognized and stripped before positional resolution. The strict
  rule is narrow on purpose — `-C` / `--change-directory` are the
  only flags that must appear before the positional, since they're
  the ones whose value (a path) is most easily mistaken for the
  pipeline name. Other wing flags (`--on`, `--from`, `--start-at`,
  ...) keep working from any position so existing
  `wing build --on prod` muscle memory is unaffected. The previously
  silent `wing run foo -C /path` shape now errors with
  `ambiguous flag position: -C must precede the pipeline name "foo"`.
  `--` is honored as a hard break: anything after `--` is opaque
  pipeline-side, so `wing run foo -- --my-pipeline-flag value`
  parses cleanly even if a future wing flag shares the name.
- **`sparkwing.MustConfig` for parity with `MustSecret` (SDK-036).**
  `MustSecret(ctx, name)` has long had a panic-on-error shortcut for
  call sites where a missing secret is genuinely a programmer
  mistake; the sibling `Config` had no equivalent. `MustConfig` fills
  the asymmetry — same shape, same trade-off (prefer the
  error-returning form unless the value's absence is unrecoverable).
- **Public sparkwing repo gains a `.sparkwing/` pipeline tree (Phase 4
  of the release-pipeline restructure).** Five wing pipelines now live
  in this repo so the platform release-all (in the sibling
  sparkwing-platform repo) can cross-reference into them:
  - `wing lint` -- gofmt + go vet across the public module.
  - `wing test` -- `go test ./...` (race-clean variant stays in
    platform).
  - `wing static-analysis` -- staticcheck (via `go run` so dev
    machines without a global install still pass) plus
    `go mod tidy -diff`.
  - `wing build` -- sanity-builds every `cmd/*` binary on the host
    platform. Production multi-arch + container builds are still
    owned by `.github/workflows/release.yaml`, which fires on tag
    push.
  - `wing release [--version vX.Y.Z]` -- validates clean tree, free
    tag, and CHANGELOG entry, then pushes the tag. The GH-Actions
    release workflow takes over from there to publish binaries to
    GH Releases and multi-arch images to GHCR. Tag pushes are never
    force-pushed (semver invariant for Go module consumers); the
    pipeline hard-refuses a tag that already exists on origin.
  Cross-module helpers were re-implemented locally (the .sparkwing/
  tree is a separate Go module, so importing platform's release
  helpers wasn't an option). The tree is intentionally minimal:
  release-all orchestration, deploy, consumer bumps, and platform
  image publish all stay in sparkwing-platform/.sparkwing/.
- **OSS container images published to GHCR on every `v*` tag (ISS-052).**
  `.github/workflows/release.yaml` gains a `build-images` job that
  builds multi-arch (linux/amd64 + linux/arm64) images for the five
  cluster-side binaries -- `sparkwing-controller`, `sparkwing-runner`,
  `sparkwing-cache`, `sparkwing-logs`, `sparkwing-web` -- from a
  single parameterized `build/Dockerfile.binary`. Each tag push
  publishes `ghcr.io/sparkwing-dev/<binary>:vX.Y.Z`; stable
  (non-pre-release) tags additionally update `:latest`. Images are
  cosign-keyless-signed via GitHub OIDC, so consumers can verify
  provenance with `cosign verify` against the workflow + repo
  identity. Helm chart READMEs (`sparkwing-full`,
  `sparkwing-runner-bundle`) updated to drop the "images don't
  exist yet, build locally" caveat.

### Fixed
- **Runs no longer report `status: success` when `logs.append` is
  silently failing (IMP-002).** Previously, a runner whose token
  lacked the `logs.write` scope produced a series of per-line
  `WARN logs append dropped` entries in pod stderr while the
  orchestrator still finished the run as `success` -- so
  `sparkwing runs status --run <id>` showed a green checkmark and
  `sparkwing runs logs --run <id>` returned empty for the actual
  node body. The black-hole was undetectable until an operator
  went to inspect the run hours later. Three concrete behavioral
  changes:
  - `logs.Client.Append` now returns `*logs.AuthError` on 401/403,
    parsing the missing scope from the controller's response body
    so callers can distinguish auth misconfig (fatal) from
    transient transport flakes (retryable).
  - The orchestrator's `httpNodeLog` retries 5xx / network errors
    up to 3 times with exponential backoff (200ms / 400ms / 800ms);
    persistent failures increment a per-node drop counter and emit
    a `logs_drop` event on the run's event stream rather than
    being downgraded to per-line WARN.
  - 401/403 latches a sticky fatal so subsequent emits short-
    circuit (no more retry storms against a misconfigured token);
    `executeNode` checks the latched error after the user job body
    completes and forces the node to `failed` with
    `failure_reason = logs_auth` and `error: "logs append blocked:
    token lacks scope \"logs.write\" (HTTP 403)"`. The node-level
    error surfaces in `runs status`'s per-node table immediately
    so the operator sees the deploy-time misconfig instead of a
    silently-broken success.

  Pinned by `logs.TestAppend_403ReturnsAuthErrorWithScope`,
  `logs.TestAppend_401ReturnsAuthError`,
  `orchestrator.TestHTTPLogs_403HardFailsRun` (the full slice:
  pipeline run -> 403 from logs server -> Run.Status=failed +
  Node.FailureReason=logs_auth), and
  `orchestrator.TestHTTPLogs_5xxRetriesThenCountsDrop`
  (transient-failure retry budget + drop counter).
- **Warm-runner now ships `.sparkwing/` compile stderr into the
  run's structured logs (IMP-001).** Previously, when the trigger
  loop's `go build` of a consumer's `.sparkwing/` directory failed,
  the operator-facing surface was the wrapper string
  `compile .sparkwing/: exit status 1`; the actual toolchain output
  (e.g. `go: go.mod requires go >= 1.26.0`) only existed in the
  warm-runner pod's stderr and required a `kubectl logs` to see.
  `bincache.CompilePipeline` now returns a typed `*CompileError`
  whose `Output` field carries the captured `go build` stdout and
  stderr (both, since the Go toolchain splits diagnostics across
  them); the cluster trigger loop POSTs that buffer to a synthetic
  `_compile` log node on the logs service, so
  `sparkwing runs logs --run <id>` surfaces the real error
  alongside the regular per-node output. The wrapper error string
  on the failed-trigger path stays terse so runner pod logs don't
  balloon. Best-effort: the ship attempt uses a fresh
  `context.WithoutCancel` ctx so a parent ctx that's already
  cancelling on heartbeat-silence doesn't drop the diagnostic, and
  a failed POST degrades to a warning rather than masking the
  underlying compile failure.
- **`wing X --explain --skip Y -o json` now honors `--skip` / `--only`
  identically to the no-`-o` form (CLI-017).** The pipeline binary's
  `--explain` entrypoint forwards the user's full argv into
  `parseTypedFlags` to surface typed pipeline flags (including the
  `SkipFilterArgs` embed exposing `--skip` / `--only`). Wrapper-only
  output flags (`-o`, `--output`, `--json`) aren't part of the
  pipeline schema, so `parseTypedFlags` rejected them as unknown --
  and the silent error fallback dropped the *entire* parsed argsMap,
  including `--skip`. The Plan was then built without SkipFilter, so
  the JSON snapshot included nodes the human-readable form correctly
  omitted. `printPipelinePlan` now strips explain-output formatting
  flags before parseTypedFlags via a new `stripExplainOutputFlags`
  helper (in `orchestrator/main.go`); both render paths consume the
  same post-filter Plan. Pinned by
  `orchestrator/explain_skip_filter_test.go`, which exercises every
  shape of `-o`/`--output`/`--json` against a fixture pipeline that
  consults `Skip` to drop a named node.
- **Cluster-mode `RunWorker` no longer wires `Backends.Concurrency`
  through a local SQLite store (RUN-017).** `internal/cluster.RunWorker`
  -- the claim loop powering `sparkwing-runner worker` -- previously
  built `Backends.Concurrency` from a throwaway `LocalBackends` bundle,
  meaning the SQLite-direct `localConcurrency` (which embeds
  `*store.Store`) sat in the runner-process Backends graph. RUN-016
  pinned the same invariant for the `HandleClaimedTrigger` path; this
  closes the parallel hole on `RunWorker`. The fix mirrors
  `HandleClaimedTrigger`: `Backends.Concurrency` is now
  `*HTTPConcurrency` against the controller, so cache hits and slot
  coordination are shared cluster-wide AND no `*store.Store` is
  reachable from `.inline()` pipeline code running in the worker
  process. Pinned by a new `internal/cluster/worker_safety_test.go`
  parallel to `orchestrator/cluster_safety_test.go`. See
  `decisions/0001-open-core-tier-strategy.md` for the open-core trust-
  boundary rationale.
- **S3-only dashboard mode is fully functional (LOCAL-015).** With
  `--no-local-store`, the dashboard server now answers `/api/v1/runs`,
  `/api/v1/runs/{id}` (including `?include=nodes`), and
  `/api/v1/capabilities` from the configured `ArtifactStore`, with
  no controller mounted. Mutating routes (`/cancel`, `/paused`,
  `/release`) deliberately stay 404 -- there's no orchestrator to
  satisfy them, so a missing handler is an honest answer. Closes
  the controller-shaped hole left by LOCAL-011 and makes S3-only
  mode the canonical OSS free-tier read path described in
  `decisions/0001-open-core-tier-strategy.md`.

### Changed
- **`/api/v1/capabilities` is now always served by the dashboard server
  (LOCAL-015).** The controller's handler is gone; the dashboard
  answers in every topology (laptop SQLite, cluster controller-proxied,
  S3-only) by querying the configured `Backend.Capabilities()`.
  `Capabilities.Storage` gains a `runs` field (`"sqlite"` | `"s3"` |
  `"controller"`) so the SPA can identify the data source without
  inferring it from the mode label. Additive on the wire -- existing
  consumers keep working.

### Removed (BREAKING)
- **`Approval.OnTimeout` field renamed to `Approval.OnExpiry`
  (SDK-036).** The old name read as if it paralleled `Node.Timeout()`
  (per-attempt execution budget), but the two fields are unrelated
  concepts: `Approval.OnExpiry` decides how an unanswered gate
  resolves once its `Timeout` elapses; `Node.Timeout()` bounds how
  long a job step is allowed to run. Renamed the SDK-facing field to
  break the accidental association. The internal store / wire
  format (`store.Approval.OnTimeout`, `on_timeout` JSON field, DB
  column) is unchanged so approvals already in flight still resolve.
  Authors update by replacing `OnTimeout:` with `OnExpiry:` in
  `&sparkwing.Approval{...}` literals — type and constants
  (`ApprovalTimeoutPolicy`, `ApprovalFail` / `Deny` / `Approve`) are
  unchanged.
- **`sparkwing.JobNode` is gone; replaced by `sparkwing.NewDetachedNode`
  (SDK-035).** The detached-node primitive (used internally by
  `JobFanOutDynamic`, `Node.OnFailure`, and the orchestrator's
  `SpawnNode` dispatch path) was exported under the easily-confused
  name `JobNode` — one letter different from the public `Job` verb.
  Renamed to `NewDetachedNode` so it doesn't sit next to `Job` in
  godoc and to make the "not registered on a Plan" semantics explicit.
  The two in-package callsites (`combinator.go`, `OnFailure`) now use
  an unexported `newNode` helper directly. Pipeline authors should
  never have called `JobNode` (its godoc said so); any code that did
  is a one-line rename to `NewDetachedNode`. As a side effect, the
  detached-node paths now apply the same `Produces[T]` ↔
  `Work.SetResult` contract validation that `sparkwing.Job` already
  enforced, so a typo in an `OnFailure` recovery or fan-out child
  panics at Plan time instead of silently materializing a malformed
  node. Approval gates are now also routed correctly through these
  paths (previously `JobNode` skipped the approval branch).
- **Every remaining `/api/runs/*` route is gone (LOCAL-016).** The
  parallel surface that LOCAL-014 started removing is now fully
  retired; `/api/v1/*` is the only public dashboard contract. Logs
  live at `/api/v1/runs/{id}/logs[/{node}[/stream]]`, the
  structured event SSE at `/api/v1/runs/{id}/events/stream`, and
  run detail with embedded nodes via
  `/api/v1/runs/{id}?include=nodes` (default response stays the
  raw `store.Run` shape; the wrapped `{run, nodes}` shape is opt-in
  via `?include=nodes` so existing CLI / runner consumers see no
  change). Cancel + debug-pause routes are controller-owned at
  `/api/v1/runs/{id}/cancel`, `/api/v1/runs/{id}/paused` (alias of
  the existing `/debug-pauses` GET), and
  `/api/v1/runs/{id}/nodes/{nodeID}/release`. The dashboard server
  registers its `/api/v1/runs/{id}/{logs,events}` patterns ahead of
  the controller's `/api/v1/` catch-all; Go 1.22 ServeMux specificity
  routes them correctly. Pinned by a regression test in
  `pkg/localws/mux_specificity_test.go`.

### Changed
- **`internal/backend.Backend` trims to read-only (LOCAL-016).**
  `CancelRun`, `ListDebugPauses`, and `ReleaseDebugPause` are gone
  from the interface and from all three implementations
  (`StoreBackend`, `ClientBackend`, `S3Backend`). The dashboard SPA
  hits the controller directly for those state mutations now, so
  the dashboard server no longer needs a write surface.
- **Dashboard data access unified behind one `internal/backend.Backend`
  interface (LOCAL-017).** The web handlers used to consume two
  parallel interfaces (`web.Reader` for state, `web.LogSource` for
  logs) plus a third type (`web.S3Reader`) for the S3-only mode. Those
  three are now collapsed into `Backend`, with three implementations:
  `StoreBackend` (laptop SQLite + filesystem logs), `ClientBackend`
  (HTTP to controller + sparkwing-logs), and `S3Backend` (S3
  state.ndjson dumps + S3 log objects -- replaces `web.S3Reader`).
  Each impl owns its own log discovery, so the controller stays out
  of the log bandwidth path. Pure refactor: API surface unchanged.
  Sets up LOCAL-016 (migrating the rest of `/api/runs/*` onto
  `/api/v1/*`) and LOCAL-015 (S3-only `/api/v1/capabilities`),
  both of which want a single backend abstraction to dispatch on.

## [v1.5.4] - 2026-05-04

### Changed
- **Renderer choice now decided by the CLI, not the pipeline binary.**
  The `sparkwing` / `wing` wrapper now stamps `SPARKWING_LOG_FORMAT`
  (pretty | json) into the environment before exec'ing the compiled
  pipeline, based on its own `IsInteractiveStdout` check. The
  pipeline binary's `orchestrator.Main` already honored
  `SPARKWING_LOG_FORMAT` first and fell back to its own TTY auto-
  detect; the wrapper just always sets it now. **Practical effect:**
  TTY-detection fixes (Git Bash MSYSTEM, mintty, etc.) take effect
  on a CLI upgrade alone -- no longer requires bumping the SDK pin
  in `.sparkwing/go.mod`. Storage path is unchanged: persistent log
  records and the dashboard's `/api/runs/.../logs` endpoint still
  emit JSONL regardless of stdout renderer choice. ANSI stripping
  for agents (no TTY → json mode → no color) also unchanged.

### Fixed
- **Run hints (`status:` / `logs:` / `retry:`) restored after a
  `sparkwing run` finishes** -- v1.5.3 dropped them based on a
  misread of "we don't need the runs status thing." User actually
  wanted them gone from `pipeline new`'s tip block (kept dropped)
  and the first-time card's self-reference (kept dropped), but
  *kept* on every run-end. Reverted that part.

### Added
- **Structured `hints` attrs on `run_start` and `run_finish`
  LogRecords.** Carries the same next-question CLI commands the
  PrettyRenderer prints, but as a `map[string]string` in the record's
  Attrs. The JSONRenderer emits them inline so an agent reading the
  JSONL stream can discover `sparkwing runs logs --run X --follow`
  / `sparkwing runs status --run X` / `sparkwing runs retry --run X`
  without parsing colored terminal output. Single source of truth:
  pretty + JSON views can't drift on the hint set.
- **Bash tab completion suggests flags at a leaf without requiring
  `-` to be typed first.** Matches the macOS/Linux bash-completion
  package UX. On Git Bash on Windows previously: `sparkwing run
  release <TAB>` produced nothing because the user hadn't typed `-`;
  now the same TAB lists `--config`, `--on`, `--no-update`, etc.
  Filtering by the partial flag prefix still works as before.

### Changed
- **`go mod tidy` output is now captured rather than streamed,
  with a Braille spinner during the wait.** Cold module cache makes
  `go mod tidy` print 30+ "downloading X" lines that bury the next-
  step scaffold output. Now: capture stdout/stderr to a buffer, tick
  a spinner on stderr while running, dump the captured output only
  if tidy fails. Non-TTY consumers (CI, agents) see a single
  `==> resolving dependencies (...)` line and the eventual
  success/fail status, no spinner flicker.

## [v1.5.3] - 2026-05-04

### Fixed
- **`wing <pipeline> <TAB>` no longer keeps re-suggesting the
  pipeline name.** The bash completer always returned pipeline names
  regardless of cursor position, producing
  `wing release release release ...` on repeated tabs. Now only
  completes the pipeline at the first positional; past it, falls
  through to flag completion using the same `_complete-flags run`
  helper sparkwing uses.
- **Git Bash on Windows TTY detection is more robust.** v1.5.1's
  fallback only checked `MSYSTEM`, which some Git Bash setups don't
  set (e.g. invoking `bash.exe` directly via cmd.exe rather than via
  mintty). Now also accepts `TERM_PROGRAM=mintty` and `TERM`
  matching `xterm*` / `cygwin*` -- a wider net so `sparkwing run`
  picks the pretty renderer (not JSONL) in every realistic Git
  Bash / MSYS2 / Cygwin invocation, and `pkg/color` enables ANSI.

### Changed
- **First-time card TIPS section reformatted** to use the same
  two-column aligned layout as `sparkwing info`'s `NEXT STEPS` /
  `FOR AGENTS` sections (no leading bullets). New `DOCS` section
  added below TIPS so the onboarding card mirrors the steady-state
  `sparkwing info` shape. Dropped the self-referential
  `sparkwing info --first-time` tip (circular -- the user is
  reading the output of that command right now).
- **`pipeline new` adds a blank line above the
  `==> resolving dependencies` heading** so it visually separates
  from the file-creation list.
- **Dropped the redundant `sparkwing runs status --run X` hint**
  from the pretty renderer's run-start and run-finish blocks. The
  per-node status streams live during the run, and `sparkwing runs`
  / the run_summary table at the end already show it. Kept the
  `follow logs`, `logs`, and `retry` hints -- those answer
  non-obvious next-questions.

## [v1.5.2] - 2026-05-04

### Fixed
- **Release job in `release.yaml` now checks out the repo.** The
  v1.5.1 release attempt failed because the new matrix-parallel
  workflow split the build (each cell self-checks-out its own copy)
  from the release job (which doesn't compile anything), and the
  release job ran `gh release create --generate-notes` -- which
  shells out to git to walk commits since the previous tag. Without
  a checkout, that git invocation hits "fatal: not a git repository".
  Added `actions/checkout@v4` with `fetch-depth: 0` so the auto-
  generated release notes have the full tag history to walk.

## [v1.5.1] - 2026-05-04

### Fixed
- **Bash tab completion no longer requires the `bash-completion`
  package.** The generated script previously called `_init_completion`
  and `_get_comp_words_by_ref`, which are defined by `bash-completion`
  -- shipped on most Linux distros but **not on Git Bash on Windows**,
  so windows users hit `_init_completion: command not found` the moment
  they sourced the completion. Replaced both with native bash
  (`COMP_WORDS` / `COMP_CWORD`) accessors covering the same surface.
  Works in any bash 4+, no external package needed.
- **`sparkwing run` now picks the pretty renderer in Git Bash on
  Windows.** Was wrongly emitting JSONL because Go's
  `term.IsTerminal` returns false there -- Git Bash / MSYS2 / Cygwin
  use mintty over a pipe to the underlying process, so the Windows
  Console-mode probe fails even at a real interactive shell.
  `selectLocalRenderer` now also accepts `MSYSTEM` being set
  (Git Bash sets `MINGW64`, MSYS2 sets `MSYS`, etc.) as a TTY
  signal. Same fallback added to `pkg/color.detectEnabled` (so
  colors come on too) via the new shared `color.IsInteractiveStdout`
  helper, and to `cmd/sparkwing` `jobs logs` format auto-detection.
  Three call sites, one definition -- can't drift again.

### Changed
- **Release workflow now builds in matrix-parallel.** Each
  (binary × goos × goarch) cell runs on its own runner, then a
  `release` job downloads every artifact and uploads in one shot.
  Wall-clock drops from ~8 min sequential to ~1-2 min parallel.
  `fail-fast: false` so a single cell failure doesn't cancel
  siblings; the release job's `needs: build` gate refuses to
  publish on partial results.
- **Server binaries no longer ship Windows assets.** The 8 unused
  `sparkwing-{controller,runner,logs,web}-windows-{amd64,arm64}.exe`
  binaries are dropped from the matrix via the `exclude:` block
  (in addition to the existing sparkwing-cache windows skip). These
  are server-side components meant for Linux containers; nobody
  runs them on a Windows host. Asset count: 40 -> 32. CLI binaries
  (`sparkwing`, `sparkwing-local-ws`) still ship for all 6
  platforms.

## [v1.5.0] - 2026-05-04

### Removed (BREAKING)
- **`GET /api/runs` (bare run-list endpoint) is gone.** The dashboard
  pod's legacy duplicate of the run-list path has been deleted.
  `GET /api/v1/runs` is now the only canonical path for listing
  runs; it accepts the same query params (`pipeline`, `status`,
  `since`, `limit`) and returns the raw `store.Run` JSON shape --
  `trigger_source` instead of `trigger`, no server-computed
  `duration_ms` (compute from `started_at` / `finished_at`), no
  pipeline `tags` (fetch separately via `/api/v1/pipelines` if
  needed). The remaining `/api/runs/<id>*` sub-routes (detail, logs,
  events, cancel, debug pause) are unchanged for now and migrate in a
  follow-up. Filter parsing for both the laptop and cluster
  controllers now flows through the shared `store.ParseRunFilter`
  helper so the two list paths can't drift. Part of LOCAL-014, the
  first ticket landing under the `/api/v1/*`-canonical principle from
  decisions/0001.

### Added
- **`sparkwing-controller` is now part of this repo.** The
  single-tenant cluster orchestrator -- previously closed-source --
  ships from `cmd/sparkwing-controller/` and `pkg/controller/`,
  source-available under the [Elastic License v2](LICENSE). External
  users can read, modify, and self-host the controller as part of a
  full sparkwing cluster deployment (no managed/hosted resale).
  Sparkwing's hosted multi-tenant build remains a separate, private
  composition layered on top of this same orchestration core; this
  repo is the authoritative source for both. Design decisions logged:
  open-core tier strategy and ELv2 licensing rationale (ORG-002).

### Fixed
- **`sparkwing run` works on Windows.** `bincache.ExecReplace`
  previously called `syscall.Exec` unconditionally, which Go's
  Windows runtime rejects with "not supported by windows" -- so
  every pipeline run on Windows died at the dispatch step. Added a
  Windows fallback that fork+execs the compiled pipeline binary as a
  foreground subprocess and propagates its exit code via `os.Exit`.
  POSIX path (`syscall.Exec` for true process replacement) is
  unchanged. Tradeoff: the wrapper `sparkwing.exe` stays alive for
  the child's lifetime on Windows, vs. POSIX where it's gone before
  the child writes its first line. Acceptable for short-lived
  pipeline runs; long-running `wing worker` may want different
  signal handling later.
- **`sparkwing run` recovers from missing go.sum entries.** First
  run after `pipeline new` would fail with `go build`'s "missing
  go.sum entry for module providing package X" if the post-scaffold
  `go mod tidy` was skipped or partial. The compile path now detects
  this specific error (sentinel `bincache.ErrMissingGoSum`,
  classified by capturing + matching `go build`'s stderr), runs
  `go mod download` to populate `go.sum` without modifying
  `go.mod`, and retries the compile once. Other compile failures
  (syntax errors, unresolved imports) bubble up as before.
- **`sparkwing-cache` no longer included in Windows release assets**
  because it uses POSIX-only `syscall.Setpgid` and `syscall.Kill`
  for git subprocess process-group management. Server-side
  component meant for Linux containers; the Windows skip is
  principled. Other server binaries (controller, runner, logs, web,
  local-ws) cross-compile cleanly and continue to ship for all 6
  platforms.

### Changed
- **`pipeline new` shows live progress for `go mod tidy`.** The
  post-scaffold dependency resolution (which can take 10-30s on a
  cold module cache) used to run silently with `CombinedOutput`,
  reading as a hang. Now prints an `==>` heading before the run and
  pipes Go's download progress straight to stderr so the user sees
  what's being waited on. Final success/fail line is unchanged.
- **`sparkwing run`'s "compiling pipeline binary" announcement is
  more honest about timing.** Previously promised "one-time, ~5-10s",
  but that ignores `go build`'s implicit dep download phase on a
  cold module cache (can be 30s+). New text: "compiling .sparkwing/
  pipeline binary (first time on this machine; may download deps)".

## [v1.4.2] - 2026-05-04

### Fixed
- **`wing` alias on Windows now dispatches as `sparkwing run`** (it
  was behaving as plain `sparkwing` because `os.Args[0]` reads
  `wing.exe` on Windows but `main.go` only matched the bare `wing`
  base name). Strip the `.exe` suffix before the dispatch check so
  POSIX and Windows behave identically.

### Changed
- **`sparkwing info` and `sparkwing info --first-time` use ALL CAPS
  section headers** (`PREREQUISITES`, `NEXT STEPS`, `TIPS`, `ABOUT`,
  `ENVIRONMENT`, `FOR AGENTS`, `DOCS`) to match the convention used
  everywhere else in `--help` output (`USAGE`, `COMMANDS`, `OPTIONS`).
- **First-time card prerequisites are now per-bullet conditional.**
  The `PREREQUISITES` section only appears when at least one check
  fails, and each missing dependency renders its own bullet
  (Go-on-PATH, sparkwing-on-PATH). A fully-set-up machine sees the
  section disappear; a fresh install sees the exact fix it needs.

## [v1.4.1] - 2026-05-04

### Fixed
- **`sparkwing update` now refreshes `wing.exe` alongside
  `sparkwing.exe` on Windows.** Because `wing.exe` is installed as a
  copy (not a symlink -- see v1.4.0 notes), it was previously left
  pointing at the old binary after a self-update. The update flow
  now rename-asides `wing.exe` next to the just-replaced
  `sparkwing.exe`. Best-effort: a missing or locked `wing.exe` won't
  fail the whole update -- `sparkwing.exe` is the canonical entry
  point. POSIX is unchanged: `wing` is a symlink, so it tracks
  automatically.

## [v1.4.0] - 2026-05-04

### Added
- **Windows binaries (`sparkwing-windows-amd64.exe`,
  `sparkwing-windows-arm64.exe`) now ship on every release.**
  `release.yaml` cross-compiles the windows pair alongside the
  existing 4 (linux/darwin × amd64/arm64) and uploads them to GH
  Releases under the same SHA256SUMS manifest. Closes the
  "Windows is not yet covered by prebuilt binaries" gap that
  install.sh used to bail on.
- **`install.sh` (sparkwing-product) handles windows.** Detects
  Git Bash via `uname -s` (`mingw*|msys*|cygwin*`), downloads
  `sparkwing-windows-<arch>.exe`, installs it to `~/.local/bin`,
  and lays down a `wing.exe` copy alongside it (a real copy
  rather than a symlink: cmd.exe/PowerShell can't see
  extensionless names per PATHEXT, and MSYS `ln -s` needs
  Developer Mode). Invokable from cmd.exe, PowerShell, and
  Git Bash without privilege.

### Fixed
- **`sparkwing update` now actually works.** Previously it tried
  to fetch `sparkwing.dev/releases/<v>/sparkwing-<os>-<arch>.{tar.gz|zip}`,
  which 404'd for two reasons: the website doesn't proxy GH
  Releases assets, and the workflow uploads bare binaries (not
  archives). `update` now hits
  `https://github.com/sparkwing-dev/sparkwing/releases/download/<v>/<asset>`
  directly (matching install.sh) and consumes the bare per-platform
  binary -- no extraction step. SHA256 verification against the
  published SHA256SUMS still runs; macOS ad-hoc codesign and the
  windows rename-aside dance are unchanged. `archive/tar`,
  `archive/zip`, and `compress/gzip` imports dropped along with
  ~80 lines of extraction code.
- **`orchestrator/paths.go sanitizeNodeFile` now scrubs the full
  NTFS-reserved set** (`/ \ : * ? " < > |`), not just `/`. Node
  IDs containing colons (e.g. timestamp suffixes) or any other
  reserved char no longer fail log-file creation on Windows. Done
  unconditionally so a run created on Linux and copied to a Windows
  host has identical log filenames -- cross-OS log inspection in
  `~/.sparkwing/runs/` Just Works.
- **`sparkwing dashboard start` example renders per-OS.** The
  `--home /tmp/sparkwing-x` example in `--help` now resolves to
  `%TEMP%\sparkwing-x` on Windows (env-var form, copy-paste-friendly
  in cmd.exe) and stays `/tmp/sparkwing-x` elsewhere. Other help
  text continues to use `~/.sparkwing` and `$SPARKWING_HOME` --
  universally readable shell-agnostic notation.

## [v0.6.4] - 2026-05-04

### Changed
- **Dashboard log panel renders JSONL prettily by default**
  (`/api/runs/<id>/logs`, `/api/runs/<id>/logs/<node>`, and the SSE
  `/stream` variant). Backed by `orchestrator.PrettyRenderer`, color
  off, child-process ANSI passed through. Storage is unchanged --
  `LogStore` still writes one `sparkwing.LogRecord` per line. Clients
  that want the structured envelope (`curl`, log shippers) opt in
  with `Accept: application/x-ndjson`. Non-JSON lines pass through
  verbatim instead of crashing the renderer. ISS-043.
- **Dashboard log endpoint now does three-way Accept negotiation**
  (ISS-044). Default `text/plain` is the safe-by-default path:
  pretty pretext with `Msg` ANSI **stripped** so `curl`/agent
  consumers piping into TTY-naive tools don't see escape garbage.
  `Accept: text/x-ansi` opts into the colored variant (renderer
  SGR on, child-process ANSI passthrough) for the SPA's log panel
  once it's wired through an ANSI-to-HTML parser.
  `Accept: application/x-ndjson` is unchanged. A `?format=raw|ansi|plain`
  query param mirrors the Accept tiers for browser-direct testing.
  Applies to bulk + SSE.

### Added
- `orchestrator.StripANSI(string) string` -- exported from
  `orchestrator/logger.go`, was previously unexported. Reused by
  `internal/web` for the plain-mode log render path. ISS-044.

### Added
- `internal/web.NewStoreReader(*store.Store) Reader` -- canonical
  adapter for the `Reader` interface. Replaces the duplicated
  unexported `storeReader` previously copied into `pkg/localws`.
  LOCAL-012.
- `orchestrator.DumpRunState` (formerly unexported) so the
  `state.ndjson <-> store.Run` round-trip test can pin the dump
  format against the `S3Reader` read path. New test in
  `orchestrator/dumpstate_test.go` fails if a future Run/Node field
  is added without a JSON tag (or with `json:"-"`) and silently
  dropped from the dashboard's S3 view. LOCAL-013.
- **Dashboard S3-only mode** (`sparkwing dashboard start
  --no-local-store`): list runs straight from an artifact-store
  without opening SQLite. A fresh laptop pointed at a CI bucket can
  now `sparkwing dashboard start --on ci-smoke --no-local-store
  --read-only` and see every `runs/<id>/state.ndjson` dump GHA
  workflows have written -- no ingest step. Requires `--log-store`
  and `--artifact-store` (or an `--on` profile that supplies them).
  The controller (`local.New`) is not mounted in this mode; write
  endpoints are absent because there's no orchestrator on the laptop
  to satisfy them. LOCAL-011.
- `storage.ArtifactStore.List(ctx, prefix) ([]string, error)` --
  enumerate keys under a prefix. Implemented for `pkg/storage/fs`
  (filepath.WalkDir) and `pkg/storage/s3` (paginated ListObjectsV2).
  `pkg/storage/sparkwingcache` returns `storage.ErrListNotSupported`
  -- the cache server has no list endpoint.
- `internal/web.S3Reader`: parses `runs/<id>/state.ndjson` into the
  same `*store.Run` / `*store.Node` shapes the SQLite reader returns,
  so `/api/runs` handlers stay backend-agnostic. State files are
  immutable so a tiny in-process cache fronts the artifact-store
  reads.

## [v0.6.3] - 2026-05-03

### Added
- **Go-toolchain pre-flight checks** on the three commands that
  shell out to `go`. Previously a user without Go on PATH hit a
  raw `exec: "go": executable file not found in $PATH` mid-scaffold,
  which read like a sparkwing bug.
  - `sparkwing info --first-time` shows a Prerequisite block at the
    top with an OS-tuned install hint when Go is missing.
  - `sparkwing pipeline new` already had a toolchain alert from
    `printInitReport`; the wrapping scaffolder now also prints a
    "warning: scaffolding will succeed but `sparkwing run` will
    fail until Go is installed" line so the operator can't miss it.
  - `bincache.CompilePipeline` (called by `sparkwing run` on a
    cache miss) returns "go toolchain not on PATH: ... Install Go
    1.26+ from https://go.dev/dl/" instead of letting the exec
    failure bubble up raw.

## [v0.6.2] - 2026-05-03

### Fixed
- **`sparkwing version`** previously checked
  `https://sparkwing.dev/releases/latest` (a marketing-site pointer
  that drifted post-rename). Now follows the GitHub redirect at
  `github.com/sparkwing-dev/sparkwing/releases/latest` instead --
  single source of truth, no API token needed.
- Sweeping doc + test cleanup of stale `github.com/koreyGambill/*`
  references that predated ORG-001. `koreyGambill/sparks-core`
  resolves nowhere; canonical home is `sparkwing-dev/sparks-core`.
- `version.go` SparkPin module-prefix matcher updated to
  `github.com/sparkwing-dev/sparks-` (was `koreyGambill/sparks-`,
  silently never matching).
- `getting-started.md` had a stale SDK import path
  `sparkwing-platform/pkg/sparkwing`; the canonical SDK import is
  `github.com/sparkwing-dev/sparkwing/sparkwing`.

### Added
- `.github/workflows/release.yaml`: cross-compiles + uploads
  sparkwing CLI binaries (linux/amd64, linux/arm64, darwin/amd64,
  darwin/arm64) plus a `SHA256SUMS` manifest on every `v*` tag push.
  Replaces the manual `gh release upload` scripts. LOCAL-009.

## [v0.6.1] - 2026-05-03

### Fixed
- **`sparkwing pipeline new` no longer scaffolds a broken go.mod.**
  `fallbackSDKVersion` was pinned to `v0.0.1`, which predates the
  ORG-001 module rename and still declares its module path as
  `github.com/sparkwing-dev/sparkwing-sdk`. `go mod tidy` against a
  freshly-scaffolded project failed immediately with a path-mismatch
  error. Bumped to `v1.3.1`. The `go` directive fallback (used when
  Go isn't on PATH) bumped from `1.22` to `1.26` to match the SDK's
  current minimum.
- **`docs/getting-started.md`** install instructions: replaced the
  fictional `https://sparkwing.dev/install.sh` with the real GH
  Releases curl URLs (one per platform), and corrected the
  `go install` path from `sparkwing-platform/cmd/sparkwing` (the
  private engine) to `sparkwing/cmd/sparkwing` (the public CLI).
  Quick Start now uses `sparkwing run release` so it works without
  the optional `wing` symlink.

## [v0.6.0] - 2026-05-03

### Added
- **`sparkwing pipeline publish`** — compiles the `.sparkwing/`
  pipeline binary and uploads it to the configured `ArtifactStore`
  at `bin/<hash>`. Supports cross-compile via `--platform GOOS/GOARCH,...`
  (default: current platform). Output formats: `table` / `json` /
  `plain`. Resolves the upload target from `--on PROFILE`'s
  `artifact_store` field or `--artifact-store URL`.
- **`bincache.FetchFromArtifactStore` /
  `bincache.UploadToArtifactStore` /
  `bincache.HasInArtifactStore`** — pluggable binary-cache helpers
  over `storage.ArtifactStore`. Same `bin/<hash>` keyspace as the
  cluster-mode HTTP cache.
- **`bincache.PipelineCacheKeyForPlatform(dir, goos, goarch)`** —
  cross-compile-safe variant of `PipelineCacheKey`. Required because
  `runtime.GOOS` / `runtime.GOARCH` are host-build constants and
  don't reflect a target arch even after `os.Setenv`.
- **`wing` compile path now honors `$SPARKWING_ARTIFACT_STORE`**:
  before falling through to `go build`, fetches `bin/<hash>` from
  the configured store. Combined with `pipeline publish`, this is
  the "ci-embedded runs without a Go toolchain" path -- the runner
  curls a prebuilt binary instead of compiling.

### Changed
- **`bincache.PipelineCacheKey` now content-hashes source files
  instead of mixing in size + mtime.** mtime-based hashing was a
  fast single-machine "did anything change?" heuristic; content
  hashing is required for cross-machine cache sharing because mtime
  trivially diverges between an operator's working tree and a CI
  checkout of identical content. .sparkwing/ trees are small enough
  that the extra read cost is negligible.

### Notes
- `wing run` / `sparkwing run` never auto-upload; the publish
  surface is intentionally explicit so quick local iteration stays
  fast.
- LOCAL-006.

## [v0.5.0] - 2026-05-03

### Added
- **`sparkwing run --mode=ci-embedded --workers=N`** (also on `wing`):
  the GHA / Buildkite / GitLab-CI migration wedge. Runs every node
  as a local process capped at `--workers` (default
  `runtime.NumCPU()`); per-node logs route through the configured
  `LogStore`; a final run + node NDJSON dump uploads to
  `<artifact-store>/runs/<runID>/state.ndjson` on exit so a remote
  dashboard can replay the run.
- **`orchestrator.Options.LogStore` + `Options.ArtifactStore`** —
  honored by `RunLocal`. When `LogStore` is set, the local
  filesystem `LogBackend` is replaced; when `ArtifactStore` is set,
  the state dump runs after the pipeline finishes (success or
  failure).
- **`SPARKWING_MODE` / `SPARKWING_WORKERS` / `SPARKWING_LOG_STORE` /
  `SPARKWING_ARTIFACT_STORE`** env-var contract for handing storage
  config to the pipeline binary's `orchestrator.Main`. CI VMs set
  these directly; laptop users get the same plumbing through
  `--on PROFILE`.
- **`docs/ci-embedded.md`** + **`examples/github-actions-ci-embedded.yaml`**
  covering setup for GHA, Buildkite, GitLab CI.

### Notes
- Live tail of in-flight ci-embedded runs is **not** supported; a
  remote dashboard replays the run after the CI VM exits the dump
  step. Streaming run state to S3 incrementally is LOCAL-005.
- Exit code mirrors pipeline outcome: `0` if all nodes succeed,
  `1` on any failure -- so the wrapping CI step fails when sparkwing
  fails.
- LOCAL-004.

## [v0.4.0] - 2026-05-03

### Added
- **`sparkwing-local-ws --on PROFILE` / `sparkwing dashboard start --on PROFILE`**
  reads `log_store` + `artifact_store` from the named profile in
  `~/.config/sparkwing/profiles.yaml`. Matches the `--on` convention
  used elsewhere in the CLI. Raw `--log-store URL` and
  `--artifact-store URL` flags remain as escape hatches for
  ci-embedded VMs that don't ship a profiles.yaml; explicit URL
  flags override the profile's fields.
- **`profile.Profile.LogStore` + `profile.Profile.ArtifactStore`**
  fields (yaml: `log_store`, `artifact_store`) carrying storeurl-shaped
  values.
- **`sparkwing-local-ws --read-only`** and the underlying read-only
  middleware. Rejects POST/PUT/DELETE/PATCH on `/api/v1/*` (auth +
  webhooks remain open) with 405. Same flag also on `sparkwing
  dashboard start` (forwarded to the supervisor child).
- **`GET /api/v1/capabilities`** on the local controller. Returns
  `{mode, storage:{artifacts, logs}, features}` so the dashboard
  frontend can adapt UI to the configured backends.
- **`GET /api/v1/artifacts/{key}`** on the local controller. Streams
  the artifact at `key` from the configured `ArtifactStore`. 404 when
  no `ArtifactStore` is wired up (lets the frontend probe with one
  GET).
- **`web.NewLogStoreSource(s storage.LogStore)`** — public wrapper so
  any LogStore-backed dashboard plugs into the existing `LogSource`
  contract without bespoke adapters.
- **`local.Server.SetCapabilities` / `SetArtifactStore`** wiring
  hooks consumed by `pkg/localws`.

### Changed
- `pkg/localws.Options` gains `LogStore`, `LogStoreLabel`,
  `ArtifactStore`, `ArtifactStoreLabel`, `ReadOnly`. Default behavior
  unchanged when zero values: filesystem reads under `paths.Root`,
  no read-only restrictions, capabilities reports `fs/fs`.
- LOCAL-003.

## [v0.3.0] - 2026-05-03

### Added
- **`pkg/storage/fs/`** - filesystem backends for `ArtifactStore`
  + `LogStore`. Atomic writes (tmp + rename); 2-char shard prefix
  for artifacts; per-(runID,nodeID) NDJSON files for logs.
- **`pkg/storage/s3/`** - S3-compatible backends. Single PUT for
  artifacts; rolling object-per-Append for logs (lex-sortable
  timestamp+seq keys, ListObjectsV2 + concat on Read). Works
  against AWS S3, R2, MinIO, B2, OCI Object Storage. Verified
  against the live `your-team-sparkwing-store` bucket.
- **`pkg/storage/storeurl/`** - URL parser + opener:
  `OpenArtifactStore` / `OpenLogStore` accept `fs:///abs/path` and
  `s3://bucket/prefix`. Honors `$SPARKWING_S3_ENDPOINT` for non-AWS
  S3 providers.
- **`sparkwing-runner worker --log-store URL --artifact-store URL`**
  flags. `--log-store` is wired end-to-end (logs route through the
  resolved backend); `--artifact-store` is parsed + validated at
  startup and consumed by future cache paths in LOCAL-003/004.
- **`orchestrator.NewLogStoreBackend`** wraps any `storage.LogStore`
  as a `LogBackend`, replacing the HTTP-only `NewHTTPLogs*` path
  for non-HTTP backends.
- **`orchestrator.WorkerOptions.LogStore`** field; takes precedence
  over `LogsURL` when set.

### Notes
- New transitive deps: `github.com/aws/aws-sdk-go-v2` (core, config,
  s3, credentials) + `github.com/johannesboyne/gofakes3` (test-only
  in-memory S3 server).
- LOCAL-002.

## [v0.2.0] - 2026-05-03

### Added
- **`pkg/storage/`** - pluggable storage interfaces (`ArtifactStore`,
  `LogStore`) for the three-mode execution split (local /
  ci-embedded / distributed). Foundation for LOCAL-002's filesystem
  + S3 backends and LOCAL-003's storage-aware sparkwing-local.
- **`pkg/storage/sparkwingcache/`** - `ArtifactStore` adapter over
  the sparkwing-cache HTTP `/bin/<key>` endpoints.
- **`pkg/storage/sparkwinglogs/`** - `LogStore` adapter over the
  sparkwing-logs HTTP service. Wraps the existing `logs.Client`.

### Changed
- Orchestrator and web consumers (`HTTPLogs`, `JobLogsRemote*`,
  `httpLogSource`) now depend on `storage.LogStore` instead of the
  concrete `*logs.Client`. No behavior change; back-end is still the
  HTTP logs service. LOCAL-001.

## [v0.1.0] - 2026-05-03

### Added
- **`cmd/sparkwing-runner`** — the cluster runner agent. Connects
  outbound to a controller (your hosted SaaS or self-hosted enterprise)
  and executes pipelines on customer infrastructure.
- **`cmd/sparkwing-cache`** — binary cache service for compiled
  pipeline binaries + source archives. Self-hostable; customer
  typically runs it in their own region for fast cache hits.
- **`cmd/sparkwing-logs`** — log aggregation service. Self-hostable
  alongside cache.
- **`internal/cluster`** — runner-agent worker logic, trigger loop,
  pool agent CLI plumbing.
- **`internal/runners/{k8s,warmpool}`** — k8s pod dispatch and warm
  PVC pool runner implementations.
- **`logutil/`** — small logging helper used by the new binaries.

### Notes
- All new packages are marked "implementation, unstable" via doc.go
  conventions where applicable. User pipeline code does not import
  any of these.
- Module now requires Go 1.26 (transitive bump from k8s.io/client-go).
- The runner uses the **pull-based agent model** — outbound HTTPS
  only. Customers do not need to expose any inbound network surface.
  Documented in the architecture doc as a key product property.

## [v0.0.1] - 2026-05-03

Initial extraction from the sparkwing engine repo (SDK-014).

### Added
- `sparkwing/` package: stable user-facing DSL — `Plan`, `Job`, `Work`,
  `Step`, modifiers, `Bash`, `Path`, `Info`, `Secret`, `Register[T]`,
  `RunContext`, wire types (`TriggerInfo`, `Git`, `Outcome`, `LogRecord`,
  `DescribePipeline`, etc.). Subpackages: `inputs/`, `docker/`, `services/`,
  `git/`, `planguard/`.
- `orchestrator/` package: runtime that user pipeline binaries link.
  Exported as implementation; APIs may change without notice.
- `controller/client/` package: HTTP client for talking to a sparkwing
  controller. Implementation.
- `bincache/`, `logs/`, `otelutil/`, `profile/`, `repos/`, `secrets/`:
  leaf utility packages used by user binaries. Implementation.
