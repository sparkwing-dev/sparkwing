# Changelog

All notable changes to **sparkwing** are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). The release
pipeline refuses to ship a new version without a matching entry below.

## How to read this

Each entry leads with a bold scope (`**sdk:**`, `**cli:**`, `**controller:**`,
`**cache:**`, `**config:**`, `**release:**`, `**docs:**`, ...) so you can
scan for the surface that affects you. Breaking changes are marked
`- **scope (Breaking):**` -- the marker goes inside the bold scope, before
the colon, which is the only form the changelog lint gate recognizes --
plus a link to a section in that release's
[migration guide](docs/migrations/) -- click through for before/after code,
ordering guidance, and gotchas the inline summary can't fit.

What belongs here:

- User-facing behavior. New features, surfaces, defaults, removals, fixes
  that an adopter would notice.
- Breaking changes. Every break in an exported `pkg/` or `sparkwing/` API,
  CLI flag, wire protocol, or YAML config field. Tagged `(Breaking)` inline.
- Migration steps for breaking changes, linked to the per-release guide.

What does **not** belong here:

- Internal refactors invisible to adopters. Renames inside `internal/`,
  test reshuffles, snapshot regenerations.
- Per-commit narrative. The release page is the narrative; commits are
  the audit trail. The pre-release manicuring agent (see
  [docs/changelog-style.md](docs/changelog-style.md)) consolidates related
  commits into one user-facing entry.
- Internal-only design docs and dev-only tooling unless adopters
  meaningfully see the result.

## Pre-1.0 caveat

sparkwing is on the `v0.x` track. Per [VERSIONING.md](VERSIONING.md),
breaking changes are permitted in minor bumps until v1.0.0. We do **hard
cuts**: removed symbols are gone, not aliased, and there is no deprecation
runway. Each minor release that breaks something ships a migration guide
so the cut is documented even though it isn't softened. Releases at
`v1.0.0+` are blocked at the release pipeline and require a deliberate
code change to unlock.

---

## [Unreleased]

### Docs

- **security:** The security policy now identifies the supported release and
  provides a private GitHub vulnerability-reporting path.

### Fixed

- **helm:** A configured web controller-token Secret is now required, so a
  missing Secret or key keeps the web pod unready instead of starting a proxy
  without its controller credential.
- **docs:** Self-hosting guidance now points operators to direct local
  execution or the complete Helm chart. The unsupported Docker Compose example
  and its private image coordinates have been removed, with migration guidance
  for prior testers.
- **web:** Run selection now discards superseded detail responses, so a slow
  request cannot replace the selected run with stale nodes or metadata.
- **docs:** Trigger filter documentation now distinguishes declarative
  `branches`, `paths`, and `actions` metadata from enforceable run guards and
  shows how to require a literal checked-out branch before any step starts.
  The `default` branch token requires dispatch metadata that controller
  webhook and local trigger claims do not supply.

## [v0.38.2] - 2026-09-01

### Added

- **cli:** `runs find` and `runs failures` now apply repository, branch, and
  commit filters in the run store before ordering and limiting results.
- **cli:** `sparkwing run --sw-run-handle-file PATH` atomically publishes the
  accepted run ID before planning or node execution.

### Fixed

- **orchestrator:** Each detached local run now executes with the environment
  captured by its own submission instead of the resident consumer's startup
  environment.

## [v0.38.1] - 2026-09-01
### Fixed

- **release:** Hosted image builds now refresh their Alpine package layer on
  every workflow attempt, preventing a cached OpenSSH revision from failing the
  final vulnerability scan after Alpine has published its security update.

## [v0.38.0] - 2026-08-31
### Changed

- **release (Breaking):** `sparkwing run kind-e2e` and its hosted workflow have
  been replaced by the opt-in `sparkwing run k8s-e2e` acceptance path. The new
  path requires an explicit Kubernetes context, image prefix and tag, and exact
  namespace/release cleanup allow-list; it never creates or deletes cluster
  infrastructure. See the
  [migration guide](docs/migrations/_unreleased.md#kubernetes-acceptance-testing).

### Fixed

- **admission:** A cache-dominant run now folds the nodes that actually
  executed into their capacity profiles; only the run-level rollup, whose
  wall time measured the cache, stays excluded. Previously such a run
  recorded nothing, so a pipeline whose runs usually cache well -- a merge
  gate rebuilding a small delta -- never accumulated the samples that retire
  the still-measuring charge, and its few live nodes were priced at carried
  prior figures or the half-machine cold-start default indefinitely.

- **orchestrator:** A run's failure line now names only the nodes that
  genuinely failed and counts its cancellations (`nodes failed: [wingd
  compile]; 72 more cancelled with the run`). Cancelled nodes were folded
  into the failed list, so a run aborted by a lost daemon read as a
  catastrophic all-node failure instead of two failures and their
  collateral.

- **controller:** GitHub webhook runs retain the full `owner/repository`
  identity when they enter the runner. Remote retries can reconstruct and fetch
  the original source instead of failing with a missing repository URL.

## [v0.37.4] - 2026-08-30
### Added

- **wingd:** `sparkwing daemon restart --force` replaces an answering daemon
  even when it already serves the installed build. Existing holders reattach
  to the successor, and an absent daemon stays stopped.

## [v0.37.3] - 2026-08-30
### Added

- **cli:** `sparkwing pipeline trigger --working-tree` now freezes tracked
  edits and untracked non-ignored files into an immutable Git snapshot, seeds
  it before admission, and runs it remotely without an origin push. Remote
  runners can clone source through the controller's admin-authenticated,
  read-only Git proxy using outbound HTTPS only. Login-enabled dashboard
  ingress accepts machine bearers on that proxy, direct-cache writes use only
  the cache token, exact bytes are restored after Git checkout, retries and
  same-repository children retain desktop placement, and each repository admits
  at most 128 distinct workspace refs without evicting admitted work.

- **controller:** Pull-request webhook runs now publish best-effort GitHub
  commit statuses under `sparkwing/<pipeline>` when `GITHUB_TOKEN` is set.
  `SPARKWING_DASHBOARD_URL` optionally adds a run-detail link. GitHub delivery
  errors are logged and never change webhook admission or the run result.
  Overlapping deliveries for the same commit and pipeline suppress terminal
  updates from superseded runs so they cannot overwrite the current result.
  Programs serving `Server.Handler` directly must call `Server.Shutdown`.

- **store:** `JoinProfileKey`, `SplitProfileKey`, and `DisplayProfileKey`
  expose the capacity-profile key encoding, so a program reading
  `runs stats -o json` or the capacity API can decode a stored key into its
  repo and pipeline halves.

### Fixed

- **admission:** The daemon supervisor now treats a completed protocol handshake
  as proof of liveness. Health checks no longer request a full queue snapshot, so
  a large admission queue cannot make its own supervisor replace the serving
  daemon and disconnect every active lease.

- **admission:** Linked worktrees now share their repository's canonical
  capacity profile. v0.37.2 keyed a checkout by its origin remote (or borrowed
  object store) but left worktrees on the repository's directory name, so a
  worktree and its main checkout split onto two profiles and each re-learned
  what the other already knew. A worktree resolves through the shared config
  and object store in its common git dir, a submodule through its own gitdir
  config and object store, and both fall back to the directory name as before
  when the repo names no remote.

- **cli:** `runs stats` learned the v0.37.2 profile-key encoding. The capacity
  table, drift lines, reset summaries, and doctor's poisoned-profile warning
  print keys as repo/pipeline again instead of the stored length-prefixed
  form, and `--reset --pipeline` accepts the bare pipeline name, the displayed
  repo/pipeline form, or the stored key -- a bare name matched nothing under
  the new encoding, leaving doctor's suggested reset the only working spelling.
  A reset reaches every stored encoding of the name it is given, since a
  pre-v0.37.2 row and its encoded successor display identically and the
  survivor would otherwise keep pricing runs behind a success message.

- **web:** The capacity dashboard prints profile keys as repo/pipeline instead
  of the stored length-prefixed encoding, matching the CLI table.

- **exec:** A no-progress timeout on Linux and macOS now sends `SIGQUIT` before
  terminating the command session, making Go goroutine dumps available through
  `runs logs`. Core-file generation is disabled for the diagnostic-enabled
  session, ordinary cancellation remains immediate, diagnostic output is capped
  at 16 MiB, and a process tree that ignores `SIGQUIT` is force-killed after ten
  seconds.

### Security

- **web (Breaking):** Login-required dashboards now refuse to start without a
  controller session backend. Login, first-admin, and logout forms enforce
  same-origin CSRF tokens; post-login redirects accept encoded same-origin
  paths only; unsafe browser API mutations require the live session's CSRF
  header; browser cookies never cross the service-bearer proxy boundary; and
  controller-side session revocation takes effect on the next protected data
  request instead of after a 60-second web cache. Transient session-backend
  failures return `502` without deleting browser cookies. See the
  [migration guide](docs/migrations/v0.37.3.md#dashboard-browser-session-hardening).

## [v0.37.2] - 2026-08-28
### Fixed

- **admission:** A checkout is now identified by the repository it holds -- its
  origin remote, or the object store it borrows from when it has no remote --
  rather than by the directory it sits in, so two checkouts of one repository
  share one capacity profile. A pipeline that clones into a fresh directory per run previously
  recorded each run under a new key, so its node measurements never reached the
  sample count that retires the cold-start charge and every run was priced at
  half the host per node. Linked worktrees are unchanged, and a checkout with no
  origin keeps its directory name.

- **admission:** macOS hosts now charge external memory pressure when the
  kernel reports no memory available. A `kern.memorystatus_level` of zero was
  classified as an unread sensor rather than as a reading, and an unread
  dimension charges nothing, so the host with nothing left granted its full
  capacity -- the inverse of the guard at the one point it exists to act.
  Linux already treated a zero available-memory reading as measured, so the
  same exhausted host refused work there and admitted it here. A level the
  kernel never populated still reports unmeasured: that sysctl fails rather
  than answering zero, which is what separates the two.

## [v0.37.1] - 2026-08-28
### Fixed

- **release:** Release builds now pin Go 1.26.6 across every binary and image
  runner, and container runtimes use the patched Alpine 3.24 base. Maintainers
  can rebuild an existing immutable tag after a
  publication-only failure; the recovery path validates and checks out that
  exact tag while preserving the original canonical-gate result.
- **orchestrator:** The default dispatch watchdog now accounts for declared
  node timeouts, retry attempts, retry backoff, dependency paths, and failure
  recovery before classifying a run as wedged. Explicit
  `SPARKWING_DISPATCH_WAIT_TIMEOUT` values remain exact operator overrides.

## [v0.37.0] - 2026-08-28
### Fixed

- **web (Breaking):** Live dashboard node logs now retain structured step buckets instead
  of collapsing pretty-rendered stream output into preamble. Authenticated
  dashboards also provide a `Log out` control in the top navigation and keep
  the controller service credential out of browser HTML and requests; the
  server-side proxy adds that credential only after validating the user's
  session cookie. Clients that sent a bearer token directly to a login-gated
  dashboard proxy must use a browser session or call the controller API
  directly. See the [migration guide](docs/migrations/v0.37.0.md#authenticated-dashboard-proxy-sessions).
- **release:** Pull requests, main, and release tags now run the canonical
  pre-commit and release-boundary gates in hosted CI. Artifact builds and
  publication wait for the tagged commit to pass, and verification jobs hold
  read-only repository permissions. Hosted frontend verification enumerates
  TypeScript tests explicitly and fails when none are discovered.
- **release:** Release builds now scan every exact Go binary, the production
  dashboard dependency tree, and both architectures of every container image
  before version or latest tag promotion. The shipped gRPC and OpenTelemetry
  trees are upgraded past reachable vulnerability findings, and any temporary
  image waiver must record an owner, reachability, mitigation, and a future
  expiry.
- **local security:** Sparkwing now creates its POSIX home, run, cache, and
  local-work directories as owner-only (`0700`), and creates SQLite state,
  outbox sidecars, logs, PIDs, rerun values, and local environment files as
  owner-only (`0600`). `sparkwing doctor` reports and repairs permissive legacy
  homes without following symlinks or removing cached binaries' execute bit.
  Windows continues to use inherited DACLs; doctor reports that ACL privacy as
  unverified instead of presenting a false clean bill.
- **Helm:** The full self-host chart now sends its runner to the bundled
  controller without an extra values override. The logs service enables
  controller-backed auth only when a token Secret is configured, and the
  runner no longer points no-repository triggers at a binary absent from its
  image. Helm rejects trigger pools with no gitcache and incomplete Secret
  references, treats configured runner and cache Secrets as required, and
  keeps long component names unique while parent service URLs remain aligned
  with runner-bundle names. The current chart defaults still do not identify a
  compatible public image set; operators must pin repositories and tags for
  every enabled image until that release blocker is cleared.
- **release:** Container publication now builds `sparkwing-runner` from its
  dedicated image contract, preserving the entrypoint, Go toolchain, and SSH
  client the Helm workload requires. The image also includes the split Alpine
  `git-daemon` package, so repository fixtures and other daemon-mode Git
  workloads do not fail after scheduling.
- **release:** The Kubernetes golden-path proof can target either a disposable
  local Kind cluster or an explicit existing-cluster context with caller-supplied
  image coordinates. Existing-cluster runs require an exact namespace/release
  cleanup allow-list, use a ConfigMap-backed Git fixture instead of a node host
  mount, and leave cluster infrastructure intact.
- **chart:** Controller, web, and runner home volumes are now assigned to the configured
  non-root UID by a short CHOWN-only init container before startup, so their
  private Sparkwing homes work on root-owned PVC and `emptyDir` mounts without
  weakening the application containers. Operators whose storage driver already
  sets ownership can disable the corresponding `volumePermissions.enabled`;
  this is also required by Restricted Pod Security and by custom images that
  do not contain `/bin/chown`.
- **s3 state:** The local SQLite outbox now retains a queued state or artifact
  write when replay reaches a non-transient object-store error, and `Drain`
  returns that error. The background drainer emits one structured warning for
  a stalled FIFO head, another only when the head or error changes, and one
  recovery notice when replay resumes. The row previously disappeared even
  though its staged write had never reached the bucket.

### Security

- **controller:** First-admin creation now requires an admin bearer token when
  controller authentication is enabled. The unauthenticated first-visit signup
  remains available only while controller authentication is disabled.

## [v0.36.0] - 2026-08-26
### Changed

- **local execution (Breaking):** Every job in a local run now executes as
  its own OS process, spawned from the same `run-node` entrypoint Kubernetes
  pods use, so one execution model serves the laptop and the cluster. Jobs no
  longer share memory with each other or with the dispatcher: typed refs and
  artifacts are the only data paths between them, which is what a pod has
  always enforced. `Ref[T].Get` now resolves from the producer's stored JSON
  on every model there is, a `go test` that runs the whole pipeline inside
  one binary included, so a pipeline that only worked because two jobs shared
  a package variable or a pointer fails where its author can see it rather
  than on the first deploy. A job's output must therefore be
  JSON-serializable: a declared output type that can never encode is rejected
  at plan time, and a value that will not marshal fails its node instead of
  reporting success and handing consumers nothing. A job process also gets no
  terminal -- stdin is `/dev/null`, and stdout and stderr are pipes the
  dispatcher forwards into the run log and onto your screen -- so a step that
  prompted for input, or a tool that insists on a TTY, needs its
  non-interactive flag. `Inline()` keeps its purpose and changes its meaning:
  it says the job runs on the dispatcher's host instead of on a cluster
  runner, not that it runs in the dispatcher's memory, and a local inline job
  is its own process like any other. Inside a job nothing changes; steps
  still share their job's process, exactly like a pod. What a job leaks,
  corrupts, or crashes is now its own, which is the point. See the [migration
  guide](docs/migrations/v0.36.0.md#process-per-node).
- **local execution (Breaking):** That now includes runs whose state is
  object-store NDJSON (`state: { type: s3 }`), the shared-bucket shape
  documented for CI. Such a run kept executing every job inside the
  orchestrator's process, because a job process reaches run state through a
  controller and a bucket has none to point it at. The dispatcher now mounts
  one on loopback for the run -- the same routes, request bodies, and status
  codes a pod talks to, served over the run's own state -- so there is a
  single local execution model rather than one per backend. What a
  bucket-backed run does *not* gain is measured capacity profiles: those are
  folded from the local runs store, and a run whose state lives on a bucket
  still folds nothing, exactly as before. See the [migration
  guide](docs/migrations/v0.36.0.md#object-store-local-runs-execute-process-per-node).
- **local execution (Breaking):** A locally spawned job process now writes its
  log to the surface its profile declares, instead of always to the executing
  machine's disk. Jobs of a run with `logs: { type: s3 }` post to the bucket,
  and jobs of a run whose profile routes logs through a controller post to the
  logs service -- in both cases what `sparkwing runs logs` reads. A profile
  naming no logs surface is unchanged: those jobs still write the run's local
  log files. A declared logs surface that will not open now **fails the job**,
  naming the profile and the surface type, rather than silently degrading to
  local files and splitting one run's logs across two places. See the
  [migration guide](docs/migrations/v0.36.0.md#local-node-logs-use-the-declared-logs-surface).
- **orchestrator:** An `OnFailure` recovery node on the local path now gets
  the same dispatch envelope as every other node -- the cache lookup, the
  concurrency slot with its `OnLimit` policy, and `SkipIf` -- which is what a
  Kubernetes pod has always given it. The local dispatcher used to run a
  recovery body directly, ignoring all three, so a rollback declared
  `Concurrency(g)` on a full group ran anyway instead of respecting the
  budget, a `SkipIf` guard never fired, and a `Memoize` declaration was
  silently dead. Expect a recovery node enrolled in a full group under
  `OnLimit: Fail` to fail where it previously ran, and one carrying `Memoize`
  to replay a cached result on a repeat failure.
- **store (Breaking):** The runs-store schema advances from version 14 to 15,
  adding `cpu_nanos`, `max_rss_bytes`, and `process_wall_nanos` to `nodes`,
  and `cpu_time_nanos` to `node_metrics`. The node columns hold the kernel's
  exit accounting for the process that ran the node, which local runs now
  record, and the span that CPU was drawn over; the metric column marks a
  sample as a per-command report and carries the CPU it measured. A node or
  sample that carries none of it keeps the zero default, read everywhere as
  absent rather than as a measurement of nothing. The upgrade is additive and
  applies on open. As with every schema advance, a binary older than this
  release refuses to open a database that has been migrated, so upgrade every
  sparkwing sharing a runs store together -- the admission daemon included,
  since it opens the same store. See the [migration
  guide](docs/migrations/v0.36.0.md#runs-store-schema-advances-to-version-15).
- **store (Breaking):** The runs-store schema advances again, from version 15
  to 16, adding a `node_bounces` table that records each request to restart a
  running job's process and what became of it. Nothing existing is touched,
  the upgrade applies on open, and a store that never sees a bounce simply
  keeps the table empty. The usual schema rule applies: a binary older than
  this release refuses to open a migrated database. See the [migration
  guide](docs/migrations/v0.36.0.md#runs-store-schema-advances-to-version-16).
- **store:** `Store.StartNode` no longer reopens a job that already
  recorded its outcome; a start arriving after a terminal row is a silent
  no-op, matching the guard `FinishNode` has always had. A job's terminal row
  is the executing process's own verdict, and now that a job can be re-run in
  place, a re-execution racing that write can no longer flip the row back to
  running or overwrite the verdict it carries.
- **admission:** A local node is priced from what the kernel charged its
  process, not only from what the two-second sampler happened to see. A node
  shorter than one sampling interval used to be invisible to pricing -- a
  pipeline of them learned nothing and kept paying a cold start no matter how
  often it ran -- and now records its measured CPU and peak memory like any
  other. Where both measurements exist the exact one is a floor under the
  sampled figures, so charges can rise for pipelines whose work lives between
  ticks. A node's recorded duration is now its process's whole life, spawn to
  reap, rather than the shorter window the node stamps from inside itself, so
  ETAs count the startup the box actually paid for. Cluster pricing is
  untouched: a pod reports no exit accounting, and a controller-backed run is
  folded by the controller, which prices from pod samples exactly as before.
- **admission:** A local run's rollup adds up what every node was drawing at
  the same moment, grouping readings into sampling windows instead of by
  exact timestamp. With each node sampling itself in its own process, two
  nodes never stamp the same nanosecond, and the previous grouping quietly
  reduced a parallel stage to its widest single node -- half a machine for a
  pair, a quarter for a fan-out of four, always in the direction that admits
  too much. Expect rollup peaks for pipelines that fan out to rise to what
  they always claimed to measure. Per-node figures are unchanged.

### Added

- **sdk:** `sw.JobSpawn` and `sw.JobSpawnEach` now work from inside a job
  wherever that job runs -- a Kubernetes pod, a local node process -- and not
  only from a job the dispatcher happened to be running itself. Every spawn
  outside the dispatcher used to hard-fail with "no SpawnHandler is installed
  in ctx", so a pipeline that decides its work mid-job could not run on the
  cluster at all. A spawned child gets the same run record it has always got
  -- a `<parent>/<id>` row, its own logs, the parent's `spawn_dispatched`
  event -- and its `WhenRunner` terms are checked against what the executing
  process advertises, matching the gate the dispatcher has always applied on
  its own path. A child is measured and priced as its own node: it gets its
  own sampler share and its own learned profile under `<parent>/<id>`, and
  because parent and child share one process the interval's reading is split
  between them rather than counted twice.
- **cli:** `sparkwing runs bounce --run RUN_ID --node NODE_ID` restarts one
  running job's process without failing the run it belongs to. The job's
  process is stopped -- SIGTERM, then SIGKILL after the grace period -- and
  the job is re-run from its first step; it never reaches a terminal state,
  so nothing downstream sees a failure and the rest of the run keeps going.
  Reach for it when a job is wedged and cancelling the whole run would cost
  more than it saves. The verb records the request and returns; the runner
  supervising the job acts on it within a few seconds. Steps run again, so a
  job with side effects needs the same idempotency a restarted pod already
  demands. A job that finishes before the stop lands is left alone, and
  bouncing again is allowed -- one request is one restart. Local runs today;
  the request is recorded through the controller, so a hosted controller
  serves it the same way.
- **store:** `Store.RequestNodeBounce`, `Store.PendingNodeBounce`,
  `Store.ConsumeNodeBounce`, and `Store.ListNodeBounces` read and write those
  requests, and `client.Client` carries the same three calls over HTTP. A
  request is refused for a run that has already finished or a job that is not
  running, since neither has a process to stop.
- **dashboard:** A Capacity page showing what admission is charging on this
  machine and how it got there. The live host ledger prints the subtraction
  behind each available figure (capacity, held, reserved, measured external
  load) beside every holder's charge with the daemon's own rationale and
  every waiter's blocking reason verbatim; a priced table lists each measured
  pipeline's CPU and memory charge, sortable, with the resolution source, the
  demand floor, and any pin drift; and picking a pipeline renders its stored
  sample window with the run each percentile charge was ranked out of marked,
  alongside the resolution order that produced the price. Host figures
  refresh every 2 seconds and learned pricing every 10; with no daemon
  running the page says so and still prices from the runs store. Two
  read-only dashboard endpoints back it, `GET /api/v1/capacity/profiles` and
  `GET /api/v1/capacity/profiles/explain?pipeline=NAME`.
- **store:** `Store.AddNodeUsage` folds what the kernel charged one node
  process into the node row -- CPU and occupancy accumulate across retried
  attempts, peak memory keeps its high-water -- and `store.Node.CPUNanos`,
  `store.Node.MaxRSSBytes`, and `store.Node.ProcessWallNanos` read it back.
  `store.MetricSample.CPUTime` and `MetricSample.OneShot` distinguish a
  per-command report from a sampler tick. Zero in any of these means nothing
  measured it, so a reader must treat it as absent rather than as a node or
  command that cost nothing.
- **store:** `Store.ProfileSamples` and `store.NearestRankIndex` expose a
  pipeline profile's stored sample window and the position a percentile
  charge was taken from, so a reader can recompute an admission charge from
  the same rows it was priced from.

### Removed

- **sdk (Breaking):** `RuntimePlumbing.Keys.RefResolver` is gone, with the
  live-Go-value reference resolver it keyed. A node's output reaches its
  consumer as JSON in the run store on every execution model, so `Ref[T].Get`
  has one path and the orchestrator installs one resolver instead of a typed
  one that only a shared process could satisfy. The author-facing surface is
  unchanged: `RuntimePlumbing` is orchestrator plumbing that pipeline code is
  documented not to reach for, and `Ref[T].Get` reads exactly as before. See
  the [migration guide](docs/migrations/v0.36.0.md#process-per-node).

## [v0.35.0] - 2026-08-24
### Changed

- **admission:** Learned CPU charges price a run's sustained demand instead
  of its burst peak. A run's cost is now the p95 across recent runs of the
  core level that covers four sampling ticks in five of each run, never below
  that run's average draw, so a node that touches 4.9 cores for one tick of a
  three-minute run no longer reserves 4.9 cores for the whole run. Memory
  still charges the p95 of the per-run peaks, because an oversubscribed box
  does not time-slice memory the way the kernel time-slices contended CPU.
  Expect a box to admit more concurrent work than it did. Queue lines and pin
  drift warnings read `measured sustained p95 over N runs`, and
  `sparkwing runs stats --capacity` gains a `CPU CHARGE` column naming the
  figure admission reserves beside the unchanged p50/p95/peak distribution.
  Existing profiles are carried forward at their current price and converge
  on the new one within twenty runs. Cluster (Kubernetes) pod sizing is
  deliberately unchanged: a pod CPU limit is a hard quota, not compressible
  headroom, so it stays peak-derived.
- **store (Breaking):** The runs-store schema advances from version 13 to 14,
  adding `sustained_cores` and `prev_sustained_cores` to `pipeline_profiles`,
  both backfilled from their peak counterparts so every stored profile keeps
  its current price until fresh runs are measured. The upgrade is additive and
  applies on open. As with every schema advance, a binary older than this
  release refuses to open a database that has been migrated, so upgrade every
  sparkwing sharing a runs store together. See the [migration
  guide](docs/migrations/v0.35.0.md#runs-store-schema-advances-to-version-14).

### Fixed

- **orchestrator:** Per-node resource metrics divide each interval's
  process-wide reading across the nodes running in it, and a run's learned
  profile is folded from the readings those shares reconstruct. Every node of
  a parallel fan-out previously recorded the whole process's CPU and memory,
  so learned capacity charged the fan-out several times over what it used.
- **orchestrator:** macOS memory samples report the process's current
  resident set instead of `getrusage`'s lifetime high-water mark, which made
  every node that ran after a memory peak inherit that peak as its own.

## [v0.34.1] - 2026-08-20
### Fixed

- **wingd:** Supervisor shutdown escalates to a bounded forced stop when graceful
  termination fails or a killed child does not reap, preventing orphaned daemon
  processes and unbounded replacement waits.
- **orchestrator:** Approval and debug-pause expirations wake at their configured
  deadlines instead of waiting for the next 500ms state-poll interval.
- **orchestrator:** Trigger consumers inspect queued dispatches and child triggers
  immediately after startup instead of delaying the first claim by one 500ms poll.
- **orchestrator:** Maintenance cadence scales with configured dispatch leases, so
  expired work using short leases is reconciled before its next heartbeat while
  the default three-minute lease retains its existing cadence.
- **controller:** Cancelling Kubernetes pool warming interrupts pod polling
  immediately and still removes the temporary warmup pod under a bounded cleanup
  context.
- **concurrency:** Superseded work receives context cancellation when holder
  observation or lease renewal reports that its replacement owns the slot.
- **cli:** Consumer stop and upgrade replacement recognize a completed Unix
  process immediately instead of waiting through the forced-stop grace period.
- **admission:** Capacity measurements remain attached to the run's repository
  when job code changes the process working directory.
- **sdk:** `Requires` documentation distinguishes runner-claimed work from
  direct and inline execution instead of claiming unmatched labels fail
  validation.
- **docs:** Scheduling guidance no longer claims runner preferences appear in
  surfaces that do not consume them.
- **sdk:** Runner-preference documentation and generated scaffolds state that
  preferences are recorded in plan snapshots and do not affect runner selection.
- **config:** Pipeline YAML parse errors name `sparkwing.yaml` instead of the
  retired `pipelines.yaml` file.
- **sdk:** Secret inspection documentation describes the typed `Secrets()`
  provider instead of removed YAML secret declarations.
- **logs:** Server-side line filters preserve the selected lines' final newline
  instead of adding or removing one.
- **cli:** `configure xrepo` help, command discovery, and shell completion
  expose its `list`, `add`, `remove`, and `prune` subcommands and inputs.
- **cli:** Run-command errors and help use the public `runs` namespace instead
  of the retired `jobs` spelling.
- **cli:** `runs logs --help`, shell completion, and the CLI reference expose
  the existing `--events-only` and `--no-events` stream filters.
- **cli:** Bash, Fish, and Zsh complete profile names for the live `--profile`
  flag instead of the retired `--sw-profile` spelling.
- **cli:** Zsh completion no longer advertises removed profile runner
  selection.
- **cli:** Zsh completion no longer queries removed pipeline target metadata
  when completing `--target`.
- **cli:** `runs cancel --help` documents the existing `--home` option for
  selecting local daemon and queued-run state.
- **cli:** Webhook listing and replay no longer advertise an unused
  `--profile` value.
- **cli:** `cluster image rollout` no longer requires or advertises an unused
  `--profile` value.
- **cli:** Local receipt help no longer advertises the removed profile billing
  rate and states that local receipts report zero cost.
- **cli:** Run retry, cancellation, and pruning help distinguishes local runs
  from named remote profiles.
- **cli:** `cluster worker --help` marks `--profile` required and no longer
  claims a global default profile.
- **cli:** Secret command help distinguishes local files from named remote
  profiles and includes examples for both modes.
- **cli:** Required `--profile` flags no longer claim a default value.
- **cli:** User-management help marks `--profile` required and includes it in
  every example.
- **cli:** Token command help marks `--profile` required and includes it in
  every example.
- **cli:** Remote client commands reject an omitted `--profile` before
  attempting the request.
- **cli:** Help and authentication guidance no longer advertise the removed
  `configure profiles use` command.
- **cli:** `configure profiles set --help` now lists only the fields the
  command can update.
- **cli:** `configure profiles add --help` now lists only the flags accepted
  by the command.
- **cli:** `runs list --help` no longer advertises the unsupported `--tag`
  filter.
- **cli:** Generated help no longer repeats `--output` for profile probes,
  webhook inspection, or agent listings.
- **cli:** Help marks subcommands as optional when the parent command also
  runs directly, including `queue`, `repos`, `run`, and `version`.
- **cli:** Run lookback flags accept `d` and `w` suffixes in addition to Go
  duration syntax. Commands such as `runs list --since 7d`, `runs grep
  --since 7d`, and `runs prune --older-than 2w` now use the same parser.
- **cli:** Read-only local queue queries no longer extend the admission
  daemon's lifetime. Monitoring can continue while an idle daemon reaches its
  configured shutdown window and its supervisor exits.

## [v0.34.0] - 2026-08-18
- **sdk:** Jobs can declare `NoProgressTimeout` alongside a longer absolute
  `Timeout`. Observable node log records reset the per-attempt inactivity
  window; delegated child work and admission waits do not consume it. A node
  that remains silent past the window fails with `no_progress_timeout`.
- **templates:** the scaffolding registry drops `go-test-build-deploy-k8s`
  and `canary-deploy-k8s`, leaving 37. Both branched on
  `SPARKWING_KIND_CLUSTER` to load the image they had just built into a
  local cluster, and sparks-core removed kind support from `docker`,
  `kube`, `deploy` and `rollback`. The branch still compiled, so
  `template-verify` kept passing them while a scaffolded pipeline built
  an image, never got it to a cluster, and deployed something stale. On
  AWS, `gke-deploy-gar-kubectl` is the kubectl-apply template to start
  from; for a canary, write the steps into `docker-deploy-ecr-eks`'s
  deploy node. `sparkwing examples --name` now points at
  `container-deploy-ecs-fargate` in its own help, since the name it
  showed no longer resolves.
- **cli (Breaking):** A run that lost log lines fails instead of
  reporting success. When the log store stays unreachable past the
  append retry budget, the node fails with the new `logs_dropped`
  failure reason and the lost-line count reaches the run record. It
  used to print `status: success` with rc 0 while dropping every line,
  which is the same false all-clear `logs_auth` already exists to
  prevent. Such a run also finishes at its real speed now -- a short
  breaker window after the first exhausted budget stops every
  subsequent line paying the full retry cost, which took one 84ms
  pipeline to 43.9s. Set `SPARKWING_LOGS_DROP_POLICY=warn` to keep the
  old lossy behavior. See
  [migration](docs/migrations/v0.34.0.md#lost-log-lines-fail-the-run).
- **cli (Breaking):** `runs list`, `runs status`, and `runs logs` read
  through the project's `defaults.profile` when no `--profile` is
  given, which is the store `sparkwing run` writes to. They used to
  read the local SQLite store regardless, so on a machine sharing a
  bucket `runs list` came back empty. See
  [migration](docs/migrations/v0.34.0.md#read-commands-follow-the-projects-default-profile).
- **config (Breaking):** A backend spec must carry the fields its type
  needs: a `bucket` for `s3` / `gcs` / `azure-blob`, a `path` for
  `filesystem`, a `url` or `url_source` for `postgres` / `mysql`, a
  name for `controller`. `{type: s3}` with no bucket used to load
  clean and render as `s3://`. `sqlite` with no path is still valid --
  the resolver fills in the host's own state database. See
  [migration](docs/migrations/v0.34.0.md#backend-specs-declare-their-required-fields).

### Added

- **controller:** `sparkwing-controller --logs-url URL` (env
  `SPARKWING_LOGS_URL`) announces the sparkwing-logs service on `GET
  /api/v1/services`, and a runner with no `logs:` surface posts node log
  lines there. The controller serves no `/api/v1/logs` route of its own,
  so a two-process deployment was sending every append into a 404 --
  silently before the `logs_dropped` change above, and as a failed run
  after it. Without the announcement a runner still falls back to the
  controller's URL, which is correct when one process serves both.
- **cli:** `pipeline hooks install --profile NAME` pins the storage
  profile a git hook's runs use, and the generated hook no longer
  inherits `SPARKWING_PROFILE` from the shell that invoked git. Two
  identical commits seconds apart could otherwise land in different
  stores, both printing a green tick. The quiet renderer a hook prints
  through now names the active profile, so a tick says which store
  produced it.
- **cli:** `dashboard start --profile NAME` reads the dashboard's logs
  and artifacts through a storage profile's surfaces. The flag was
  advertised by help, completion, and the CLI reference, and was
  registered nowhere.

### Fixed

- **cli:** `--profile NAME` resolves against the project's own
  `profiles:` block as well as `~/.config/sparkwing/profiles.yaml`. A
  profile declared in `sparkwing.yaml` was reachable by a bare
  `sparkwing run` and rejected by the flag every doc example teaches.
  A name in both files resolves to the user's.
- **cli:** `sparkwing profile` with no flag names the store it would
  use. It reported "project defaults apply" and then rendered every
  surface as unset, so no command answered where a run's state went.
- **cli:** `runs status` exits 0 for a successful run read through a
  profile. The exit code came from local SQLite whichever store the
  status was read from, so any machine that had not itself run the
  pipeline printed the right status and exited 1.
- **cli:** A missing AWS region names `AWS_REGION` and the backend that
  wanted it, rather than the SDK's "resolve auth scheme: resolve
  endpoint: endpoint rule error, Invalid region".

### Docs

- **docs:** `deployment-modes.md` states what a Mode 2 runner needs
  beyond the bucket and the shared profile: `AWS_REGION`, the AWS
  credential chain, and `SPARKWING_S3_ENDPOINT` for a non-AWS store --
  including that the endpoint applies process-wide, so one profile
  cannot mix a MinIO cache with a real-AWS state. It also records that
  the secrets surface has no object-store option, so "no controller"
  and "no per-host secret provisioning" cannot both hold.

## [v0.33.0] - 2026-08-13
- **controller (Breaking):** On Darwin, the first host CPU sample after start
  reports `CPUMeasured=false` instead of a process-lifetime average. Later
  samples derive utilization from the change in cumulative process CPU time,
  so past CPU work no longer remains booked as current external load. In the
  measured queue, this defect caused roughly 18% of admission refusals; 82%
  remained with external CPU forced to zero because reservation accounting is
  a separate constraint. Callers that already defer admission decisions until
  CPU is measured need no change. See
  [migration](docs/migrations/v0.33.0.md#the-first-darwin-cpu-sample-is-unmeasured).
- **cli (Breaking):** `pipeline list -o json` is an index. It carries
  `name`, `short`, `entrypoint` and `triggers` -- what a caller picks a
  pipeline by -- where it used to carry every pipeline's full help text,
  args, examples, env vars and risk labels as well. `pipeline describe
  --name <n>` was already the read verb for those and still answers with
  all of them. A pipeline declaring no `short` now summarizes as the
  first line of its help, so nothing loses its line. `pipeline discover`
  streams the same index plus its score. See
  [migration](docs/migrations/v0.33.0.md#pipeline-list-is-an-index).
- **cli (Breaking):** `pipeline sparks list -o json` is a stream: a
  `kind: summary` line carrying `sparkwing_dir` and the library count,
  then one `kind: library` line per library. It was a pretty-printed
  object wrapping a `libraries` array, so `head -1` returned `{` and a
  truncating reader got nothing. Single-object verbs are unchanged, as
  in v0.32.0. See
  [migration](docs/migrations/v0.33.0.md#pipeline-sparks-list-is-a-stream).
- **cli:** `configure profiles list` takes `-o pretty|json|plain`, which
  it had no machine-readable mode at all before. JSON is one profile per
  line; the token is redacted in every mode, because a machine-readable
  listing is the shape most likely to be piped into a log.

## [v0.32.1] - 2026-08-13
### Changed

- **sdk (Breaking):** The result-memoization modifier `.Cache()` is renamed to
  `.Memoize()` on both `JobNode` and `JobGroup`, with `CacheConfig` →
  `MemoizeConfig`, `CacheOption` → `MemoizeOption`, and `CacheConfig()` →
  `MemoizeConfig()`. `CacheKey`, `CacheKeyFn`, `Key`, `NoCache`, and `TTL` keep
  their names. The old name read like GitHub Actions `actions/cache` while doing
  the opposite (skipping the node instead of restoring a directory); the
  `actions/cache` equivalent is `.CacheDir()`. No alias: every call site is a
  compile error until updated. See
  [migration guide](docs/migrations/v0.32.1.md#cache-becomes-memoize).

### Fixed

- **cli:** `--help` lists the subcommands the CLI actually dispatches. A group's
  COMMANDS listing is now derived from the command registry rather than authored
  beside it, so it can no longer name a command that does not exist or omit one
  that does. `sparkwing --help` gains `examples`, `sparkwing run --help` gains
  `config`, `sparkwing configure xrepo` is a registered command instead of a
  mention in its parent's listing, and about seventy subcommand descriptions that
  had been reworded on only one side now match the command's own synopsis. Hidden
  commands stay out of every listing as before.

- **release:** Windows CLI binaries build again. The stale socket cleanup now
  checks Unix directory ownership only on Unix, so cross-compilation no longer
  references `syscall.Stat_t` on Windows.

## [v0.32.0] - 2026-08-13
### Changed

- **cli (Breaking):** list-shaped `-o json` output is NDJSON -- one
  complete, independently parseable JSON object per line, with no
  enclosing array and no pretty-printing. `sparkwing commands -o json`
  was 258KB across 6,439 lines and `AGENTS.md` sends an arriving agent
  straight to it, so the orientation path handed a fresh agent a
  quarter-megabyte document whose first five lines parsed as nothing:
  `head` is the only sizing tool a caller has, and it is line-oriented.
  Now `sparkwing commands -o json | head -5` is five whole command
  records, and `runs list -o json` is 20 lines instead of 633. Migration
  is mechanical -- decode a line at a time, drop the `[0]` indexing --
  and no field was renamed or dropped. An empty listing is an empty
  stream rather than `[]`. Single-object verbs (`runs status`, `runs
  get`, `runs receipt`, `pipeline describe`, `queue`, `doctor`,
  `version`, `info`) are unchanged, as are `pretty`, `plain`, and
  `markdown`. See
  [migration guide](docs/migrations/v0.32.0.md#list-output-is-ndjson)
  for the full command list.
- **cli (Breaking):** `sparkwing commands -o json` emits index fields
  only. Each record is now `path`, `synopsis`, and `subcommand_count`
  (0 means the verb is a leaf); `description`, `flags`, `examples`,
  `positional_args`, and the `subcommands` array are gone from the
  listing, which drops the full surface from 207KB to 17KB. Those
  fields are exactly what `<path> --help` prints, from the same command
  registry -- so the listing was spending a caller's context budget on
  a second copy of the help system, one that could disagree with the
  first. Read the index to choose a command, then `<path> --help` to
  learn how to call it; `--help --json` is unchanged and still returns
  the full record for one command. `-o pretty`, `-o plain`, and `-o
  markdown` (including `--split-dir`, which generates the
  `docs/cli-*.md` reference) are untouched. Hidden commands stay out of
  the listing, now as a documented decision rather than an accident of
  the filter; `--include-hidden` lists them marked `"hidden": true`.
  See
  [migration guide](docs/migrations/v0.32.0.md#commands--o-json-is-an-index)
  for before/after records.

### Fixed

- **wingd:** harden daemon lifecycle arbitration across stalled holders,
  restarts, build skew, and scratch homes. Contended holders now prove control
  plane liveness before keeping capacity; restart preserves every durable lease
  even when the current budget shrank and gives clients a wider reattach
  window; same-major builds fail closed unless their identities establish a
  safe ordering; and stale temporary socket directories are reclaimed without
  leaving a dead discovery record under the Sparkwing home.

### Docs

- **sdk-reference:** split the subpackages onto their own pages
  (`sdk-docker.md`, `sdk-git.md`, `sdk-inputs.md`, `sdk-planguard.md`,
  `sdk-services.md`), leaving `sdk-reference.md` as the root package
  plus a linked subpackage index. The single page had reached 103K
  characters, past the ~100K truncation limit of most agent fetch
  tooling, so the subpackages at the end of the page were silently
  invisible to an agent reading it -- exactly what an author needs when
  a pipeline builds an image or reads the branch. The root page is now
  92K. Offline: `sparkwing docs read --topic sdk-<name>`.
- **sdkref:** the generator writes its pages to an output directory
  (`sdkref <repo-root> <out-dir>`) instead of stdout, pruning generated
  pages for subpackages that no longer exist. Only pages carrying the
  generated marker are pruned, so a hand-authored `sdk-*.md` survives.

## [v0.31.0] - 2026-08-12
### Added

- **sdk:** Nodes and groups can declare dependency caches with
  `.CacheDir(sparkwing.GoModules())`, `sparkwing.NpmCache()`, or the generic
  `sparkwing.Dir(path, sparkwing.KeyFromFile(...))`. The declared directory is
  restored before the node runs and saved after its first success, locally
  under `$SPARKWING_HOME/depcache` and on clusters via the cache service's blob
  store, so a build's dependency downloads stay inside the cluster instead of
  egressing every run. Caching is best-effort throughout: a missing lockfile,
  an unreachable backend, or an oversized archive logs a warning and never
  fails the node. This is distinct from `.Cache()`, which memoizes a node's
  result so the node does not run at all; `.CacheDir()` makes the work fast
  while the node still runs. Porting `actions/cache` maps to `.CacheDir()`. See
  [caching](docs/caching.md).
- **cache:** `pkg/cachecontrol` measures the managed pipeline-binary cache and
  reclaims inactive entries within caller-set byte and entry-work bounds. The
  result reports observed capacity separately from removed entries; admission
  callers remeasure the filesystem after every attempt.

### Changed

- **runner:** Dependency fetches now default to the in-cluster pull-through
  proxy. Wherever a cache URL is known, the runner container and every pod it
  spawns start with `GOPROXY`, `npm_config_registry`, `PIP_INDEX_URL`, and
  `PIP_TRUSTED_HOST` pointed at the cache pod's `/proxy/...` routes, so a
  build's dependency traffic stays inside the cluster instead of leaving
  through the NAT gateway once per run. `GOPROXY` keeps `proxy.golang.org` and
  `direct` behind a `|` separator, so any proxy error falls through to
  upstream and private modules still resolve through `~/.netrc`. Opt out with
  `cache.dependencyProxy.enabled=false`, `--dependency-proxy=off`, or override
  a single ecosystem via `runner.extraEnv`. See
  [gitcache](docs/gitcache.md#dependency-proxy-defaults--egress).
- **runner:** Kubernetes node pods are created with
  `imagePullPolicy: IfNotPresent` instead of `Always`, which re-checked (and
  on cache eviction re-downloaded) the runner image once per node in the DAG.
  Deployments that depend on a mutable tag being re-pulled per node need
  `--image-pull-policy=Always` (or `SPARKWING_IMAGE_PULL_POLICY`).
- **cache:** `/archive`, `/file`, `/tree-hash`, `/branch-contains`, and
  `/sync/negotiate` no longer run a synchronous `git fetch` on every request.
  A successful fetch keeps a repo fresh for 15s (`--fetch-fresh-window` /
  `FETCH_FRESH_WINDOW`), which collapses a webhook burst from one upstream
  fetch per request to one fetch; the background loop still bounds staleness.
  `POST /git/refresh` is exempt and always fetches, so the push-then-trigger
  path is unchanged.

### Fixed

- **cache:** A persistently failing mirror fetch no longer re-downloads the
  entire repository on every `/archive` request. Recovery reclones are limited
  to one per hour per repo (`--reclone-cooldown` / `RECLONE_COOLDOWN`); inside
  the cooldown the request fails with the underlying git error and the
  operator fix. Reclones log under a `recovery reclone:` prefix, increment the
  new `sparkwing.gitcache.recovery_reclones` metric, and more than one in 24h
  is reported by `GET /health`.

- **wingd:** Supervised admission daemons idle out again. Since v0.28.0 the
  external supervisor's health probe opened a working connection every two
  seconds, and every connection counted as daemon activity -- so a daemon
  whose only traffic was its own watchdog never reached its idle window, and
  detached supervise+run pairs outlived the throwaway homes that spawned
  them (release hosts, scaffold verifies), charging their CPU to measured
  external load and starving admission for real work on the box. The probe
  now declares itself in its handshake and the daemon answers without
  counting it as activity: a daemon with only probe traffic idles out on
  schedule and the supervisor treats that clean exit as the end of
  supervision. Socket sweeps (daemon discovery) are idle-neutral the same
  way. A probe-declared connection may only read queue state; the daemon
  drops it for anything else, so the accounting exemption cannot be
  claimed by working clients. Supervisors already running from v0.28.x
  keep probing the old way until their pair is killed once.

### Security

- **cli + release:** `sparkwing update` accepts only an Ed25519-authenticated
  checksum manifest and platform asset. It verifies downloaded, staged, and
  installed bytes, restores the prior binary when the final check fails, and
  treats lookup or verification failure as terminal. Releases publish one
  immutable, signed asset set after macOS codesigning.

## [v0.30.0] - 2026-08-12 [YANKED]

Never released. The tag was cut from a branch that never landed on `main`,
its release build failed at asset signing, and no binaries were published.
The Go module proxy cached the tag before it could be recalled, so the
version cannot be deleted; `go.mod` retracts it instead, and `go get
@latest` skips it. Everything intended for it ships in v0.31.0.

## [v0.29.0] - 2026-08-12
### Security

- **cli:** `sparkwing update` now installs only bytes it can prove are the
  release's bytes. The release signs its `SHA256SUMS` manifest with an ed25519
  key and publishes a detached `SHA256SUMS.sig`; the updater verifies that
  signature with a public key compiled into the binary (pure-Go
  `crypto/ed25519`, no external tool) *before* trusting any digit in the
  manifest, checks the download against the signed digest, installs atomically,
  then re-hashes the installed file and requires it to equal the verified
  digest -- restoring the prior binary and failing loudly on a mismatch. The
  macOS ad-hoc codesign moved to the release, before the manifest is hashed, so
  the verified bytes install unchanged: there is no post-verification mutation.
  A signature, digest, download, or install failure is now terminal -- the
  updater no longer falls back to `go install`, which was not bound to the
  release manifest and could install to a different path than the running
  binary. Success reports the installed path, version, and verified digest.
- **release:** The release workflow ad-hoc-signs the macOS binaries with
  `rcodesign` before computing `SHA256SUMS`, signs the manifest with the
  `SPARKWING_UPDATE_SIGNING_KEY` secret via the auditable `cmd/sign-manifest`
  helper, and uploads `SHA256SUMS.sig`. Generate the keypair with `go run
  ./cmd/sign-manifest -genkey`. **One-time bootstrap:** the first release that
  carries a new signing key must be installed out-of-band -- via `bin/install.sh`
  or a direct download-and-verify -- because a CLI built before the key existed
  cannot verify it. This is inherent to key pinning; see
  [security.md](docs/security.md#verified-self-update).

## [v0.28.0] - 2026-08-12
### Added

- **storage:** `fs.LogStore.RunDir(runID)` names the directory a filesystem
  logs backend writes a run's node logs into. The orchestrator records it as
  the run's `log_path`; it is a method on the concrete filesystem store, not on
  the `storage.LogStore` interface, because the object-store, controller, and
  stdout backends have no local directory to name.
- **cli:** `sparkwing runs submit <pipeline>` queues a local run and returns its
  id and log directory immediately, with execution owned by the machine rather
  than the calling terminal. Closing the terminal, dropping an ssh session, or
  killing the submitting process no longer ends the run. The acknowledgment
  comes after the trigger and its pending run row are durable and after a
  resident consumer has taken ownership of the queue, so a printed run id always
  names a run that is recoverable or terminal -- never one that quietly never
  started. `log_path` follows the `run_start` rule: present only when the
  directory exists. `-o json` emits `{run_id, log_path, ...}`; `-o plain` emits
  the bare id.

  Repeat submissions deduplicate through `--idempotency-key`, scoped to the
  pipeline: a second submission of the same pipeline carrying a key an earlier
  one used returns the original run id, its current status, and creates nothing.
  The runs store enforces it with a unique constraint, so two callers racing with
  one key still produce one run. Reusing a key with different arguments is
  refused, because a key names one intent and different arguments are a different
  request. `--request-id` is a separate tracing field recorded on the run that
  never affects deduplication.

  A submitted run executes with the resident consumer's environment, not the
  submitting shell's -- the same rule the admission daemon follows. The
  acknowledgment names the consumer's pid so the difference is visible; see
  [local-execution](docs/local-execution.md) for the ways to supply values a run
  needs.

  Flags a detached run cannot honor -- `--sw-index`, `--sw-ref`, `--sw-dry-run`,
  the other run-shaping `--sw-` flags, and `--profile` -- are refused with the
  reason rather than silently ignored.
- **cli:** `sparkwing runs consumer {start,status,stop}` inspects and controls
  the resident process that executes submitted runs. One consumer serves a
  sparkwing home at a time, elected by a file lock rather than a PID check, so a
  consumer killed with `kill -9` leaves no stale state; it exits on its own after
  five idle minutes. A running dashboard consumes the same queue and stands down
  when a resident consumer holds the lock, so a run is never dispatched twice.
  Work queued while nothing is resident runs when a consumer returns, and a claim
  whose consumer died before its run started is swept back onto the queue once
  its lease lapses. A run that already started is left to the orphan reaper and a
  run that already ended has its claim closed out, so recovery never re-executes
  live or finished work -- the lease is wall-clock while the heartbeat defending
  it is monotonic, so a suspended laptop can lapse the lease of a dispatch that
  is perfectly alive. Stopping a consumer mid-dispatch returns that run to the
  queue rather than failing it.

  A consumer records the sparkwing version it was built from, and a submission
  from a different build replaces it, so an upgrade takes effect instead of the
  first build serving a busy home indefinitely.
- **cli:** `sparkwing runs cancel --run <id>` now cancels a submitted run that no
  consumer has claimed, as a store transaction requiring neither a dashboard nor
  a profile. Cancellation names one run id and cannot reach a resubmission, which
  is a different run with a different id.

### Changed

- **store (Breaking):** The runs-store schema advances from version 12 to 13,
  adding two columns to `triggers` and one index. `idempotency_key` carries a
  caller's deduplication token, under a partial unique index on
  `(pipeline, idempotency_key)` that skips the empty default -- so dedup is a
  database guarantee rather than a race-prone check-then-write, and a key is
  scoped to the pipeline that used it rather than to the whole store.
  `claim_seq` counts how many times a trigger has been claimed, so a dispatch
  whose lease lapsed and was re-taken cannot write an outcome over the run the
  new claim is producing. The upgrade is additive and applies on open; every
  existing trigger carries the empty key and generation zero. As with every
  schema advance, a binary older than this release refuses to open a database
  that has been migrated, so upgrade every sparkwing sharing a runs store
  together. See the [migration
  guide](docs/migrations/v0.28.0.md#runs-store-schema-moves-to-version-13).

  Anyone who ran an interim build of this branch before the schema was
  finalized has a version-13 database missing `claim_seq`, which fails
  submission with `no such column: claim_seq`. That shape was never released;
  delete the development `state.db` (`$SPARKWING_HOME/state.db`, default
  `~/.sparkwing/state.db`) and let it be recreated.

### Fixed

- **wingd:** The daemon log stays bounded between daemon restarts. Rotation
  happened only when a daemon started, while each `SIGUSR1` diagnostics dump
  appends up to 2MB to a daemon that by definition keeps running -- so a
  resident daemon asked for dumps over the weeks between restarts grew
  `wingd/d.log` without limit. A dump now rotates the log to `d.log.1` first
  when it is already past the 1MB cap, using the same once-rotated shape as
  the rotation at spawn. The rotation copies the log aside and empties it in
  place rather than renaming it: the log is a descriptor its writers
  inherited, not a path they reopen, and three processes hold it -- the
  supervisor, the daemon it starts, and the client that opened it -- so a
  rename would strand the ones that did not rotate on the archive and let
  that archive grow without bound instead.

- **cli:** `sparkwing commands --path` accepts the unqualified prefix and
  refuses one that matches nothing. Every command path begins with
  `sparkwing`, so `--path runs` is the spelling a reader reaches for first --
  and it used to match nothing, which is indistinguishable from "this CLI has
  no runs commands"; only `--path "sparkwing runs"` worked. Both forms now
  select the same subtree, matched by whole path components -- `--path run`
  selects `run` and its subcommands rather than also dragging in the separate
  `runs` group. A prefix that selects no command is an error naming
  the prefix with a non-zero exit, instead of an empty listing (or the literal
  `null` under `-o json`) at exit 0; when the prefix matched only hidden
  verbs, the error says to pass `--include-hidden`.
- **orchestrator:** Record `log_path` for runs whose profile uses a filesystem
  logs backend (`logs: {type: filesystem}`). Those node logs are written to the
  executing machine's disk, but the run reported no log path, so readers
  scraped the stream for output that was already on their own disk. The
  invariant is unchanged: a recorded path always names a directory that exists,
  and backends whose logs live behind a URL still record nothing.
- **cli:** Stop `pipeline trigger` from hanging when the controller dies
  mid-run. The log-follow loop discarded every failed status read and polled
  again forever, so a controller that went away during a follow left the
  command waiting indefinitely instead of reaching its existing
  unknown-outcome exit (3). The follow now gives up after the run's status has
  been unreadable for 60 seconds and reports the transport error; any
  successful poll in between resets the clock, so a replica rolling out is
  still ridden through. This also bounds `sparkwing runs logs --follow`, which
  shares the loop: a controller outage lasting more than 60 seconds now ends
  the follow with a non-zero exit instead of tailing nothing indefinitely.
- **orchestrator:** Make argument and guard semantics agree across every entry
  point. A run row records only the arguments the operator passed; the
  `defaults.args` and pipeline `args:` layers are re-read from the checkout the
  run executes out of, at that checkout's revision (for a retry, the source
  run's recorded revision). Only `sparkwing run` performed that read. Queued
  triggers -- which is every dashboard run and retry, the local trigger loop,
  and every spawned child -- planned with unmerged arguments and skipped guard
  evaluation entirely, and the cluster node entrypoint and `debug replay`
  planned with unmerged arguments too, so a `secret:"true"` input supplied by
  `sparkwing.yaml` was never registered with the executing side's log masker.
  All four paths now resolve the same layers, with the operator's explicit
  arguments still winning per key. Behavior change: a trigger whose arguments
  come from `sparkwing.yaml` now runs with them and is subject to the
  pipeline's guards.
- **orchestrator:** Evaluate `arg:<flag>=<value>` guards against the arguments
  the run actually executes with. Guards were judged on the caller's arguments
  alone, so a value supplied by the project's `defaults.args` block or a
  pipeline entry's `args:` block was invisible to them and a guard written to
  reject a protected target waved the dispatch through. Guards now read the
  same merged set the pipeline is invoked with, with the command-line flag
  still winning per key. Behavior change: a guard that silently passed because
  its value came from `sparkwing.yaml` now fires; a guard on an argument no
  layer supplies is unaffected.

## [v0.27.0] - 2026-08-12
### Security

- **orchestrator:** Redact secret values that a run's arguments inherit from
  `sparkwing.yaml`. A `secret:"true"` input can take its value from the
  project's `defaults.args` block or a pipeline entry's `args:` block instead of
  a command-line flag; the run's log masker was seeded from the arguments the
  caller passed, while the pipeline was invoked with those layers merged in. A
  secret supplied only by the project config was therefore never registered for
  redaction, and a job that logged it wrote the plaintext to the node log, the
  persisted annotations and summaries, the node's failure excerpt, and the
  `child_run_start` audit payload -- while the same secret passed as a flag was
  redacted. The masker is now seeded from the merged arguments the run actually
  executes with. Already-written logs and rows are unchanged: rotate any secret
  a job logged that reached it through `sparkwing.yaml`.
  Display surfaces were never affected. A yaml-supplied value is not recorded on
  the run row, and the secret-argument classification comes from the pipeline's
  declared input names rather than from the values, so `runs list`, `runs get`,
  `runs status`, the controller endpoints, and `run_start` redacted correctly
  throughout.

### Changed

- **cli + orchestrator (Breaking):** Daemon hosting moves from compiled
  pipeline binaries to installed sparkwing binaries. The invariant: the
  installed Sparkwing distribution owns daemon lifecycle; pipeline clients
  declare required capabilities and use the running daemon, but never host,
  replace, or upgrade it. A pipeline binary no longer serves the hidden
  `wingd` verbs and no longer takes a running daemon over -- it shares
  whatever daemon is serving. `sparkwing run` sets `SPARKWING_WINGD_BIN` to
  its own path before exec'ing the pipeline binary (an exported value of
  your own survives) and brings the daemon to its own version first, for
  invocations that will actually admit work. A pipeline binary invoked
  directly falls back to the `sparkwing` on PATH, and starts the daemon
  through that installed binary rather than through itself.

  The point is that a repo's `.sparkwing/go.mod` pin is no longer part of
  the machine-wide daemon version negotiation: one repo bumping its SDK can
  no longer churn the daemon every other repo on the box shares.

  On a box where no daemon is available, a run now proceeds uncoordinated
  with one stderr warning, which keeps a pipeline binary shipped alone to a
  deploy box a working product. It loses host arbitration only: CPU and
  memory charges are not held, so concurrent runs there can oversubscribe
  the machine. `.Concurrency()` groups are unaffected -- box- and
  run-scoped groups are enforced through the shared store instead of the
  daemon, so "one deploy at a time on this box" still holds, with weaker
  crash cleanup (a killed run's slot waits for `sparkwing doctor` rather
  than being freed when its socket closes).

  Two cases fail instead of degrading. A pipeline that reserves host
  capacity with a plan- or node-level `.Resources()` pin fails, because CPU
  and memory have no fallback arbiter and there is no weaker version of
  that reservation to fall back to. And a daemon that *is* running but
  speaks an older protocol than the pipeline binary fails every run, pinned
  or not: it is actively holding capacity for other work, so joining it
  unadmitted oversubscribes the box. Both errors name the fix.
  `SPARKWING_ALLOW_UNADMITTED=1` (exactly `1`) forces the uncoordinated
  path in either case; `--sw-dry-run` is exempt from both. See [migration
  guide](docs/migrations/v0.27.0.md#daemon-hosting-moves-to-installed-binaries).

  `sparkwing.ToolSlot` changes shape on such a host: it returns its
  documented `granted=false` fallback, where a headless pipeline binary
  used to self-host a daemon and be granted the budget. Job bodies already
  have to handle that return, and fall back to whatever serialization the
  tool ships with.

  **Migration.** Nothing changes for anyone with the sparkwing CLI
  installed and on PATH, which is the normal laptop and CI setup. Hosts
  running a pipeline binary with no sparkwing installed keep working unless
  their pipelines pin `.Resources()`, which now needs the CLI installed (or
  `SPARKWING_ALLOW_UNADMITTED=1`). Pipeline binaries built against an older
  SDK are unaffected: they keep their existing self-hosting behavior and
  talk to a newer CLI-hosted daemon exactly as before, so a mixed box
  during a rollout is fine.

  One edge is worth naming, and it covers every release so far: no
  sparkwing through v0.26.0 can host a daemon for a pipeline binary built
  against this SDK, because none of them serves `wingd supervise`. A host
  whose installed CLI predates this release will refuse to host, and the
  error names the release to install. Update the CLI to v0.27.0 or newer on
  any host that runs pipeline binaries built against this SDK.

### Fixed

- **wingd:** The admission daemon is now supervised: it is started as a
  child of a watchdog process that replaces it if it stops answering
  health probes. A daemon that has stopped scheduling cannot run its own
  recovery, so only another process can bound it. Both the supervisor and
  the spawn that reaches it were added after v0.26.0 was cut, so no
  release has shipped an unsupervised-then-supervised daemon -- this is
  the first release with one at all. It works because spawning now starts
  an installed binary and both the CLI and `sparkwing-runner` serve both
  `wingd` verbs; a compiled pipeline binary, which serves neither, is
  never asked to. A test in each hosting binary pins that it serves the
  verb the spawn invokes, and the headless gate pins that a pipeline
  binary is never asked to.
- **runner:** `sparkwing-runner --local-admission` can bring up the
  admission daemon it depends on. It served only `wingd run` while its own
  spawn path invoked the supervisor, so its spawn answered itself with a
  usage error and no daemon appeared. Unreleased regression: the spawn
  verb it collided with was added after v0.26.0 was cut.
- **wingd:** A daemon host that fails to start now fails the run promptly
  instead of after the full thirty-second socket wait, with an error
  naming the binary that was started and the tail of whatever it wrote. A
  named-but-unstartable host -- a typo'd `SPARKWING_WINGD_BIN`, a stale
  path -- reports the variable and its value rather than "could not reach
  the admission daemon", which sent the reader to inspect a daemon when
  the fault was a path they typed. A spawn that never starts a process no
  longer leaves an empty `d.log` behind implying one ran.

## [v0.26.0] - 2026-08-12
### Added

- **cli + orchestrator:** Runs now report where their logs live instead of
  making callers reconstruct the path. The `run_start` record carries a
  top-level `log_path` attribute -- the run directory holding the per-node
  `.log` files, on the machine that executed the run -- and the same value is
  persisted on the run's invocation snapshot, so it also shows up in
  `sparkwing runs status` (one `log_path:` line, or a top-level `log_path`
  field under `-o json`), `sparkwing runs get`, and the run receipt. Only runs
  that write logs to a filesystem carry it; runs logging to a controller or an
  object store omit the field rather than name a directory that holds nothing.
  Because the path is the executor's, a run read back through `--profile` may
  name a directory that is absent locally: the text output marks those, while
  the JSON reports the path as recorded.
- **wingd:** `SIGUSR1` makes the daemon write a full goroutine dump plus
  a one-line count of its connections, holders, waiters, leases, and
  guards to its log
  (`~/.sparkwing/wingd/d.log`). A daemon that is burning CPU can now be
  explained while it is still running -- `kill -USR1 <pid>`, then read
  the log -- instead of only after the operator has killed the evidence.
  POSIX only; Windows has no such signal.

### Fixed

- **cli:** `sparkwing pipeline trigger` without `--detach` now exits on the
  triggered run's outcome instead of always exiting 0. The follow ended when the
  run reached a terminal state but never read its status, so a failed remote run
  printed no failure and reported success to the CI job or script wrapping it.
  Exit codes now match a local `sparkwing run`: 0 for success, 1 for failed or
  cancelled. Both follow modes -- log streaming and node status -- print the
  run's status block and failing-node errors to stderr, so the reason survives a
  `> run.log` redirect (the status follow also renders to stdout as it polls, so
  a terminal shows that block twice). A follow that ends without a readable
  terminal status, including a controller that becomes unreachable mid-run,
  exits 3 and names the run to re-check with `sparkwing runs status` rather than
  reporting a possibly-succeeding run as failed. `--detach` is unchanged -- it
  reports submission, not outcome. Scripts that relied on the old always-zero
  exit need `|| true` to keep it.
- **wingd:** A daemon watching guarded runs no longer forks `ps` once per
  guarded session per tick. Each sweep now takes a single kernel process
  listing and judges every session -- membership and leader identity --
  against that one view. On macOS the listing comes from `kern.proc.all`
  rather than a `ps` fork: a full snapshot measured 0.7ms against 34ms on
  a laptop with 669 processes, and the per-session identity lookups it
  replaces are gone entirely. This is the largest contributor to the
  reported case of a daemon pinned near 200% CPU while the queue appeared
  hung.
- **wingd:** Guard sweeps back off exponentially (to 5s) while the daemon
  cannot read the process table at all, and return to full cadence on the
  first success; a single guarded run whose inspection fails no longer
  slows the sweep for the others, and a guarded leader that simply exits
  between two observations is read as the answer it is rather than as a
  failure. A process table the daemon cannot read used to be retried ten
  times a second forever.
- **wingd:** The `ps` listing -- used on Linux and other Unixes, and as
  the macOS fallback -- is now bounded at two seconds, so a wedged `ps`
  fails the inspection instead of blocking the daemon goroutine that
  asked for it. A daemon that drops to that fallback says so once in its
  log, since the fallback costs about fifty times more per listing and
  rarely recovers. The post-`SIGKILL` wait for a process tree
  to disappear backs off from 10ms to one second and logs once when it
  slows down, so a descendant that cannot be killed costs about one
  process-table read per second rather than a hundred.
- **wingd:** Runs and CLI commands that lose their connection to the
  daemon retry with capped exponential backoff and jitter instead of
  reconnecting as fast as the socket allows. Every dropped connection
  makes the daemon persist its state, so an unpaced client used to spin a
  core on each side. Admission-critical exchanges (acquire, re-attach,
  guard watch, cancel) still retry until they succeed or the caller gives
  up; read-only ones (`sparkwing queue`, stats reset) now fail with a
  clear error after ten attempts rather than retrying forever. The waits
  inside connect -- for a spawned daemon's socket to appear, and for a
  predecessor daemon to release the election during an upgrade -- are
  paced the same way, keeping their thirty-second budgets while dialling
  a few times a second instead of twenty.
- **wingd:** A client that keeps finding the same older daemon gives up
  after three takeover attempts, names both versions, and points at
  `sparkwing daemon restart`, instead of draining and respawning in an
  unbounded loop when the successor comes back as the version it
  replaced. Meeting a different old daemon starts the budget again, so
  losing a socket race to several predecessors is still resolved.
- **wingd:** Frames the daemon sends a client are bounded by a ten-second
  write deadline. A client that stops reading without closing its socket
  is treated as gone rather than holding a daemon goroutine for as long
  as it lives.

### Changed

- **orchestrator:** A failed node's recorded error is now bounded and redacted.
  Previously a failing `sparkwing.Bash`/`Exec` step stored the command's entire
  stderr *and* the entire command in the node's error -- both unbounded and
  unmasked, so a large compiler run, or a generated script, turned a state row
  into hundreds of kilobytes that `runs status`, the dashboard, and every
  notification had to carry. The whole error text now has a hard ceiling of
  about 5 KiB: the last 20 lines (at most 4 KiB) of command output, led by a
  headline trimmed to one short line, with secret values redacted throughout.
  When output was dropped the text says so and names the command that prints
  the rest:
  `… earlier output omitted (see: sparkwing runs logs --run <id> --node <id>)`.
  An error with no captured output keeps its *first* lines instead -- there the
  message is the diagnostic and its opening names the problem -- and points at
  no log, because the log does not contain it. The node log still holds the
  full output; cancelled and upstream-failed nodes are untouched.
- **cli:** `sparkwing runs errors -o json` and `sparkwing runs status -o json`
  carry the excerpt as structured data per failed node: `log_excerpt` (the
  masked bounded output, without the headline and marker that decorate the
  error text) and `log_excerpt_truncated`. Both fields are absent together when
  there is nothing to excerpt -- a failure with no captured output, or a node
  that did not fail on its own -- so absence is reportable rather than
  fabricated. Where absence itself cannot be established (an event stream too
  large to scan, or a controller that will not serve it) the node carries
  `log_excerpt_unavailable` instead of a silent gap. Excerpts ride a new
  `node_failure_excerpt` run event, so local and controller-backed reads render
  identically; runs mirrored to S3-backed state carry no events and so no
  excerpts. See [observability](docs/observability.md#failure-excerpts).

### Security

- **orchestrator:** Redact secret values in persisted node and step annotations
  and summaries. The node log's masking wrapper sat *inside* the wrappers that
  write `sparkwing.Annotate` and `sparkwing.Summary` output to the state store,
  so those wrappers persisted the record before it was redacted: the log file
  showed `***` while the annotation and summary rows beside it -- read by
  `runs status`, `runs summary`, and the dashboard -- held the plaintext value.
  The masker is now outermost. Rotate any secret a job passed to `Annotate` or
  `Summary`; existing rows are unchanged.
- **orchestrator:** Redact secret values in the structured attributes of node log
  records, not just their messages. A failed step reports the command's whole
  error text in `attrs.error`, so a secret that reached a command line or its
  output was persisted in the node log one line below the redacted message --
  on every execution path, local included. String attributes are now masked,
  including strings nested in lists and maps; anything nested deeper than the
  redaction pass inspects is replaced wholesale rather than emitted.
- **orchestrator:** Redact secret values in node logs written by cluster and pod
  node execution. `run-node` built the per-run masker from the pipeline's secret
  arguments and wired it into secret resolution, but never installed it on the
  context the node log wrapper reads, so a node running on a runner or in a pod
  persisted secret values in plaintext where the same node run locally redacted
  them. Already-written logs are unchanged: rotate any secret a job logged from
  a remote node.
- **cli, controller:** pipeline inputs tagged `secret:"true"` are now redacted to
  `***` wherever a run is read back. Previously the tag only masked node log
  bodies, so a secret passed as an argument was printed in full by the `Setup`
  block of `sparkwing run`, by `runs list`, `runs get`, `runs status`,
  `runs find`, `runs tree`, `runs wait`, and `runs receipt`, served by the
  controller's run endpoints, and rendered by the dashboard's Setup panel --
  including the copyable `rerun` reproducer command. Runs now record which
  arguments their pipeline declared secret, and the read paths redact them;
  retry and replay still re-execute with the real value. Audit events
  (`child_run_start`) that forward a parent's arguments to a child are masked
  too.
  Runs started before this release carry no such record and render unchanged.
  Redaction is applied on read: the run row, its backups, and `state.ndjson`
  dumps still hold the plaintext, so keep treating the state database as
  secret-bearing. In a mixed-version fleet an older CLI or controller reading
  the same database still renders those arguments in full and still computes
  the pre-change `receipt_sha`, so upgrade every reader before relying on this.
  Deliberately not covered. Trigger rows carry no classification and the
  dispatch path serves them to runners verbatim, so
  `sparkwing runs triggers get|list` and `GET /api/v1/triggers` still show
  argument values. A run pre-allocated by a fresh trigger is listable in
  `pending` before a worker starts it, and the controller holds no pipeline
  schema to classify it from, so it is unredacted for that window -- which
  includes a child retry spawned through a trigger's `retry_of`; only
  `POST /api/v1/runs/{id}/retry` and replay inherit their source's
  classification directly. And redaction of the reproducer is anchored on the
  argument name, so a secret value also passed to a non-secret argument is
  masked in logs but shown under that other name.
- **controller:** `GET /api/v1/runs/{id}` accepts `?include=secret_values`,
  which returns a run's real argument values instead of the redacted ones. It
  is honored only for tokens carrying `nodes.claim` or `admin`, because cluster
  executors fetch the arguments they run with from this endpoint; every other
  caller, the dashboard included, keeps getting the redacted view.

## [v0.25.0] - 2026-08-11
### Added

- **cache:** `sparkwing cache info` and `sparkwing cache prune` inspect and trim
  the compiled pipeline binary cache. The cache is now bounded rather than
  unbounded: after each compile, least recently used binaries are evicted to fit
  `SPARKWING_CACHE_MAX_BYTES` (default `2GiB`) and `SPARKWING_CACHE_MAX_ENTRIES`
  (default `20`), either set to `0` to disable. Eviction ranks entries by last
  use rather than build time. Kernel-backed execution and writer leases prevent
  eviction of active entries.
- **cache:** `sparkwing cache explain` prints a pipeline's cache key, whether it
  is cached, and every input behind it with its own digest and file counts --
  including how many files git ignored and excluded, which is the usual reason
  an edit does not trigger a rebuild. When other cached entries came from the
  same checkout, it names the inputs that changed since, answering why the last
  run recompiled.
- **cache:** cached binaries record which checkouts have used them and how many
  times, shown by `sparkwing cache info`. A cache key is a content fingerprint
  and `-trimpath` keeps build paths out of the binary, so without this an entry
  cannot be identified; entries listing more than one checkout are the
  path-independent key paying off.

### Changed

- **cache:** the pipeline binary cache key no longer depends on where a checkout
  lives. Local `replace` targets are recorded by module path instead of absolute
  filesystem path, a covering `go.work` is folded in by its normalized directives
  rather than its raw bytes, and builds pass `-trimpath`. Two checkouts of the
  same commit now compute the same key and compile to byte-identical binaries, so
  a worktree reuses the primary checkout's build instead of making its own.
- **cache:** files git ignores are excluded from the pipeline binary cache key.
  Untracked local debris -- provider plugins, release outputs, coverage data --
  no longer perturbs the key or costs time to hash, which also makes the key
  reproducible across machines and CI runners. Directories outside a git
  repository still hash everything. Set `SPARKWING_HASH_ALL_FILES=1` to restore
  full hashing if a build depends on a gitignored file, such as a generated asset
  pulled in by `//go:embed`.

  These two changes invalidate existing cached binaries once; the next
  invocation of each pipeline recompiles.

## [v0.24.0] - 2026-08-10
### Added

- **local admission:** `sparkwing queue exec` admits bootstrap commands before
  they begin and binds each grant to the command's Linux or macOS process
  session. Supervisor death and daemon restart retain capacity until the full
  command tree stops; unsupported platforms refuse before admission.
- **daemon recovery:** `sparkwing daemon recover-state --yes` preserves an
  unreadable state file after the operator verifies guarded commands stopped,
  providing an explicit escape from fail-closed startup without silently
  discarding unknown lease authority.
- **commands:** `sparkwing commands -o markdown --split-dir DIR` writes the
  CLI reference as one page per top-level command group plus a
  `cli-reference.md` index, pruning generated pages for groups that no longer
  exist. `bin/gen-cli-docs.sh` and the pre-push drift gate use it.

### Docs

- **cli-reference:** split into one page per command group (`cli-runs.md`,
  `cli-pipeline.md`, ...) with `cli-reference.md` as the index. The single
  page had grown to 155K characters, past the ~100K truncation limit of most
  agent fetch tooling, so commands late in the alphabet were silently
  invisible to agents reading the page; the largest per-group page is ~35K.
  Offline: `sparkwing docs read --topic cli-<group>`.
- **docs:** repository-wide accuracy audit. Every documentation surface was
  verified against the code and corrected where it had drifted; the largest
  fixes: the `Plan` interface signature and registration pattern in
  getting-started, the README's public-API map (`pkg/...` is covered API, not
  "may change at any time"), `schedule:` triggers documented as declarative
  (nothing evaluates the cron; drive it with an external timer),
  `run <pipeline> config` documented as the declared-secrets view, the token
  `--scope` example corrected to real scopes, and the GitHub Actions
  ci-embedded example rewritten around committed profiles with the real
  `--sw-mode` / `--sw-workers` flags.
- **docs:** the doc gates now also cover the surfaces outside `docs/`:
  root-level markdown, chart/install/web READMEs, pipeline registrations in
  `.sparkwing/`, `examples/`, and `docs/_sidebar.json` completeness (both
  directions, stale exclusions included).

### Fixed

- **controller:** `POST /api/v1/tokens` rejects unknown scopes with a `400`
  naming the offending scope and the valid set, instead of minting a token
  that authenticates but fails every scope check with `missing_scope`.
- **cli:** `pipeline sparks lint` accepts a multi-module `spark.json`: a
  manifest declares exactly one of `packages[]` (a library that is one Go
  module) or `modules[]` (a monorepo of independently tagged modules), and
  each `modules[]` entry's Go module path is cross-checked against that
  directory's `go.mod`. The path argument is also accepted positionally, as
  the command's own examples show it.
- **queue:** External CPU now subtracts measured live holder process-tree usage
  instead of reserved lease capacity. Process reuse, overlapping trees, sensor
  loss, and macOS sampling no longer make queue headroom contradict host load.
- **queue:** Internal nodes and barriers now retain their owning run's original
  queue rank, so a newer run cannot overtake an older live run by submitting
  its work first. The daemon verifies the live owner lease before applying
  that rank; invalid or stale ownership claims keep ordinary arrival order.
- **queue:** Start-time estimates now remain unknown when an active holder has
  outlived its measured duration, instead of promising immediate admission
  while capacity is still occupied.
- **queue:** Start and clear estimates now simulate host and semaphore
  constraints together, including atomic multi-resource admission and
  backfill. A waiter blocked by a semaphore no longer reports the earlier
  host-only estimate, and unknown or overflowing resource bounds stay unknown.
- **queue telemetry:** Run listings retain concurrent node admission waits,
  distinguish plan-level admission from node admission, and correlate terminal
  events with the matching request. Interleaved or stale events no longer erase
  a live wait or make a run-level queue position look like node execution.
- **local admission:** Explicit cancellation is persisted before execution is
  signalled, applies atomically to every member of a shared lease, survives
  daemon restart and connection replacement, and cannot resurrect a terminal
  run through admission or reattachment.
- **local admission:** Guarded session inspection now retains capacity when a
  recycled leader PID coexists with live session members, ordinary unguarded
  state remains readable by the previous release, and disconnected
  cancellation gets the same cooperative cleanup window as its supervisor.
- **queue telemetry:** Semaphore constraints now appear in the blocking reason
  instead of leaving a blocked waiter with only its host-capacity explanation.
- **queue:** External CPU now subtracts measured live holder process-tree usage
  instead of reserved lease capacity. Process reuse, overlapping trees, sensor
  loss, and macOS sampling no longer make queue headroom contradict host load.
- **queue:** Internal nodes and barriers now retain their owning run's original
  queue rank, so a newer run cannot overtake an older live run by submitting
  its work first. The daemon verifies the live owner lease before applying
  that rank; invalid or stale ownership claims keep ordinary arrival order.
- **queue:** Start-time estimates now remain unknown when an active holder has
  outlived its measured duration, instead of promising immediate admission
  while capacity is still occupied.
- **queue:** Start and clear estimates now simulate host and semaphore
  constraints together, including atomic multi-resource admission and
  backfill. A waiter blocked by a semaphore no longer reports the earlier
  host-only estimate, and unknown or overflowing resource bounds stay unknown.
- **queue telemetry:** Run listings retain concurrent node admission waits,
  distinguish plan-level admission from node admission, and correlate terminal
  events with the matching request. Interleaved or stale events no longer erase
  a live wait or make a run-level queue position look like node execution.
- **local admission:** Explicit cancellation is persisted before execution is
  signalled, applies atomically to every member of a shared lease, survives
  daemon restart and connection replacement, and cannot resurrect a terminal
  run through admission or reattachment.
- **local admission:** Guarded session inspection now retains capacity when a
  recycled leader PID coexists with live session members, ordinary unguarded
  state remains readable by the previous release, and disconnected
  cancellation gets the same cooperative cleanup window as its supervisor.
- **queue telemetry:** Semaphore constraints now appear in the blocking reason
  instead of leaving a blocked waiter with only its host-capacity explanation.
- **release:** Self-release validation now requires an isolated
  `SPARKWING_HOME`, preventing a prerelease binary with a newer embedded schema
  from migrating the operational runs store before installed readers upgrade.

## [v0.23.1] - 2026-08-09
### Fixed

- **store (Breaking):** The runs-store schema advances from version 11 to 12
  to persist child-trigger repository provenance. Every process that opens a
  shared local store must run v0.23.1 or later before the store is migrated.
  See the [migration guide](docs/migrations/v0.23.1.md#runs-store-schema-moves-to-version-12).
- **cache:** An in-flight-dedupe follower now runs after its leader fails,
  is cancelled, or otherwise ends without a reusable result. A leader's
  non-success outcome can no longer become the follower's verdict.
- **local execution:** Same-repository child pipelines execute from the
  parent's checkout even when repository metadata is inherited. A damaged or
  missing cross-repository registry no longer blocks that handoff. The
  exported `store.Trigger.RepoInherited` field records that routing provenance
  for trigger backends.
- **orchestrator:** Admission waits pause the dispatch watchdog only for the
  nodes they cover. Legitimately queued child pipelines no longer time out
  their parent, while an admitted sibling that stops making progress still
  reaches the dispatch deadline.
- **queue telemetry:** Local admission messages identify the run and node for
  each reported position, so interleaved waiters cannot look like one job
  moving backward in the queue.
- **release:** Independent gates finish before the changelog commit, failed
  releases restore a tidy development module, and Markdown lint uses a pinned
  self-provisioning command.

## [v0.23.0] - 2026-08-08
### Added

- **web/chart:** `sparkwing-web --cache <url>` adds sparkwing-cache to the
  services health panel, and the `sparkwing-full` chart wires it up. The cache
  reports background fetch failures and an unwritable cache directory on
  `GET /health`, and nothing was probing it, so a cache whose fetch loop had
  stalled was invisible while runner builds quietly degraded to cold clones.
  The new `web.cache.url` value defaults to the bundled runner-bundle's cache
  Service exactly as `web.logs.url` defaults to its logs Service; a release
  that disables the bundled cache starts the web pod without the flag. The flag
  is probe-only and defaults to empty, so a dashboard that is not given a cache
  URL shows the same panel it always did. `sparkwing-full` is version `0.1.1`
  for the new value.
- **cli:** `--help` is now honored wherever it appears. A value-taking flag
  consumes whatever follows it, so `sparkwing pipeline new --template --help`
  bound `--help` as the template name and then failed on an unrelated missing
  `--name`. The request is answered before parsing; arguments after a `--`
  terminator are still operands.
- **cli:** `sparkwing examples` prints one line per entry -- name and
  when to reach for it -- instead of every manifest in full, and lists the
  built-in shapes alongside them. The catalog was 606 lines, which is not a
  list anyone reads: agent trials grepped it, re-dumped it as JSON, and parsed
  it with python before picking. Choosing needs a name and a line; the rest is
  behind `--name`. The shapes are marked as DAG skeletons with echo bodies,
  because `ci-pr-check` is named for a job a registry template actually does
  and reads as the answer until you open it.
- **cli:** (Breaking) the sparks-core registry moved off the creation path.
  `sparkwing examples` browses it; `pipeline new --template` now takes only a
  shape (`minimal`, `build-test-deploy`, `ci-pr-check`, `release`,
  `scheduled-report`) and rejects a registry name. Templates and examples were
  one list doing two jobs: agent trials spent a quarter of their turns
  choosing between forty entries, most of which are thin wiring over
  sparks-core libraries rather than a place to start. A shape is what you
  scaffold; an example is what you read, and `docs search` is how you find one.
  See [the migration guide](docs/migrations/v0.23.0.md#pipeline-templates-is-now-sparkwing-examples-and---template-takes-a-shape).
- **cli:** `pipeline sparks vendor` is now `pipeline sparks inflate` -- it
  copies a spark library's source into your repo so you can edit it, which is
  not what "vendor" says.
- **cli:** `docs search` now answers from the example corpus as well as the
  docs. The registry pipelines are working, verified code, which answers "how
  does someone do ECS Fargate" more directly than a reference section defining
  the symbols involved -- and unlike prose they are executed, so they cannot
  quietly stop being true. They surface through search rather than a listing
  on purpose: choosing from a catalog is the expensive part, consulting one
  that search handed you is not.
- **cli:** `docs search` now returns the matching **sections** -- topic, heading,
  line range, snippet -- instead of a list of whole topics, with `--body` to
  print them in full. The reference pages run to tens of thousands of tokens
  while the question is usually narrow (what a `pull_request` trigger looks
  like, what fields `ApprovalConfig` has), and answering it used to mean
  reading a page, grepping it for line numbers, and slicing out the range.
  A heading hit outranks a body hit and a shorter section outranks a longer
  one, so the defining section ranks first. `--topics` restores the previous
  whole-topic listing.
- **cli:** `sparkwing docs guides` and `docs read --guide <name>`: named sets of
  topics that answer one task together, for when reading a single page leaves
  you a lookup short. The first is `authoring` -- the DAG model, the idioms the
  linter enforces, how a pipeline fires, and the config schema -- four pages in
  one call rather than four calls. `docs read --topic` is unchanged and still
  returns exactly the page asked for. Guides carry narrative topics only; the
  generated references are lookup tables, and `docs search` is the path to
  those.
- **agent discovery:** `info --for-agent` now emits current, one-wake context,
  structured `info` includes `capability_epoch`, and new pipeline scaffolds
  point agents at the live command instead of copying a command catalog.

- **dashboard:** timestamps say which day they belong to. The run header,
  Setup, Summary, the selected node, the approval banner, the attempts
  dropdown, and the home page's approval and last-deploy rows now render a
  dated stamp instead of a bare clock or a bare "66d ago", with the year
  shown only when it isn't the current one. Setup gained a `finished` row.
  Log lines keep their bare clock, which is the right default when every
  line in a run shares a date, and gain a `date` toggle beside `ts` for the
  runs where it doesn't -- an old run, one that crossed midnight, output
  read next to another day's. Copied and downloaded logs always carry the
  full date regardless of the toggle.

- **sdk:** Work DAGs can declare `w.ParallelFailures(sparkwing.FailFast)` for
  short local feedback or `sparkwing.CollectAll` for comprehensive diagnostics.
  Fail-fast siblings now finish as cancelled instead of failed, telemetry names
  the decisive step and cancellation latency, and `.Finally()` cleanup steps
  run after their declared dependencies terminate without inheriting sibling
  cancellation. Collect-all preserves hard dependency semantics: a failed
  prerequisite unlocks only `.Finally()` cleanup or a dependent explicitly
  enabled by `.ContinueOnError()`. The zero value remains fail-fast for
  compatibility.

- **daemon:** `sparkwing daemon status -o json` reports whether the local
  admission daemon is running and names its exact source revision when known.
  `sparkwing daemon restart` refreshes only an already-running daemon through
  the existing drain and lease-reattachment path; a stopped daemon stays
  stopped. Post-merge self-heal uses this surface so the next pipeline no
  longer pays the takeover cost, and release-pinned pipelines leave the
  refreshed source build resident.

- **sdk:** `sparkwing.AcquireLintSlot(tool)` lets worktrees of one repo share a
  golangci-lint cache without being told about each other's files. golangci-lint
  keys a cached result on the import path and file content, deliberately
  stripping the main module's directory, but stores the absolute filename of the
  tree that produced the finding. Point two worktrees at one cache directory and
  every replayed finding names the other tree -- 49 of 49 on sparkwing -- and
  once that tree is deleted it names files nobody can open. A slot is a fixed
  path, a symlink repointed at whichever worktree holds an exclusive lease, plus
  the cache that goes with it, so every run sees one path and a replayed
  filename lands under the current holder. Measured on 205k Go lines at load
  18-32: a second worktree pays 94.65s with its own cache and 2.42s through a
  slot, reporting only its own files. Nothing is hidden -- content is part of
  the cache key, so a planted violation was still caught, warm, in 1.76s. Use
  `slot.Configure(cmd, "GOLANGCI_LINT_CACHE")` rather than setting the
  directory by hand: it sets `PWD` too, and without that Go's `os.Getwd`
  resolves the symlink and the slot quietly does nothing. A lease always
  succeeds -- with every slot busy it hands back the private `ToolCacheDir`,
  cold and no less correct, and `Canonical` says which. `SPARKWING_LINT_SLOTS`
  sets the pool size, default 4. A repo with more than one Go module takes one
  lease and uses `slot.ConfigureIn(cmd, module, "GOLANGCI_LINT_CACHE")` per
  module; a path that climbs out of the lease is ignored rather than honored,
  because a command run outside the canonical path writes the worktree's own
  paths into the shared cache.

- **cli:** the machine budget resolves from `~/.config/sparkwing/budget` (or
  `$XDG_CONFIG_HOME/sparkwing/budget`) as well as `SPARKWING_BUDGET` and the
  daemon's `--budget` flag, in that precedence: flag, then environment, then
  file. The admission daemon is started on demand by whichever run needs it
  first and inherits that process's environment, so an exported budget applies
  to whatever daemon that shell happened to spawn and dies with it. A budget in
  the file is read at every daemon startup, so it survives a kill and respawn
  and needs no shell to have exported anything. The file holds one setting
  line; blank lines and `#` comments are skipped, so the reason a budget is in
  force can live beside it. A value that will not parse fails daemon startup
  rather than being dropped, whichever setting it came from.

- **cli:** `sparkwing queue` names the setting the active budget came from
  (`budget 6.0 cores (machine 10.0) (from config ~/.config/sparkwing/budget)`),
  and says so when no budget is set anywhere rather than staying silent -- a
  machine admitting against everything it has reads exactly like a deliberate
  whole-machine budget otherwise. The `external: ignored` line names its source
  too. `sparkwing doctor` reports a non-default budget with the setting behind
  it, including one that ignores external load, and flags a budget that came
  from an environment the next respawn may not carry.

- **wingwire:** `BudgetState` gained `Source`, `Origin`, `Raw`, and
  `IgnoreExternal`, and the daemon now reports budget state whether or not a
  budget is set. An absent `BudgetState` means the daemon did not describe its
  budget, which is not the same answer as "no budget set".

- **sdk:** `sparkwing.ToolSlot` takes a named box-wide budget from inside a
  running job, for external tools that ship their own machine-wide lock the
  admission daemon cannot see. A waiting step reports its queue position the
  way a queued node does. `sparkwing.BoxToolBudget` builds the matching
  `ConcurrencyGroup`, counted in hundredths of a core so widening what a tool
  covers re-derives the concurrency from one measured number instead of a
  hand-tuned slot count.
- **cli:** `sparkwing queue` estimates a start time for a run waiting on a
  named semaphore. Previously only host-capacity waits carried an ETA, so a
  step queued behind a tool budget could report its position but never how
  long.

### Changed

- **cli:** `pipeline explain` now reports what fires a pipeline, and says so
  plainly when nothing does. A trial produced a pipeline for "run go test on
  pull requests" that declared no trigger: it compiled, linted clean,
  explained clean, ran green, reported no friction, and was the fastest run in
  its sweep -- every signal said it was the best result in the set.
  `branches` / `paths` / `actions` filters are marked as recording intent,
  since nothing reads them today. Only per-pipeline facts appear: that
  delivery depends on a GitHub webhook is true of every such trigger in every
  repo, so it belongs in the docs rather than in every explain, and
  `sparkwing cluster webhooks list` is what answers it for a real repo.
  Nothing here changes an exit code -- it reports rather than judges, since a
  manual-only pipeline is legitimate, which is also why it is not a lint
  rule.

- **sdk/lint:** new `group-cache-shared` rule. `JobGroup.Cache` applies one key
  function to every member, so a `JobFanOut` matrix stores one result and
  replays it for every cell -- a matrix over Go 1.23 and 1.24 serves 1.24's
  pass for both. It presents as a fast green run, so nothing downstream catches
  it. The finding names `Members()` as the way to key each job, which an agent
  previously had to find by reading the SDK reference.

- **cli:** `pipeline new --on` accepts several triggers -- `--on
  push,pull_request`, or the flag repeated. `on:` is a map and real workflows
  use more than one key; a migration trial reproducing a workflow that fires
  on both had to hand-edit the yaml the scaffolder had just written. `manual`
  stays exclusive, since combining "no trigger" with a trigger is a
  contradiction rather than a merge, and is rejected by name.

- **cli:** `pipeline new` gains `--on pull_request|push|schedule|manual`, so the
  DAG and the trigger are separate choices. They were welded together --
  `ci-pr-check` was the only shape declaring `on: pull_request` -- which three
  agent trials in a row complained about: wanting a PR-triggered single check
  meant scaffolding three nodes and deleting two, and editing *down* from a
  template means reasoning about which parts are load-bearing before cutting.
  A PR-triggered single check is now `--template minimal --on pull_request`.
  Omitting `--on` keeps each shape's own default, so nothing changes for
  anyone not passing it, and an unknown value names the whole vocabulary
  rather than sending the author off to find it. `--on` was a global flag
  retired in v0.5.0; a command that declares a flag in its own registry entry
  now owns that spelling, and the retired-flag pointer still fires everywhere
  else.

- **cli:** (Breaking) `sparkwing commands` defaults to a one-line index instead
  of a 235KB JSON dump. Anything parsing its output must now pass `-o json`,
  which is byte-identical to the old default. See
  [the migration guide](docs/migrations/v0.23.0.md#sparkwing-commands-prints-an-index-pass--o-json-for-the-old-output). It emitted the full record for all 139 verbs on the theory
  that agents are the primary audience -- which is true, and is why JSON was
  the wrong default: agents do not size output before reading it. A trial fed
  the bare command into a narrow lookup, spent roughly 58,000 tokens, and got
  truncated anyway. The index is the same 139 verbs in 140 lines, and it now
  ends by naming both ways down: `<path> --help` for one verb, `--path PREFIX`
  to narrow. `-o json` is unchanged and one flag away.

- **docs:** `docs search` stopped shredding sections on code-block comments. A
  `#` opening a line inside a fenced block is a comment, not a heading, and
  naming the file an example belongs in (`# .sparkwing/sparkwing.yaml`) is how
  nearly every YAML block in the corpus opens -- 453 such lines across 22 docs,
  358 in the generated CLI reference alone. Each one truncated its real section
  to the opening fence and stranded the content as an orphan section named
  after a filename, so the top hit for "pull request trigger" rendered as a
  bare ```` ```yaml ```` with the answer eight places below it. Sections now
  track fences, including tildes and nesting depth.

- **docs:** proposals and migration guides now rank below every reference hit
  in `docs search`. A proposal records what someone wanted to build (often
  tagged "draft" or "not implemented") and a migration guide records what
  changed between two versions; both tend to be short, and the tie-break
  favors short sections, so they won exactly the broad queries where the asker
  knows least -- searching "trigger" returned a redesign sketch above the
  schema that exists. Both remain searchable.

- **cli:** `pipeline new` now names the query that answers "how do I fire
  this", and `sparkwing info --for-agent` lists each shape's node count and
  whether it declares a trigger. The shape list agents choose from was five
  bare names, so `ci-pr-check` got picked on its name alone and its
  three-node lint/test/gate structure came as a surprise after scaffolding.

- **cli:** `pipeline new --template ci-pr-check` now writes the `on:
  pull_request` trigger it is named for, and `--template scheduled-report`
  writes an `on: schedule` at 09:00 UTC -- something its help already claimed
  it did. Six agent trials across two vendors each reached for `ci-pr-check`
  to satisfy "run go test on pull requests", and five then spent turns
  searching the docs for the `on:` syntax the shape had not written; the sixth
  wrote no trigger at all and produced a manual-only pipeline that passed both
  lint and explain. Filters stay empty, because a `branches: [main]` default
  would bake in a branch name the repo may not use. Triggers are declarative
  -- the controller dispatches whichever pipeline its webhook names -- so this
  changes nothing about `sparkwing run <name>` locally, and the scaffolder now
  names the trigger it declared in its created-files list rather than wiring a
  repo into a GitHub event silently. The other three shapes remain purely
  structural and still declare nothing.

- **cli:** `sparkwing pipeline new --help` describes the surface that exists.
  It still advertised scaffolding a registry template with `--template <name>
  --param k=v` and pointed at a `pipeline templates` verb, neither of which
  survives -- `--template` takes one of five shapes and `--param` is gone from
  the verb. That help is the second thing anyone reads when authoring a
  pipeline, so the stale copy sent readers to a removed command at exactly the
  moment they were deciding what to do. `sparkwing examples --name <name>`
  likewise closed by offering a scaffold command that now errors; it points at
  reading the body and starting from a shape instead. `docs/getting-started.md`
  and the sparks template-catalog section carry the same correction, and both
  dead tokens are now on the docs-drift denylist so they cannot return
  quietly.

- **sdk:** golangci-lint in this repo's own gates now runs under a box-wide
  budget and passes `--allow-parallel-runners` while it holds one, instead of
  serializing on golangci-lint's private lock. The private lock admits exactly
  one linter per machine, so concurrent gates queued behind each other no
  matter how much headroom the box had. `--allow-serial-runners` remains the
  fallback whenever the budget is not held, because dropping the tool's lock
  without a budget would leave nothing serializing lint at all.
- **cli:** `sparkwing queue` tells "nothing is queued" apart from "I never
  reached the daemon". It printed `{}` for both, which is what a quiet machine
  prints, so the command whose help promises "the truthful view of local
  admission" reported an idle queue while it was blind. Every format now states
  reachability outright -- `daemon.reachable` and `daemon.state` in JSON, a
  `daemon` row in plain, a leading line in pretty -- and a socket that cannot be
  reached names the dial failure and exits 4 rather than exiting 0 with an empty
  queue.

- **cli:** `sparkwing doctor` always reports the admission daemon: serving with
  its version and protocol, none running, or unreachable. It carried no daemon
  field at all before, so a sweep that never reached the daemon printed the same
  counts a healthy machine prints and read as a clean bill. An unreachable
  daemon is no longer clean, because the rejection, skew and lockout checks
  under it never ran, and the orphaned-run repair is skipped there rather than
  finalize a run that daemon may be holding.

- **cli:** an unreachable admission daemon leads with why. The error opened with
  "admission daemon started but exited before serving", which reads as a crash,
  and buried the real cause -- a bind the sandbox refused, a socket path over the
  OS limit -- under the log tail. The daemon's own last logged line now leads,
  with the log path still named.

### Fixed

- **cli/portability:** the upgrade notice's version memory is now kept per
  install instead of in one shared file. A machine with more than one sparkwing
  binary -- a `go install` build in `$GOBIN` beside a source install in
  `~/.local/bin` -- resolves a different one from an interactive shell than from
  a launchd job, cron entry, or systemd unit, because PATH is not one list. Each
  invocation used to stamp `~/.sparkwing/last-version` with its own version, so
  the record read as an upgrade followed by a downgrade followed by an upgrade,
  none of which happened. Each install now stamps its own file under
  `~/.sparkwing/last-version.d/`, keyed by a digest of its resolved path
  (symlinked names collapse into one install), so no copy can rewrite another's
  memory; the old `last-version` file is no longer read and is safe to delete.
  The split machine itself is reported, read-only, wherever it can be seen:
  `sparkwing doctor` scans PATH plus the well-known install directories --
  a scan limited to the caller's own PATH would call the machine clean from the
  very shell whose neighbor is the rival -- makes the sweep unclean, and prints
  the exact reversible `mv` that retires each extra copy; `sparkwing info`
  reports the running binary's resolved path and any other installs
  (`executable` in `-o json`, identity lines in `--for-agent`); `sparkwing
  update` names other copies after installing, and its `go install` fallback now
  states exactly where the new binary landed, including when the binary you ran
  was not the one replaced; `bin/install.sh` reports copies outside its
  destination, no longer modifies anything outside `$DEST`, reads `go
  install`'s directory from the first `GOPATH` element when `GOPATH` is a
  list, and succeeds with `$HOME` unset (the report just skips home-derived
  directories). Unix remedies shell-quote each path and guard both the retire
  destination and the undo source, so repeated cleanup cannot overwrite a
  file. Windows reports both exact paths and asks for File Explorer or
  shell-specific quoting instead of claiming one command is safe in cmd.exe
  and PowerShell. Nothing deletes, renames, or picks between binaries. See
  [docs/local-execution.md](docs/local-execution.md).

- **web:** the dashboard's services panel now reads the health body it
  receives instead of classifying on the status code alone. Every sparkwing
  service reports partial failure in-body while still answering HTTP 200 --
  only a total outage turns into a 5xx -- so a filling log volume, a stalled
  gitcache fetch loop, an unwritable cache directory, and a controller whose
  runs are mostly failing all showed up as a healthy lamp. Those services now
  render amber with the upstream `problems` listed beneath them, which is what
  the panel was already built to display. A service answering 200 outside that
  JSON contract still reads as healthy, so nothing that was green for a good
  reason turns amber. `sparkwing configure profiles test` has always decoded
  the body; the two now share one implementation of the contract and can no
  longer disagree about the same service at the same moment. The panel's own
  slow-response finding survives a degraded body rather than being folded into
  it, so a service that is both slow and reporting problems lists both, and the
  probe still drains what it did not read so the panel keeps reusing one
  connection per service instead of opening a new one every refresh.
- **local execution/cache:** `RunAndAwait` children now execute from a
  parent-owned binary lease instead of a pathname in the shared compile cache.
  Clearing or replacing that cache while a parent is live can no longer strand
  its children, and a missing lease now reports its cache provenance and the
  recovery command.
- **chaos:** soak actors and their descendants now stay in exact owned process
  groups whose unreaped leaders anchor cleanup until every descendant is gone.
  Daemon queries are bounded, an independent process guard fails fast when
  owned processes grow past their limit or any one of them stays unreaped, and
  the scheduled runner owns the entire isolated test session so test timeouts
  and signals cannot orphan nested groups.
- **cli/portability:** `cluster image rollout` no longer guesses the author's
  `~/code/gitops` checkout. Select the repository explicitly with
  `--gitops-repo` or `SPARKWING_GITOPS_REPO`; if neither is set, the command
  exits before reading or changing a repository. The public checkout also no
  longer carries configuration for a private work manager.
- **admission:** an exact clean source build now supersedes the release it was
  built from while remaining older than later releases. Opaque worktree and
  dirty builds remain unordered against releases, preserving shared-daemon
  behavior without leaving an installed same-release source build behind its
  resident daemon. Version-change notices now say `changed` rather than
  claiming every transition is an upgrade.
- **cli:** `daemon status` and `daemon restart` now default to pretty output on
  a terminal and JSON when piped; an explicit `-o` continues to win.
- **cli:** `info --for-agent` now marks the one part of its output that is safe
  to keep. A `8<` block carries what this repo uses and the single command that
  reports everything current -- no versions, paths, or command catalogs, so a
  copy in an instruction file cannot go stale. Everything after it is labelled
  wake-scoped, and now includes an authoring quickstart (templates, the
  `authoring` guide, lint/run) so a first-time author does not have to go
  looking. The block also ends with this repo's state -- whether a
  `.sparkwing/` exists, how many pipelines it holds, whether Go is on PATH --
  and the CLI's own examples name it as the first command an agent should run,
  rather than `info -o json`. The block used to send readers to `info -o json`
  for exactly that, and every recorded trial made the second call before doing
  anything else: a whole model round to learn one line.
- **cli:** `pipeline lint` with no target now lints every pipeline instead of
  printing its help and exiting non-zero. Linting is read-only static
  analysis, so the whole repo is both the safe default and what the bare
  command plainly meant; `--all` still says it explicitly.
- **cli:** `sparkwing examples` (the registry browser, renamed from `pipeline
  templates` later in this release) whose `--category`/`--cloud` filter
  matches nothing now lists the values that do exist. Recovering from a guessed
  filter previously meant dumping the unfiltered registry and
  reverse-engineering the vocabulary from it.
- **cli:** `pipeline new` run from a source-built CLI no longer pins an SDK
  version the module proxy cannot resolve. A local build stamps
  `v<semver>-dev+<sha>`, which is neither a `+dirty` marker nor a
  pseudo-version, so it passed the resolvable-version check and was written
  straight into the scaffold's `go.mod`. The result did not build, and
  `version update --sdk` could not repair it: `go get` parses the broken
  `go.mod` before it can bump anything, so the fix was blocked by the
  problem. Build metadata other than `+incompatible`, and any `-dev`
  prerelease, now fall back to the pinned release.
- **docs:** the SDK reference now covers the SDK's subpackages
  (`sparkwing/docker`, `sparkwing/git`, `sparkwing/inputs`,
  `sparkwing/planguard`, `sparkwing/services`), each with its import alias.
  The generator documented only the root package, so 42 exported
  declarations an authoring agent needs for any pipeline that builds an
  image or reads the branch were reachable only by opening the module
  cache.
- **docs:** the SDK reference now lists constants declared with a named type,
  so every enum in the SDK is spellable from the docs alone. `go/doc` files a
  typed constant under its type rather than the package, and the generator
  emitted only the package-level set, which silently dropped values such as
  `ApprovalApprove`, `NoCache`, `Queue`, and `StageAction` -- a reader shown
  `OnExpiry ApprovalTimeoutPolicy` had no way to learn what to assign it.
- **controller:** local failed-run retries now compile and execute from an
  immutable detached snapshot of the source run's recorded Git revision, and
  verify its full origin identity and complete plan snapshot before node
  creation. Dirty or subsequently edited working-tree files cannot change retry
  behavior. If that provenance is unavailable, retry fails clearly instead of
  falling back to an ambient checkout or another registered repo.
- **dashboard:** following an approval link now selects the run it points at.
  The Runs list holds the 200 most recent runs, so an approval that had been
  waiting a while opened the detail pane while nothing on the left moved --
  there was no row to highlight or scroll to. The run being viewed is now
  spliced into the list when the window or the active filters would exclude
  it. `?node=` is read as well as written, so a link lands on the node it
  names and node selection survives a reload; following one from the
  approvals dropdown while already on `/runs` re-selects instead of only
  changing the address bar.


- **docs:** `pkg/docs.Section` gains a `Breadcrumb` field naming the headings a
  section sits under. Additive; existing fields are unchanged.

- **docs:** search hits carry the headings they sit under. The generated CLI
  reference has one `Examples` section per verb -- 139 identically titled --
  so a result list of them said nothing about which verb each belonged to, and
  they were indistinguishable to the ranking too. Hits now read
  `` `sparkwing debug run` > Examples ``. A breadcrumb match scores below a
  heading match, since it says what a section sits under rather than what it
  is about.

- **cli:** `pipeline new` says a declared trigger is not yet live. The `on:`
  block records intent; the controller still has to receive the event, which
  means pointing a webhook at the pipeline. An agent trial read the success
  output, did not open the yaml, and noted it would reasonably have reported
  the trigger as working.

- **docs:** `docs search` ranked a table of CLI verb names above the page that
  answers the question. "command" is a substring of "Subcommands", and a
  substring hit on a heading scored the same as a real one, so the generated
  reference's subcommand tables won "run shell command" -- and won the
  shorter-is-better tie-break too, because a table is short and an explanation
  is not. Heading and body matches are now scored by whether the token matches
  as a word, as a word prefix (so "shell" reaches "shelling"), or merely as a
  substring, and single-character tokens are dropped since they match
  everywhere and narrow nothing. The two sections about running a shell
  command are also titled with the words people search for rather than with
  the API's own vocabulary; ranking can only surface what is written.

- **cli:** the `minimal` scaffold's stub named `ExecIn` and `BashIn`, neither of
  which the SDK has ever had -- it exports `Exec` and `Bash`. That comment is
  the first thing an editor of a fresh pipeline reads, and every agent in a
  six-config sweep searched the docs for those spellings, found nothing, and
  fell back to reading the whole SDK reference. The stub also now shows the
  shell-out-and-propagate-failure shape it was pointing at. A test holds every
  scaffold's CamelCase identifiers -- in comments as well as calls, since this
  one was prose -- to symbols the SDK actually exports.


- **admission:** Zero-cost run-registration connections no longer suppress the
  empty-machine liveness floor. When external CPU or memory pressure leaves no
  ordinary headroom and no positive Sparkwing resource grant is held -- CPU,
  memory, or semaphore -- exactly the FIFO queue head starts; subsequent work
  stays queued behind that positive grant. Zero-cost run-registration
  connections do not count as grants; `sparkwing queue` labels them as
  connected rather than counting them as resource holders.

- **queue:** waiter reasons now follow the admission ledger's CPU liveness
  floor. An otherwise-idle run that is really waiting for memory or a semaphore
  no longer blames external CPU merely because raw CPU headroom is below its
  measured charge; `waiting_on` names the binding headroom dimension.

- **admission:** an older weighted waiter now allows one useful backfill and
  then protects its complete resource request until admission. A stream of
  younger jobs can no longer repeatedly consume transient headroom while the
  queue reports the older job first; queue state exposes the backfill count and
  the protection reason, including across daemon takeover, and the rolling
  event window retains backfill and protection transitions after the waiter
  departs.

- **gate:** The changelog-required check now runs on macOS system Bash 3.2
  while preserving spaces, Unicode, and newlines in changed filenames. Its
  portability fixture runs in both the lint and pre-push gates so a newer Bash
  dependency cannot silently return.

- **queue:** a semaphore-only orchestration lease is shown as part of its active
  node's admission wait instead of as a second, stalled run with a cancel hint.
  JSON keeps both participant rows and now links the holder to its active waiter.

- **hooks:** `sparkwing pipeline hooks status` now names declared hooks that
  are missing or not firing and prints the repair command. `sparkwing info`
  leads its next steps with the same repair, so a missing blocking gate is
  visible before a commit or push silently bypasses it. A failed install proof
  now happens before any candidate hook filename or config value is published;
  prior hooks remain callable during proof and unchanged if it fails. Complete
  replacements publish by atomic rename, and later installation errors roll
  the transaction back byte-for-byte, including managed gates, global-hook
  forwarders, file modes, and the repository's `core.hooksPath`.

- **release:** the release pipeline updates the source-build scaffold fallback
  and the pipeline module pin after publishing a tag. A release can no longer
  leave the next unrelated gate red because generated projects still name the
  preceding SDK version.
- **gate:** local worktrees run golangci-lint through a leased canonical path,
  so a fresh checkout can reuse a warm cache without replaying another
  checkout's filenames. Blob-backed fixed runners keep their existing private
  cache path so restore and save still operate on the directory lint reads.

- **admission:** host-pressure hysteresis can no longer hold a recovered
  reading indefinitely. The daemon keeps the deadband that absorbs noisy
  samples, but reapplies its newest effective value within 30 seconds, so a
  near-threshold waiter can proceed without a daemon restart. Queue JSON now
  distinguishes the age of the effective admission value from the age of the
  newest successful host-pressure measurement; older clients keep the existing
  `external_sample_age_ms` field and safely ignore the additive
  `external_measurement_age_ms` field.

- **config:** the repo registry is written through a uniquely named staging
  file instead of a shared `repos.yaml.tmp`. Concurrent writers no longer
  collide on the staging file, and staged contents are synced before rename.

- **config:** automatic registration skips checkouts under the system temp
  directory so short-lived fixture repos do not accumulate as dead entries.
  Explicit registration still accepts them, and `sparkwing xrepo prune`
  removes entries after their checkouts disappear.

- **config:** test binaries without an explicit Sparkwing config override use
  a disposable sandbox instead of the developer's real configuration and
  state directories. Behavior outside test binaries is unchanged.


- **cli:** `sparkwing queue`'s `EXTERNAL` column reports the host reading
  again instead of a residual. It used to derive external as capacity minus
  held, reserved and available, so once pressure pushed available to its
  zero floor the column stopped depending on the sensor at all and printed
  capacity minus the reserve -- exactly 80.0% of the machine on a 20%
  reserve -- byte-identical across reads while real demand fell. Both host
  dimensions now carry the figure the availability math ran on, and the view
  prints that reading's age, because a reading held in force by the deadband
  cannot otherwise be told from a live one.

- **cli:** a host dimension the sampler could not read now says so. The macOS
  sampler seeded free memory from `vm.page_free_count` and only overwrote it
  when `kern.memorystatus_level` came back sane, so an unreadable level left
  the free-page count standing -- 0.31 GiB of 16 on an idle box, which
  reports 98% of the machine consumed in the same format as a real reading.
  There is no fallback now: an unread dimension renders as `unmeasured`
  rather than a figure, carries `"external_source": "unmeasured"` on the
  wire, and has nothing subtracted from available, so a run is never held
  back by pressure nobody measured.

- **cli:** `sparkwing pipeline hooks survey` no longer reports an empty fleet
  when it could not read the repo registry. A `repos.yaml` with one stray
  character made it print `[]` and exit zero, which is exactly what a machine
  with nothing registered prints, so a corrupt registry read as a swept, clean
  fleet while every gate on it went unchecked. The survey now names the file
  and exits non-zero, and writes no rows at all rather than a document a
  script would parse as an answer. `hooks install --fleet` and
  `hooks fire --fleet` refuse the same way instead of sweeping nothing and
  reporting success.

- **cli:** `sparkwing doctor` carries `gates_survey_error` when the gate
  survey could not run, and says on the pretty path that no repo was checked.
  The rest of the sweep still reports, because the registry says nothing about
  this home's runs, locks or daemon. `-o plain` gains `gates_survey_failed`.
  An empty `ungated_repos` alone could not tell a gated fleet from an unread
  one.

## [v0.22.3] - 2026-08-07
### Fixed

- **cli:** capacity profiles are keyed by the repository a run launched
  from, not by the directory. Every linked worktree of a repo now reads and
  writes one profile, so a pipeline run from a fresh branch is priced from
  what it already cost instead of starting from the conservative cold-start
  default. Tooling that gives each ticket its own worktree was throwing the
  learning away exactly as often as work began.
- **cli:** a still-measuring pipeline can no longer be charged more memory
  than the machine grants a single run. Only cores were capped before, so a
  demand floor could climb past the ceiling and stay there: the floor comes
  down only when a run measures below it, and a run priced above the ceiling
  is never admitted, so it could never measure.
- **cli:** `sparkwing runs stats --reset` reaches a demand floor with no
  measured samples behind it and reports it, instead of answering "no
  measured capacity profile to reset" about the exact profile that is
  pricing the runs. A bare pipeline name now resets every repo-scoped key
  that carries it, and the summary names each key it reached.
- **cli:** a request no release could ever satisfy is refused at submit with
  the arithmetic (`needs 12GiB of memory, this machine has 8GiB`) rather
  than queued until it times out. An ordinarily busy box still queues.

## [v0.22.2] - 2026-07-31
### Added

- **sdk:** `ToolSlot` coordinates external tools through a named, box-wide CPU
  budget. Waiting steps report their queue position, and `BoxToolBudget`
  derives concurrency from the tool's measured CPU cost.
- **cli:** `sparkwing queue` now estimates start times for work waiting on
  named concurrency limits.

### Fixed

- **runner:** Host admission now subtracts measured CPU utilization instead
  of run-queue length. I/O-bound workloads no longer make idle cores appear
  occupied; contention detection still uses load average. Linux reads CPU
  counters from `/proc/stat`, while macOS estimates utilization from the
  process table.
- **cli:** `sparkwing queue` reports the external-pressure reading used by
  admission, including its age, instead of reconstructing a misleading
  residual.
- **cli:** Unread host metrics render as `unmeasured` and do not reduce
  available capacity.

## [v0.22.1] - 2026-07-30

### Added

- **cli:** `sparkwing pipeline hooks fire` establishes a gate by making it
  refuse a commit, which is the only check that can. Reading a hook directory
  cannot: a repo whose hooks are shadowed inspects as fully installed and
  refuses nothing, and one whose `core.hooksPath` points at a sibling repo
  inspects as installing nothing and refuses everything. `fire` stages a file,
  commits it with the gate told to refuse, and reports whether git refused it
  and which hook file did -- all inside a throwaway linked worktree that shares
  the repo's config and carries its own index and detached HEAD, so the repo's
  working tree, branches and HEAD are untouched whatever the gate does. Every
  refusal is checked against a control commit with hooks switched off, so an
  unrelated failure is not read as a gate doing its job. Only a hook sparkwing
  wrote that carries the self-test guard is ever executed; anything else is
  reported `unprovable` and exits non-zero, because an enforcement question
  that could not be answered is not a pass. `--fleet` for every registered
  repo, `-o json` for the machine-readable form.

- **cli:** managed blocking hooks carry a `SPARKWING_HOOK_SELFTEST` guard that
  makes them refuse and name their own file, so `hooks fire` can see a gate
  work without paying for the gate. The variable can only refuse -- there is no
  value that lets a commit through, because one would be a bypass with an
  environment variable for a key. Re-run `sparkwing pipeline hooks install` to
  add the guard to hooks installed before this release.

- **cli:** `sparkwing pipeline hooks survey` gained a `borrowed` state, for a
  repo whose `core.hooksPath` points at another repo's hook directory. Its
  commits are refused by a file nothing in it declares, installs or keeps, and
  an uninstall in the owning repo disarms it with no commit here and no
  warning, so it counts as ungated even though commits really are refused. It
  used to report as `uninstalled`, which read as no gate while the gate was
  armed and blocking. The remedy clears the repo's own override first: install
  treats a repo-scoped `core.hooksPath` as deliberate and leaves it alone.

- **cli:** `sparkwing doctor` reports `gates_surveyed`, and says on the healthy
  path how many repos were surveyed and that every declared gate fires. An
  empty ungated list on its own could not be read as a gated fleet, because a
  build that never ran the survey omits the field entirely and a reader looking
  for problems finds none either way.

### Changed

- **cli:** `sparkwing pipeline hooks install` now runs a repo's blocking gates
  once before those gates can fire. While a repo's hooks are inert a gate that
  cannot execute -- a red pipeline, an admission daemon the repo's pinned SDK
  cannot speak to -- is indistinguishable from one that passes; arming turns
  the first into a commit that fails every time, which is worse than the
  silence it replaces. Proof completes before hook or config publication, so a
  failure changes nothing and cannot interrupt an already-working gate.
  Complete replacements publish by atomic rename, and any later error restores
  the prior hooks, forwarders, modes, and `core.hooksPath` exactly. The proof
  runs on every install that leaves a gate live, including a re-install of an
  already-armed repo. `--no-prove` arms without the proof.

- **admission:** a still-measuring pipeline's contended-run demand floor
  now decays: a contended run that measures below the floor halves it
  (never past the run's own evidence), mirroring the ceiling-hit doubling
  that raises it. Sustained external load can no longer ratchet a
  pipeline's charge to the machine ceiling permanently -- the price
  converges back to measured demand once the load passes, without
  `runs stats --reset`.
- **admission:** capacity profiles are keyed `repo/pipeline` for runs
  launched inside a git repo (bare pipeline name outside one), so
  same-named pipelines in different repos -- every scaffolded repo's
  `ci` -- no longer pool measurements and contended floors on one
  machine-global row. Existing bare-keyed rows are left behind and
  ignored by in-repo runs; each pipeline re-measures once under its
  scoped key, and stale rows can be cleared with
  `runs stats --reset --all --yes`.
- **admission:** capping a still-measuring charge at the machine's
  grantable ceiling is no longer silent: the run prints the capped
  charge with the exact `runs stats --reset --pipeline <name>` that
  clears a contention-poisoned profile.
- **cli:** `sparkwing doctor` reports (never repairs) a poisoned
  capacity profile -- a contended-run demand floor pricing every run at
  or above the machine's grantable ceiling -- naming the profile and the
  reset command.

### Fixed

- **cli:** `hooks survey` no longer reports one hook as both `installed` and
  `missing`. Every shadowed repo did -- the gate sat in its hook directory and
  in the list of hooks git runs nothing for -- and a reader who resolved that
  contradiction the wrong way saw a repo with nothing to fix. Shadowed hooks
  now have their own field, `missing` means no managed gate exists anywhere,
  and the two are disjoint. `-o plain` keeps its column count and its meaning:
  the third field is still every hook git runs no gate for.

- **dashboard:** the embedded documentation is served at `/docs` on whatever
  address `sparkwing dashboard start` (or `sparkwing-web`) is listening on.
  It is the same set `sparkwing docs list` and `sparkwing docs read` print,
  rendered as HTML with the pages cross-linked, so a reader on a machine with
  no route to sparkwing.dev has the full reference from the binary they are
  already running. The route sits inside the dashboard's authenticated mux:
  a deployment that requires a login to see runs requires one to read the
  docs, and one that does not, does not.

- **sdk:** `docs.ReadRaw` returns an embedded page's markdown exactly as it
  ships. `docs.Read` still rewrites cross-page links into `sparkwing docs
  read` commands for terminal output; `ReadRaw` is for renderers that resolve
  those links themselves.

- **sdk:** `wingwire.ProtocolFloors` maps SDK releases to the wire protocol
  major their clients speak, and `wingwire.ReleasedProtocolFloors` returns the
  table this build ships. Explaining a refused handshake means comparing two
  versions that are both free to be old -- the resident daemon's and the
  calling repository's pin -- so it needs the whole history of protocol
  cliffs, not just the one current when the binary was compiled.

- **cli:** `sparkwing pipeline hooks survey` reports what git does with the
  hooks every registered repo declares, and `sparkwing pipeline hooks install
  --fleet` arms them all in one pass. Each repo reads as `armed` (git runs a
  gate), `shadowed` (gates installed, but `core.hooksPath` sends git
  elsewhere), `uninstalled` (a declared hook was never written), or
  `undeclared` (no pipeline asks for one). Both read the machine's repo
  registry -- `repos.yaml`, filled by `sparkwing configure xrepo add` and by any
  `fallback_paths` it lists -- rather than a list scoped by hand, which is how
  a repo ends up ungated for weeks with nothing reporting it. A checkout the
  registry does not list is not surveyed and not swept; register it first.
  `-o json` for the machine-readable form, `--ungated` for just the
  actionable rows. Ungated means a hook that can refuse work does not fire, so
  `--ungated`, `doctor` and the `--fleet` summary all count `pre-commit` and
  `pre-push` and nothing else: a repo whose only missing hook is `post-commit`
  loses a notification rather than a gate, and a repo that declares no
  blocking hook is counted apart from the ones `--fleet` armed rather than
  among them.

- **cli:** `sparkwing doctor` names the registered repos that accept commits
  with no gate, under `ungated_repos` in `-o json`. It reports them on a run
  that finds nothing else to repair too, since a healthy home is exactly where
  an ungated repo would otherwise go unmentioned. Which repos git gates is
  machine configuration rather than a sparkwing home's state, so it is
  reported alongside the sweep and does not decide the home's verdict.

- **cli:** `sparkwing doctor` (and `ops doctor` in a pipeline binary) now
  reports admission daemons running for other sparkwing homes that were
  built from a scratch module -- the ones reporting version `v0.0.0`, which
  no release carries and only a module with a local `replace` directive
  produces. Each home keeps its own daemon on its own socket, so such a
  daemon is unreachable from the home you are inspecting yet sits beside
  the resident daemon in any process listing, where its log and its bind
  failures read as production state. Doctor names the version tell, the
  socket it answers on, and where its binary usually lives (a temp
  directory, from the process arguments); it never stops the process, which
  stays the operator's call. The `-o json` report carries the same under
  `stray_daemons`.

- **cli:** `sparkwing run --sw-index PATH` runs a pipeline against a git index
  the caller supplies instead of the repository's own, so a verifier can have
  steps scoped to the staged diff -- comment checks, `git diff --cached`
  sweeps -- judge a snapshot of work that is not committed yet. The flag is
  the deliberate counterpart to the `GIT_INDEX_FILE` git exports to every hook
  it launches, which sparkwing drops on startup so the gated repository cannot
  leak into a pipeline's own work; an argument is how a caller says the
  binding is intent rather than inheritance. A bound run writes an
  `index_bound` event naming the absolute index before the pipeline starts,
  and a caller that requires that receipt can tell a run that judged its
  snapshot from one that judged the repository's index; a run rendering for a
  person says the same thing in prose. A path that does not exist is refused,
  since git reads a missing index as an empty one.

- **sdk:** `sparkwing.ToolCacheDir(tool)` returns a cache directory for an
  external tool, scoped to the worktree the pipeline runs in. Tools that key
  their cache on file content alone -- golangci-lint among them -- replay a
  stored result for identical input regardless of which checkout produced it,
  so two worktrees of one repo sharing the default cache report each other's
  file paths. Pass the directory through the tool's cache variable
  (`Env("GOLANGCI_LINT_CACHE", sparkwing.ToolCacheDir("golangci-lint"))`) to
  keep the caches disjoint while each worktree still reuses its own. Running
  two lint jobs at once is a different problem and a scoped cache does not
  touch it: golangci-lint takes its parallel-runner lock on one file in the OS
  temp directory, so every run on the box contends for it no matter where
  `GOLANGCI_LINT_CACHE` points -- only `TMPDIR` moves it -- and a run that
  cannot take the lock retries for 5s and then exits `parallel golangci-lint is
  running`, which a gate reports as a lint failure against a tree that is fine.
  Pass `--allow-serial-runners` so the run waits its turn instead. It queues on
  golangci-lint's own lock, so one pipeline adopting it stops failing straight
  away with no fleet-wide agreement needed; the flag waits indefinitely, so
  bound it with a context deadline, and report a deadline that fires as
  could-not-run with the time waited rather than as a finding in the tree.

- **admission:** the `hello_ack` handshake frame carries
  `native_protocol_major`, the newest wire protocol major the daemon speaks,
  alongside the existing `protocol_major`, which now reports the major agreed
  for that one connection. A client reads the two together to know it is the
  older side and must not take the daemon over -- replacing a newer daemon
  with an older one would be undone by the next native client, with nothing
  bounding the exchange. Daemons predating the field omit it, and a reader
  falls back to `protocol_major`.

- **admission:** upgrading sparkwing no longer locks out every repo whose
  pipeline pins an older SDK. The daemon now answers the handshake on the
  newest wire protocol major it shares with the client rather than only on
  its own, so a repo pinning v0.17.25 keeps getting admission grants from a
  v0.22.0 daemon instead of failing its gates with `daemon speaks a newer
  protocol; upgrade sparkwing` -- advice that could not work, because the
  client that failed was the repo's compiled pipeline binary, not the CLI.
  The served range is `wingwire.MinProtocolMajor` through
  `wingwire.ProtocolMajor`; the client states its own major on every
  connection, so nothing maps releases to protocol majors. Raising the floor
  is what makes a stale pin fail, and is a break to be announced as one.

- **admission:** a run-lease request from a client on a pre-`sub_lease`
  protocol major is read in that major's terms, so the node-level semaphore
  acquisitions such a client makes no longer get a daemon-written terminal
  run row on top of the one the run writes for itself.

- **cli:** three help entries named commands that do not exist --
  `sparkwing users add`, `sparkwing profiles set` and `sparkwing profiles
  use`, whose real paths sit under `cluster` and `configure`. They reached
  readers through `--help` and through the generated CLI reference, and a
  test now fails when a doc or the README names a command the CLI does not
  dispatch.
- **cli:** the admission handshake now names the side that has to move when a
  daemon speaks a newer wire protocol. The old message said "upgrade
  sparkwing", which pointed at the CLI on `PATH` -- but the client in that
  handshake is the pipeline binary compiled from the calling repository's
  `.sparkwing/go.mod`, so upgrading the CLI cannot change the outcome. The
  error now reports both sides' protocol major and version, names the pin to
  raise and the release to raise it to, offers `SPARKWING_HOME` for an
  isolated daemon, and says outright that the CLI is not a party to it.
  This wording does not reach any repository that is locked out today. The
  message is compiled into the client, and the clients that hit this error
  are pipeline binaries pinned below the boundary, which carry the old text
  and keep printing it; a repository sees the new message only once its pin
  has moved past the boundary, at which point it is no longer refused. The
  benefit is for the next protocol bump, not this one -- for repositories
  currently refused, the `doctor` entry below is the surface that reaches
  them, because it runs from the machine's CLI rather than from the pinned
  binary being turned away.
- **cli:** `doctor` reports the version skew that actually blocks work.
  It compared the running CLI against the resident daemon and said nothing
  about registered checkouts whose pinned SDK speaks an older protocol
  major -- the skew that refuses every gate those checkouts run. The new
  `locked_out_repos` section names each one with its pin and the release the
  pin has to reach. This is the diagnosis that reaches an operator who is
  locked out right now. A linked worktree gets its own row, marked
  `worktree`: the pipeline binary is built from the `.sparkwing` of whichever
  checkout runs the gate, so a worktree's pin is its own and folding it into
  its primary would hide the pin that is actually refused. A checkout whose
  `.sparkwing` carries a `replace` or a `go.work` using a local SDK checkout
  is skipped: its declared pin is not what gets built, so reporting it would
  send an operator to edit a line that changes nothing. The daemon's protocol
  major is read from its handshake ack, and each pin is compared against the
  lowest release known to speak that major, so the check keeps working across
  every cliff rather than the one current when the CLI was built: a daemon
  left resident at an older major still locks out every pin below it, and a
  daemon speaking a major *newer* than the CLI is diagnosed too. Reading the
  ack rather than the queue state is what makes that second case visible at
  all: such a daemon refuses the CLI's queue query outright, but answers the
  handshake before it refuses anything.
- **cli:** `doctor` names a resident daemon whose wire protocol this build
  does not know, in a new `daemon_protocol_gap` section. This build cannot
  query that daemon, and it cannot name the release that first spoke that
  protocol -- which release carried a bump is decided after the build being
  asked was cut -- so each locked-out checkout is measured against the
  daemon's own release, which speaks that protocol but need not be the lowest
  release that does. The remedy printed there is to update the sparkwing CLI,
  which carries the release table this diagnosis reads. It is the one state in
  which upgrading the CLI helps, so the `locked_out_repos` warning leaves out
  its note that it does not, and says instead that the target on each row is
  the daemon's own release.

- **cli:** `pipeline hooks install` no longer leaves a dead gate on machines
  whose global git config sets `core.hooksPath`. That setting replaces
  `.git/hooks` for every repository, so installed hooks were silently never
  run. Install now points the repository at its own hook directory and
  chains the machine's hooks from it: a hook both layers define runs the
  pipelines first and hands off afterwards, and a hook only the machine
  defines (`prepare-commit-msg` and friends) gets a forwarder, so nothing
  stops firing. Only names git itself runs as hooks are forwarded, so a
  helper script kept in that directory is not turned into one. When a
  hand-written hook blocks one of those forwarders, install refuses the
  claim and names the hook rather than silencing the machine's copy; both
  install and `pipeline hooks status` report a machine hook nothing hands
  off to, including in a repository that already carries the claim. A
  `core.hooksPath` the repository set itself is left alone
  and reported instead. `pipeline hooks uninstall` removes the forwarders
  with the rest and releases the claim, so the machine's hooks apply again,
  including when the hooks are already gone by other means. All three verbs
  now resolve the hook directory through git's common directory, so they
  also work from a linked worktree.
- **cli:** `sparkwing doctor` reports a hook directory git is not reading,
  naming the gates that stopped firing and how to restore them, so a
  disabled commit or push gate surfaces instead of going unnoticed.
- **cli:** a pipeline started by a git hook no longer inherits the
  repository git bound the hook to. git hands a hook `GIT_INDEX_FILE` (and,
  on other paths, `GIT_DIR` and friends), every process the pipeline starts
  inherits them, and a step that builds a scratch repository then works on
  the gated repository instead: staging in a temp directory writes into the
  commit being gated, which fails it outright when partial-commit paths hand
  over an absolute index. sparkwing now drops the repository-binding `GIT_*`
  variables before dispatching any command, so steps -- and the third-party
  tools they call -- resolve repositories from their own working directory.
  Identity (`GIT_AUTHOR_*`, `GIT_COMMITTER_*`) and config selection
  (`GIT_CONFIG_*`) are untouched. No reinstall is needed: the fix travels
  with the binary. Managed hooks scrub them a second time, in a subshell
  around the pipeline invocations, so a hook written by a current install
  also protects an older `sparkwing` on `PATH`; the hand-off to a global
  hook stays outside that subshell and keeps the environment git gave it.
  `pipeline hooks install` rewrites managed hooks, so re-running it picks
  the second layer up.
- **cli:** the index a commit is being composed in survives that unbind as
  `SPARKWING_GATE_INDEX`, an absolute path, so a check scoped to the staged
  change still sees the change. Only a commit of already-staged content
  hands a hook the repository's own index; `git commit -a` and
  `git commit -- <path>` hand over a lock file that holds the content, and a
  step reading `git diff --cached` without it is shown an empty commit and
  passes it. Steps opt in per command
  (`GIT_INDEX_FILE="$SPARKWING_GATE_INDEX" git diff --cached`) rather than
  inheriting the binding, which is what keeps a stray `git add` in a scratch
  checkout out of the commit being gated. `commentcheck -staged` reads it,
  preferring a `GIT_INDEX_FILE` a caller set deliberately and falling back to
  the repository's index when no hook is running.

- **admission:** a sparkwing built from source (a `(devel)` or `+dirty`
  build) now takes over a running release admission daemon whatever the
  two version tags say, through the same drain-and-succeed path a newer
  release takes. Runs launched by a source build no longer die with
  opaque `invalid` rejections against a daemon from an older install. A
  release never takes a source-built daemon back, and an unorderable
  version (`(devel)`, `(unknown)`) never takes anything over, so no two
  builds can drain each other in a loop; `sparkwing ops doctor`'s
  version-skew warning explains the cases takeover leaves alone.

## [v0.22.0] - 2026-07-22

### Added

- **sdk:** `Plan.Priority(n)` declares a local admission priority.
  Higher-priority queued runs admit before lower-priority ones at the
  local daemon, so a queue of cheap frequent work can no longer starve
  the occasional important run behind it.
- **release:** the release pipeline refuses to cut a version when the
  latest published release tag is not in the checkout's history, naming
  the missing tag and the recovery steps. A release cut from a stale
  line can no longer silently ship without a prior release's work.

### Changed

- **admission:** unpinned local runs no longer reserve their whole
  measured peak for the DAG's lifetime. The run admits on its plan-level
  concurrency gates only; each node then acquires its own host cost at
  dispatch and releases it at node end. Early cheap nodes run while a
  later heavy node waits for capacity, and a run whose nodes are parked
  on a concurrency group no longer holds idle cores that queue other
  runs. Explicit whole-run `.Resources()` pins still reserve at run
  scope for the whole run.
- **admission:** capacity profiles are versioned by a fingerprint of the
  DAG shape plus every concurrency declaration (group name, capacity,
  scope, policy, member cost). Changing a declaration starts a new
  measurement generation priced from the predecessor's peak instead of
  pricing on samples measured under the old shape. On first run after
  upgrade every pipeline re-measures once from its prior peak.
- **admission:** the measured host charge is now the p95 of the last 20
  clean runs (previously p99 of 50, which at that window size is the
  maximum). One freak run no longer pins a pipeline's price for weeks,
  and a genuine cost change re-prices within the shorter window. Queue
  views and drift warnings say "measured p95" accordingly.

- **admission:** unpinned local work admits CPU and memory per node instead of
  reserving one host charge for the run's lifetime. Node concurrency and host
  resources grant atomically, so a node waiting on a concurrency limit holds
  neither and unrelated work can use the box.
- **admission:** capacity learning records node demand and re-measures plans
  when their concurrency declarations change. Explicit run resources retain
  run-lifetime admission semantics.
- **admission:** higher-priority plans admit before lower-priority plans, while
  equal priorities retain FIFO order. Bounded backfill admits work that fits
  without starving the oldest compatible waiter.
- **cli:** queue views expose execution holders and waiters with node identity,
  priority, blocking resources, and separate capacity/concurrency wait data.
- **admission:** (Breaking) the local daemon wire protocol advances to v2 for
  phase-scoped leases and child attachment. Clients and daemons using different
  protocol majors refuse to share a live ledger; upgrade every Sparkwing
  process on a host together.

### Fixed

- **release:** template verification now runs after the pre-push gate and
  isolates each generated pipeline's daemon state and lease environment.
  Release validation no longer races Terraform providers for host capacity
  or attaches generated pipelines to a daemon with an incompatible protocol.
- **admission:** a queued run that reconnects (client restart, daemon
  takeover) reattaches to its existing waiter instead of losing its
  place or duplicating itself; retry matching validates request shape,
  cost semantics, and process identity before replacing a stale waiter.
- **admission:** node-level concurrency admissions are encoded as
  distinct queue participants, fixing ID collisions between a run and
  its nodes; queue views show stable participant identity, and node
  participants stay internal to the queue rather than leaking as
  phantom runs.
- **admission:** a node killed by run teardown records `cancelled` and a
  node evicted by a `cancel_others` group records the superseding run,
  while a node that genuinely failed keeps its failure even when the
  run is torn down moments later.
- **cli:** `sparkwing queue` reports ETA as unknown while a run is
  blocked instead of a fabricated clearing time.
- **release:** tracked-file parsing preserves NUL-delimited git output,
  self module sums are written canonically, and a stale release
  resource pin no longer inflates the release run's admission charge.
- **docs:** restored the v0.17.3 through v0.17.12 changelog sections,
  which were missing from this line's history.

## [v0.20.0] - 2026-07-21
### Fixed

- **admission:** a persisted ledger the daemon cannot restore no longer
  wedges host-wide admission. Restored grants the current budget cannot
  hold are shed newest-first (each shed is logged with its run and size),
  and a state file that cannot be restored at all is quarantined to
  `state.json.corrupt-<unixtime>` while the daemon serves with a fresh
  ledger. Previously one leaked or oversized lease made every daemon
  start exit with an invalid-resize error before serving, blocking every
  run on the box until an operator removed the state file by hand.
- **admission:** restoring a snapshot whose soft-core grants overcommit
  the core total (a legal ledger state) no longer fails the startup
  resize; the allowance the live ledger gives soft-core grants now also
  applies when capacity is re-derived after a restore.

### Added

- **cli:** `sparkwing doctor` reports quarantined admission ledger files
  (`state.json.corrupt-*`) left behind after a failed restore, naming
  each so it is found and reviewed instead of sitting in the home
  forever. Reported with an explanation, never removed.

## [v0.19.0] - 2026-07-18
### Added

- **docs:** the docs drift gate (`internal/doccheck`, run in pre-push)
  gains two mechanical sub-gates. `cli-verbs` resolves every `sparkwing
  <verb> <subverb>` invocation shown in the docs against the CLI command
  tree and fails on any subcommand that doesn't exist. `service-ports`
  checks that wherever a doc names a cluster service by its DNS label and
  states the port its Service targets, the port matches that binary's
  default `--addr` bind port. Both anchor to a single in-repo source of
  truth (the help registry and the service `main.go` files), so a renamed
  verb or a changed service port that the docs still cite fails the gate
  instead of misleading a reader.

- **controller:** the health endpoint now reports an `auth` field
  (`"enabled"` / `"disabled"`) so operators can see at a glance whether
  the controller is serving open. `sparkwing cluster status` and
  `sparkwing profiles test` flag the controller probe as a warning when
  auth is disabled.
- **controller:** `SPARKWING_REQUIRE_AUTH` (or `--require-auth`) makes an
  empty tokens table a hard startup error, guarding against accidentally
  deploying an open controller. Off by default so first-run bootstrap and
  laptop-local use keep working.

- **config:** New `on: pull_request` trigger. A pipeline declaring it
  fires on GitHub `pull_request` deliveries (the controller dispatches on
  the `opened`, `synchronize`, and `reopened` actions; other actions are
  acknowledged and ignored). The run checks out the PR head commit, and
  the pipeline reads the pull request from
  `RunContext.Trigger.PullRequest` (number, base ref, head ref, head and
  base SHA, action). The block accepts declarative `actions` and
  `branches` filters. sparkwing does not yet report the run's result back
  to GitHub as a commit status or check; see
  [docs/hooks.md](docs/hooks.md).

### Changed

- **cli:** `sparkwing pipeline templates` pretty output now groups entries
  under category headers and ends with a footer advertising its own
  affordances (the `--category` / `--cloud` filters, the `--name` detail
  view, and the shown counts), so the catalog is scannable and its flags
  are discoverable without reading the source. The `-o json` shape is
  unchanged.

- **controller:** a controller that starts with an empty tokens table now
  logs a loud warning that every endpoint is served unauthenticated,
  instead of falling into open-serving mode silently.


- **cli:** when the local admission daemon rejects a request as invalid it
  now names the offending input and value (an unrecognized cost source, a
  malformed field) instead of a bare `invalid`, logs the rejected request,
  and `sparkwing doctor` flags a repeat-rejection pattern with its cause.
  Doctor additionally reports a version skew between the running binary and
  the resident daemon -- a development or newer build that did not take over
  an older daemon, and would otherwise fail opaquely -- with the two
  versions and how to resolve it.

### Fixed

- **cache:** the compiled-pipeline binary cache now invalidates when a
  module replaced to a local filesystem path changes. Replace targets
  from the pipeline's `go.mod` and from an in-scope, covering `go.work`
  (both its `use` modules and its local `replace` directives) are hashed
  by content -- all files, so edits to embedded assets such as a
  template registry's manifests count -- rather than by a version the
  local checkout doesn't carry. Previously a `go.work` replace was
  ignored entirely and edits under a `go.mod` replace only counted when
  they touched Go source, so a run could execute a stale binary compiled
  before the change.

- **config:** S3-backed run state (Mode 2, shared object storage) now
  wires the local SQLite outbox that the deployment docs promise. When
  the bucket is briefly unreachable, run state writes stage to
  `~/.sparkwing/outbox.db` (one per host, honoring `SPARKWING_HOME`) and
  replay in order once connectivity returns, so a transient blip no
  longer fails the run or drops state. While the outbox holds queued
  writes for a run, later flushes keep flowing through it, so the
  replay can't be overtaken by a direct write.


- **cli:** an unpinned pipeline whose host cost is being auto-measured no
  longer dies before its first node with an opaque `local admission: wingd:
  fail on "invalid"` when the box has ample free capacity. The local
  admission daemon now accepts every cost source its own resolver produces
  (the re-measuring and demand-floor charges were rejected as unknown), so
  the documented happy path admits without an explicit `plan.Resources(...)`
  pin.

### Docs

- **docs:** `security.md` now states plainly that secret encryption at
  rest is opt-in (off until `SPARKWING_SECRETS_KEY` / `--secrets-key-file`
  is set) and documents the open-serving warning surfaces.

## [v0.18.0] - 2026-07-18
### Added

- **cli:** `sparkwing pipeline templates` gains `--category` and `--cloud`
  filters and a `--name <template>` detail view (description, when-to-use,
  prerequisite, a parameters table, applicability, and the template
  README). `--body` additionally renders the pipeline body using parameter
  defaults, with `<placeholder>` values for required params. All views
  support `-o json`.
- **cli:** `sparkwing pipeline sparks vendor --module <name>` ejects a
  spark block module's source into `.sparkwing/sparks/<name>/`, makes it
  writable, and adds a `replace <module> => ./sparks/<name>` directive to
  `.sparkwing/go.mod` so imports stay unchanged while the code becomes
  yours to edit. A bare name resolves to a sparks-core block; a full
  module path is accepted as-is.
- **template-verify:** new pipeline (`sparkwing run template-verify`)
  that builds the CLI from the working tree, then scaffolds every
  sparks-core registry template into a throwaway repo and checks it
  compiles, lints, and explains. Templates whose manifest marks them
  `verify: runnable` also run end-to-end against a synthesized fixture
  (a go module or a Dockerfile); a docker-fixture template's run is
  skipped when no Docker daemon is present. Templates that import
  sparks-core blocks are built against the local sparks-core checkout so
  they can be verified against unreleased library APIs they co-develop
  with.
- **release:** the release pipeline now gates on `template-verify` being
  green (via a cross-pipeline await) before it will push a tag, so a
  broken template can't ship.

### Docs

- **docs:** Document the template catalog workflow and the spark-module
  vendoring flow in the sparks reference, with pointers from
  getting-started.

## [v0.17.25] - 2026-07-16
### Fixed

- **cluster:** Trigger workers now keep claiming work while earlier trigger
  handlers run, so nested `RunAndAwait` child triggers are not stranded behind
  their waiting parent.

## [v0.17.24] - 2026-07-16
### Fixed

- **cluster:** Kubernetes runner Jobs that disappear before terminal state now
  fail their node instead of leaving the orchestrator waiting on a missing Job.

## [v0.17.23] - 2026-07-16
### Added

- **cluster:** Trigger-side Kubernetes runner Jobs can now receive node
  selectors and tolerations through CLI flags or environment variables.

## [v0.17.22] - 2026-07-16
### Fixed

- **cluster:** Kubernetes runner Jobs now set writable Go cache paths under
  `/tmp`, so non-root job pods can compile fetched pipeline sources.

## [v0.17.21] - 2026-07-16
### Fixed

- **storage:** Artifact-store URLs with `http://` or `https://` now open the
  `sparkwing-cache` HTTP backend, so cluster runner pods can reuse the shared
  cache service for node artifacts.

## [v0.17.20] - 2026-07-16
### Fixed

- **cluster:** Kubernetes runner Jobs can execute node work through the
  generic `sparkwing run-node` entrypoint, so runner job images can fetch the
  triggered source and run a single claimed node.

## [v0.17.19] - 2026-07-16
### Fixed

- **cluster:** Kubernetes runner Jobs now execute node work through the
  `sparkwing-runner run-node` entrypoint.

## [v0.17.18] - 2026-07-16
### Fixed

- **cluster:** Kubernetes runner Jobs now set restricted-compatible pod and
  container security contexts, so clusters enforcing the restricted Pod
  Security Standard can admit spawned runner pods.

## [v0.17.17] - 2026-07-16
### Fixed

- **release:** The bundled `.sparkwing` module metadata includes the
  Kubernetes client dependencies required by trigger-side k8s dispatch, so
  remote trigger workers can compile released pipeline sources without
  modifying `go.mod`.

## [v0.17.16] - 2026-07-16
### Fixed

- **cluster:** Trigger workers can pass `--trigger-runner=k8s` with
  `--claim-nodes=false` so claimed triggers dispatch pipeline nodes through
  Kubernetes Jobs instead of running each node inside the long-lived trigger
  worker.

## [v0.17.15] - 2026-07-16
### Fixed

- **cache:** Gitcache seed uploads stream bundle bodies to disk instead of
  buffering them in memory, so large private-repo seeds stay within normal
  cache pod memory limits.

## [v0.17.14] - 2026-07-16
### Fixed

- **cluster:** Remote pipeline triggers can seed a controller-backed gitcache
  from the submitting checkout when the cache cannot fetch a private origin
  directly. Seed uploads are scoped to the triggered commit and require the
  controller's admin scope.

## [v0.17.13] - 2026-07-16
### Fixed

- **cluster:** Remote pipeline triggers can now fetch source from non-GitHub
  Git origins through the stored `repo_url` while preserving the canonical
  GitHub clone path for GitHub-backed triggers.
- **admission:** Measured and default CPU costs now act as backpressure, not
  a hard admission gate. Memory and explicit resource pins still gate
  admission strictly, while CPU pressure admits one memory-fitting run before
  stopping additional CPU-bearing work. A saturated host therefore keeps
  making progress instead of parking every queued run behind a CPU-only
  deficit.

## [v0.17.12] - 2026-07-15
### Added

- **admission:** Plans can now declare a local admission priority with
  `Plan.Priority(n)`. Higher-priority queued runs admit before lower-priority
  runs while equal-priority runs keep FIFO order; `sparkwing queue` now shows
  waiter priority in human and plain output.
- **cli:** Source-built scaffolds now fall back to the latest published SDK in
  this release line when the running binary has no stamped version.

## [v0.17.11] - 2026-07-14
### Fixed

- **admission:** Queued retries can now update a waiting request's measured
  host cost before grant instead of failing with `duplicate` or running with a
  stale charge. Node-level host resources and concurrency semaphores now admit
  together, so a node no longer holds a semaphore while still waiting for host
  capacity.

## [v0.17.10] - 2026-07-14
### Fixed

- **admission:** Runner-mode nodes now use separate daemon participant IDs
  for host admission and node-local semaphore admission, so a node that also
  enters a local concurrency group no longer fails with `duplicate` before its
  work starts. Queue rows keep the owning run as `run_id`, include the daemon
  participant key separately, and display the run/node label in human-facing
  queue views.
- **cli:** Fresh source-built scaffolds now pin the latest published SDK
  fallback, so projects created from an unreleased local binary still build
  against a resolvable module version.

## [v0.17.9] - 2026-07-14
### Fixed

- **admission:** A client that reconnects after receiving a local-admission
  grant now reclaims the matching live lease instead of failing with
  `duplicate`. The daemon still rejects mismatched retries and restored
  multi-member leases fail closed rather than transferring partial ownership.
- **cli:** Fresh source-built scaffolds now pin the latest published SDK
  fallback, so projects created from an unreleased local binary still build
  against a resolvable module version.

## [v0.17.8] - 2026-07-14
### Fixed

- **admission:** `sparkwing queue` now leaves the clear-time estimate unknown
  when FIFO admission cannot model a queue drain, instead of reporting zero
  milliseconds. Waiters blocked only by earlier queued work now say so, while
  soft CPU waiters and zero-core work follow the same fit rules the admission
  daemon uses.

## [v0.17.7] - 2026-07-14
### Fixed

- **admission:** Queued local-admission retries now reattach to the existing
  waiter only when the retry matches the queued request exactly. A reconnect no
  longer fails a waiting node with `duplicate`, and a different process cannot
  take over a queued request by reusing its run id.
- **orchestrator:** Node terminal results are written with cancellation-detached
  state updates, so a run that starts tearing down does not lose the original
  failure reason and later display the node as orphaned.

## [v0.17.6] - 2026-07-14
### Fixed

- **admission:** Measured and default CPU costs now preserve the daemon's
  reserved headroom once any work is already running. CPU still has an idle
  liveness floor, so an idle host admits one run under pressure, but additional
  CPU-bearing work waits unless it fits the grantable budget. This keeps
  `sparkwing queue` capacity math nonnegative and prevents local admission from
  overcommitting the host.
- **admission:** `sparkwing queue` stall detection now ignores holder rows that
  draw no host resources and hold no semaphores. Zero-resource summary rows no
  longer receive stalled labels or cancel recovery advice.
- **release:** The built-in release pipeline no longer declares a stale local
  CPU pin. Local admission can now use the daemon's measured profile for release
  runs instead of an outdated hand-written budget.

## [v0.17.5] - 2026-07-14
### Fixed

- **admission:** Runs whose cost is still being measured now reach local
  admission instead of failing before their first node with an invalid-cost
  rejection. The daemon accepts every cost source emitted by the resolver and
  treats measuring and floor-derived CPU costs as backpressure.

## [v0.17.4] - 2026-07-14
### Fixed

- **admission:** Unpinned local runs now acquire host resources at node
  dispatch instead of reserving the measured peak for the whole DAG. Fast
  early nodes can run while a later memory-heavy node waits for capacity;
  explicit whole-run resource pins still reserve at run scope.
- **admission:** Daemon restart now restores CPU-overcommitted ledgers so
  current work can drain instead of blocking host admission. Memory remains a
  hard restore bound.

## [v0.17.3] - 2026-07-14
### Fixed

- **admission:** Measured and default CPU costs now act as backpressure, not
  a hard admission gate. Memory and explicit resource pins still gate
  admission strictly, while CPU pressure admits one memory-fitting run before
  stopping additional CPU-bearing work. A saturated host therefore keeps
  making progress instead of parking every queued run behind a CPU-only
  deficit.

## [v0.17.1] - 2026-07-13
### Added

- **admission:** A queued run now explains where its charge came from, not
  just its size. The waiting log line, `sparkwing queue` holder and waiter
  rows, and the queue JSON carry a short rationale beside the cost -- "needs
  5.0 cores (measured p99 over 12 runs)", "(first run, conservative default
  until measured)", "(explicit pin)", "(re-measuring at 2x prior charge)" --
  so the number reads as a decision, not an edict. Holder and waiter rows gain
  a `cost_rationale` field in `-o json` for a dashboard to tooltip.
- **cli:** `sparkwing repos` gains a per-repo deep dive. `sparkwing repos info`
  reports one repo's SDK pin against the latest release, the migration guides
  in between with their titles and summaries, its worktrees and any that pin a
  different version, its branch, commit, and clean/dirty state, whether the pin
  can open the machine's shared state database, and its pipelines with last-run
  status -- and prints one suggested next step when something is off. It
  defaults to the repo containing the current directory; `--repo` names another
  fleet member. An explicit `sparkwing repos list` verb now names the bare
  listing.

### Fixed

- **cli:** The `sparkwing queue` resource table now reconciles on screen. The
  external column reports the same smoothed external load the availability
  math actually used, so capacity - in use - reserved - external = available
  holds exactly rather than appearing off by the deadband. A one-line legend
  spells out that arithmetic, and "Running" and "Waiting" headers label the
  two tables.
- **admission:** Fully-cached runs no longer poison learned profiles. A run
  whose completed nodes are predominantly cache hits measured the cache, not
  the work, so it is excluded from duration and resource learning like a
  contended run -- its millisecond wall time can no longer collapse a
  pipeline's p50 or age out its real peak. `sparkwing runs stats` reports how
  many runs were excluded this way in a new CACHED column.

## [v0.17.0] - 2026-07-13
### Added

- **admission:** Capacity measurement is now honest about contention and
  pipeline change. A run the daemon flags as throttled by host contention no
  longer folds into the measured profile: it measured what it got, not what it
  wanted, so its reading only raises a per-pipeline demand *floor* (a lower
  bound) and never sets the measured peak or graduates the profile. While a
  version has not yet finalized a measured price it is charged a safety
  multiple (2x) of that floor -- and a contended run that consumed essentially
  its whole charge escalates the floor to the charge, so successive runs
  double in on true demand from below. `sparkwing runs stats --capacity` shows
  the operative floor and labels the source `floor`.
- **admission (Breaking):** Capacity profiles are now versioned by pipeline
  plan hash, advancing the runs-store schema from version 10 to version 11 --
  see the
  [migration guide](docs/migrations/v0.17.0.md#runs-store-schema-moves-to-version-11).
  A structural (DAG-topology) change re-measures the pipeline instead of
  pricing it on the previous version's samples: the changed version is charged
  2x its predecessor's peak (a large predecessor makes that exceed the box, so
  it runs alone and measures solo emergently) and stays in `measuring` until
  clean, uncontended runs finalize a new measured price. The queue and
  `runs stats --capacity` views show the `measuring` and `floor` sources, and a
  measuring run narrates itself (`re-measuring at N cores (2x prior charge)`).
- **cli:** The changelog is now a readable, offline docs topic.
  `sparkwing docs read --topic changelog` serves the release notes embedded
  in the binary like every other doc, and `sparkwing version --changelog`
  prints the notes for the installed release (pointing at the release page
  when newer versions exist). "What changed" is answerable from the binary,
  no browser required.
- **cli:** Operator-enforced version holds cap CLI self-upgrades.
  `sparkwing version hold --set v0.15` (or the `SPARKWING_VERSION_HOLD`
  environment variable) makes `sparkwing version update --cli` and
  `sparkwing update` refuse to install anything beyond the ceiling, so an
  agent cannot cross it against operator instruction; `sparkwing version`
  shows the active hold. A `vMAJOR.MINOR` hold caps a whole minor series
  (patches allowed, the next minor refused); `vMAJOR.MINOR.PATCH` is an
  exact ceiling.
- **cli:** A CLI version change now announces itself. The first run after
  the binary version changes prints a one-line pointer at the changelog and
  recovery docs, and `sparkwing info` surfaces the same pointer, so an
  upgraded fleet discovers new controls from its own command output instead
  of hoping agents browse the docs.
- **ops:** Headless hosts are operable without the CLI. A compiled pipeline
  binary now serves the admission surfaces for itself --
  `<binary> ops queue|doctor|stats|stats-reset|version` -- with the same
  output conventions (`-o pretty|json|plain`) and JSON shapes as
  `sparkwing queue` / `sparkwing doctor`. This makes concrete the principle
  *sparkwing does not require sparkwing*: the pipeline binary is the product,
  the CLI a developer convenience, and everything the CLI does at runtime the
  binary can do on its own. See the "Headless hosts" section of the CLI docs.
- **cli:** `sparkwing queue --profile NAME` inspects a controller's admission
  state through the same renderer as the local view: every concurrency key
  with its holders and waiters, plus each registered runner's free capacity.
  One vocabulary now reads local and cluster admission alike; it is the
  preferred replacement for `sparkwing cluster concurrency`, which narrows to
  a single namespace and is slated for removal once parity is complete.

### Fixed

- **admission:** Machine capacity is a living value. The daemon re-derives it
  at every start (never trusting a restored snapshot) and re-checks it on a
  slow cadence while running, so a hot instance resize or a runtime cgroup
  quota edit is picked up without a restart. Changes apply with a gentle
  deadband, are logged, and show in the queue header (`capacity changed: 4.0
  -> 8.0 cores`); a shrink never evicts a running holder -- it drains
  naturally while admission tightens -- and the clamp now also honors
  `cpuset.cpus`.
- **admission:** A holder that reclaimed its lease after a daemon restart can
  be cancelled. `sparkwing runs cancel` (and the daemon-first cancel path) now
  reaches a reattached run, not only runs admitted by the current daemon
  incarnation.
- **admission:** A liveness floor guarantees sparkwing never refuses all work.
  Whenever no run holds a host resource, the queue head is admitted regardless
  of the reserve or external load, so a fully loaded box still runs exactly one
  pipeline at a time rather than none; headroom sensing gates only the runs
  beyond that first. A sole run admitted under load says so
  (`admitted as sole run; host under external load ...`). The empty-host case
  first shipped in v0.16.5; this release extends the floor to every
  no-holder state.

## [v0.16.9] - 2026-07-13
### Fixed

- **admission:** Capacity profiles with obsolete CPU accounting are ignored
  on read and on the next profile update. Older samples could preserve
  impossible CPU peaks, causing admission to reserve too much CPU and block
  queued runs unnecessarily.

## [v0.16.8] - 2026-07-13
### Fixed

- **runs:** `sparkwing runs list` and `sparkwing runs status` now surface
  local admission waits as queued work. Runs waiting for host capacity show
  `queued (N/M)` in list output and an admission line in status output, while
  admitted and terminal runs keep their actual run status.
- **admission:** Queued runs now receive fresh position updates when the run
  ahead is admitted, so long waits no longer repeat an obsolete queue position.

## [v0.16.7] - 2026-07-13
### Fixed

- **admission:** `sparkwing queue` now evaluates holder liveness from the
  holder's process tree instead of only the root process. A wrapper or shell
  that is idle while child test processes are still active no longer appears
  as stalled, while an idle descendant tree still reports as stalled after a
  bounded grace window.

## [v0.16.6] - 2026-07-13
### Fixed

- **admission:** Removing a `.Resources()` declaration now clears the stored
  capacity pin the next time that pipeline or cluster-dispatched node runs.
  Previous versions could keep charging the last explicit pin from the local
  profile store or controller profile even after source stopped declaring it,
  so stale undersized pins survived code cleanup until an operator manually
  reset state.

This release also first shipped the host-capacity admission wave: no run is
rejected for exceeding host capacity (an oversized measured peak or explicit
`.Resources()` pin runs alone at the machine's grantable budget, with a loud
warning naming the pin and the machine, superseding the v0.16.4 behavior where
an oversized pin failed); measurement no longer overshoots (a reaped command's
CPU is amortized over its own wall time, derived rates are clamped to host
cores, and stored profile peaks are capped at host capacity); and clients
transparently reconnect and reattach across a daemon restart, idle-exit, or
version takeover.

## [v0.16.5] - 2026-07-13
### Fixed

- **admission:** A host with no sparkwing holders now admits the queue
  head even when external load leaves zero measured headroom. A saturated
  machine falls back to one sparkwing run at a time instead of letting the
  queue park forever.
- **daemon:** Restored admission state is resized to the current machine
  budget before the daemon serves, so a restart after a capacity change
  cannot admit new runs against stale totals.

## [v0.16.4] - 2026-07-12
### Fixed

- **admission:** Measured host costs that exceed the largest request the
  daemon can grant are capped before admission, so one bad profile cannot
  make a pipeline permanently never-admissible. Explicit `.Resources()`
  pins still fail loudly when they exceed host capacity.
- **local workspace:** Starting the local workspace with an explicit home
  no longer mutates the process-wide `SPARKWING_HOME` environment value.

## [v0.16.3] - 2026-07-12
### Added

- **orchestrator:** Operator recovery controls for bad measurements.
  `SPARKWING_BUDGET` gains an `ignore-external` term (usable alone or with
  a cap) that tells admission to stop subtracting measured non-sparkwing
  load -- the escape hatch for a misreading host sensor. `sparkwing queue`
  still shows the real external reading and adds an `external: ignored
  (operator setting)` line, and contention detection keeps using the real
  saturation, so observability stays truthful. `sparkwing runs stats
  --reset --pipeline <name>` clears a pipeline's learned capacity profile
  (samples, peaks, waits, contention tally) so it re-learns from a cold
  start after one freak run poisoned it, preserving any `.Resources()` pin
  and printing what it dropped; `--reset --all --yes` resets every
  pipeline. The daemon now logs a one-line note when a requested budget
  exceeds machine capacity and is clamped.

### Fixed

- **admission:** A run held in local admission now re-emits its wait
  status on a heartbeat (every 30s by default) instead of going silent
  after the first "queued for local admission" line, so a long wait reads
  as healthy backpressure rather than a hang. The "admitted; starting run"
  line prints after any wait.
- **runs cancel:** Cancelling a run that already finished now reports the
  truth ("already finished (success) -- nothing to cancel") as a no-op
  success instead of a misleading "not found"; a genuinely-unknown run id
  still fails as not found.
- **exec:** A command killed mid-run by cancellation now reports "command
  terminated by cancellation (signal: killed)" instead of "command failed
  to start"; the started process's exit code of -1 no longer collides with
  the never-started sentinel. Genuine launch failures keep their wording.

## [v0.16.2] - 2026-07-12
### Fixed

- **admission:** The local admission daemon now backfills a smaller run
  past a queued heavier one when the free budget fits it, and stops
  backfilling once a holder younger than the waiting run is what keeps it
  from fitting. Weighted local groups and host cores no longer idle
  capacity behind a run that cannot currently fit, matching the
  controller's weighted-queue admission.
- **docs:** The v0.16.0 migration guide now documents the runs-store
  schema move to version 10 (one-way migration; an older binary refuses
  a newer database by naming the version it needs), which the published
  v0.16.0 and v0.16.1 tags' embedded copies lack.

## [v0.16.1] - 2026-07-12

Published from the same release line as v0.16.0, so this tag also predates
the daemon-ledger backfill extension and the reconciliation entries above;
the next release is a strict superset.

### Fixed

- **admission:** Weighted queue admission can backfill a later runnable
  waiter behind an earlier waiter that is too large for the current
  remaining budget, while still stopping once a younger backfilled holder
  is what blocks the older waiter. Re-arriving queued work also preserves
  its original arrival order, so a polling waiter does not lose its place.

## [v0.16.0] - 2026-07-12

Published from a release line that branched before the weighted-queue-capacity
backfill fix reached the mainline, so this tag ships without it. That fix
landed in v0.16.1; its extension to the local admission daemon's ledger lands
in the next release, which is a strict superset of everything below.

This release carried the concurrency rebuild. Local runs are admitted by the
local admission daemon (`sparkwingd`) instead of box slots and store-side
concurrency slots; the `box-slots` and `maintenance` command trees are removed
in favor of `sparkwing queue` and the new `sparkwing doctor`; the runs-store
schema advances from 6 to 10 and stamps the minimum sparkwing version it needs;
and resource measurement now costs a run by its whole process tree. It also
bounded the plan-level concurrency admission acquire, so a wedged store surfaces
a concrete error rather than a run left heartbeating with every node pending.
See [docs/migrations/v0.16.0.md](docs/migrations/v0.16.0.md) for the breaking
changes and upgrade steps.

### Added

- **orchestrator:** The local admission daemon detects its own cgroup
  limits at startup and clamps capacity to the container it runs in, so a
  6 GiB container on a 24 GiB host plans against 6 GiB rather than the host
  total -- the oversubscription that quietly returned inside CI containers.
  External-load sensing measures the container's own CPU and memory usage
  where the cgroup provides it, an explicit `SPARKWING_BUDGET` still caps
  below the detected limit, and `sparkwing queue` shows a `container limit`
  row against the host. Linux reads cgroup v2 (with a v1 fallback); macOS
  has no container path and uses the host.
- **cli:** `sparkwing repos` lists the machine's fleet of sparkwing
  repos -- derived from observed runs unioned with `repos.yaml` -- with
  each repo's SDK pin, last run, and how many migration guides it is
  behind. Linked git worktrees fold into their primary checkout; a
  worktree pinned differently is reported as a detail line.
- **cli:** `sparkwing repos update` bumps the fleet's SDK pins in one
  sitting with a compiled per-repo verdict: `clean` when the bump
  compiled and every pipeline plan is byte-identical, `plan-differs`
  with a structured node/dep/step diff when a plan changed shape, and
  `broken` with the actual error plus the crossed migration guides.
  Dry-run by default; `--apply` commits per repo, `--verify` runs each
  repo's pre-commit gate, `--repo` scopes to one.
- **store:** the state database records the minimum sparkwing version
  required for its schema. A binary meeting a newer database refuses
  with `this state database needs sparkwing >= vX; you have vY; run
  sparkwing version update --cli` instead of a bare schema number,
  falling back to schema numbers for databases stamped before this
  shipped.


- **runner:** A registered runner on a box that also runs local pipelines
  can route controller-dispatched work through the box's local admission
  daemon, so both work sources share one FIFO queue and one arbiter
  instead of competing blindly. Set `local_admission: true` in
  `agent.yaml` (or `--local-admission` on `sparkwing-runner runner`);
  each claimed node then submits the same admission request a local run
  does. The runner advertises the daemon's live free capacity (cores,
  memory, queue depth) to the controller on every claim and heartbeat --
  surfaced in the agents view -- so the scheduler mostly dispatches work
  that fits, with local admission as the backstop. `local_reserve`
  (`SPARKWING_LOCAL_RESERVE`, e.g. `2,4gb` or `10%`) holds capacity back
  from what the runner advertises. Leases carry an origin (local vs
  controller) shown as an `ORIGIN` column in `sparkwing queue`.
- **controller:** In cluster/runner-pod mode, runner pod requests and
  limits are sized from the same resolution the local daemon uses: an
  explicit `.Resources()` pin wins, else the node's measured peak
  cores/memory, else a conservative default. Limits follow a policy of
  generous CPU headroom (compressible) and tight memory headroom (OOMs).
  The controller folds finished cluster runs' measured metrics into
  per-node and rollup profiles and records a `resource_pin_drift` event
  when an applied pin has drifted far from the measured peak -- the same
  under-/over-pinned warning the local system emits, now load-bearing
  where the kube scheduler believes the declaration.
- **cli:** The admission daemon detects and surfaces *contended* runs --
  a run measurably slower than its own measured p99 while the host is
  saturated by non-sparkwing load, distinguished from a wedged holder
  (`stalled`) and a legitimately long one. `sparkwing queue` marks the
  holder `(contended)` with a one-line explanation, a finished contended
  run prints an end-of-run attribution (`took 12m vs p50 8m30s; host
  saturated 62% of the run`), the queue's recent-events line counts
  contended runs, and `sparkwing runs stats --capacity` shows each
  pipeline's contended share. Detection is sample-gated (an unprofiled
  run is never flagged) and observability-only -- it never changes an
  admission decision.
- **cli:** A single machine budget caps how much of the host sparkwing
  may use. Set `SPARKWING_BUDGET` to a core count, a percentage, or a
  cores-and-memory pair (`6`, `50%`, `6,8gb`); it caps the admission
  ledger below the machine total, and `sparkwing queue` shows the cap as
  its own headroom row (`budget 6.0 cores (machine 10.0)`). Appending
  `enforce` hardens the cap at the OS level -- a cgroup v2 wall on Linux,
  background QoS scheduling on macOS -- in addition to admission.
  Measured admission remains the primary mechanism; the budget is the one
  machine-level knob that complements it.
- **cli:** `sparkwing runs stats --capacity` now shows each pipeline's
  CPU and memory distributions (p50/p95/peak across the same window of
  recent runs that backs the duration percentiles) instead of a lone
  peak, so a steady pipeline and a spiky one no longer look alike. The
  percentiles are informational; admission still charges the measured
  peak, because under-reserving a spiky pipeline recreates the
  oversubscription admission exists to prevent.
- **cli:** capacity stats gain per-pipeline queue-wait percentiles: the
  daemon-admission wait (submit to grant, the exact interval run
  durations exclude) is recorded per run and shown as a WAIT p50/p99
  column. "p50 duration 8m, p50 wait 3m" answers "is this box too
  small" with a measurement instead of a guess. Observability only --
  no admission behavior changes.
- **cli:** `sparkwing queue` names the repo each holder and waiter came
  from (a REPO column in pretty output, a `repo` field in JSON), so a
  queue full of identically-named pipelines from different checkouts
  stays readable on a shared machine. Runs launched outside a git
  repository show a dash.
- **cli:** `sparkwing queue` renders a one-line recent-events summary in
  its header ("last 24h: 142 runs, median wait 4s, 3 evictions (key:
  land), 1 queue-timeout" -- zero categories are omitted), backed by a
  bounded rolling window the daemon persists across restarts; JSON
  carries the structured window in an `events` field. Attached child
  runs now render indented under their parent holder with an attached
  marker and a `parent` field in JSON, instead of appearing as runs
  that hold nothing.
- **cli:** `sparkwing doctor` -- the one safe repair verb. It removes
  only provably-dead local state (run rows left `running` with no live
  process or daemon lease, leftover box-slot lock files whose owner is
  gone, local-scope concurrency rows whose run has ended, and run
  directories with no run row) and reports what it found and did.
  `--dry-run` reports without changing anything. It never kills a
  process, never touches the daemon's live state, and never touches
  cluster-scoped (global) rows, so it is safe to run at any time and a
  healthy machine reports a clean bill.
- **cli:** `sparkwing queue` now names the serving daemon's version and
  uptime in its header, and both `sparkwing queue` and `sparkwing doctor`
  warn when an older-pinned pipeline binary is admitting outside the
  daemon through a held box-slot lock -- with the fix (bump that repo's
  sparkwing pin) so a mixed machine cannot silently oversubscribe.
- **cli:** `sparkwing queue` now explains a host-pressure wait instead of
  looking idle: the resource table shows each host dimension's reserved
  margin, measured external (non-sparkwing) load, and what remains
  grantable, and every queued run carries a one-line blocking reason
  ("needs 5.0 cores; 4.8 available (external load 3.2)"). The waiting
  run's stderr queue line and the dashboard queue endpoint carry the same
  fields, so a queue with free capacity but no holders no longer reads as
  a bug.
- **sdk:** `Plan.Resources(...)` and `JobNode.Resources(...)` (plus the
  `JobGroup` equivalent) declare optional cold-start cost hints via
  `sparkwing.Cores(n)` and `sparkwing.MemoryGB(n)`: advisory estimates of
  peak CPU and memory that admission uses before a measured profile
  exists for the pipeline. Hints flow into the plan snapshot; they are
  never limits, and pipelines that declare none keep today's behavior.
- **wire:** New `pkg/wingwire` package defines the versioned JSON wire
  protocol (newline-delimited JSON) shared by the upcoming local
  admission daemon and its clients: version handshake, all-or-nothing
  admission request/grant, queue-position and eviction events, lease
  release/re-attach, drain handshake, and a queue-state snapshot. It
  also defines `SPARKWING_LEASE_TOKEN`, the single environment variable
  a parent run will use to pass its lease to child runs. Data types
  only -- the daemon and its transport ship separately.

### Changed

- **orchestrator (Breaking):** Local runs are admitted by the local
  admission daemon (`sparkwingd`) instead of box slots and store-side
  concurrency slots. At run start the process submits one all-or-nothing
  admission request (host resources from `.Resources()` hints plus every
  box- and run-scoped plan-level `.Concurrency()` group) and holds the
  granted lease on an open daemon connection for the run's lifetime; a
  queued run prints a single stderr queue-position line. Child runs
  inherit by attaching to the parent's lease via `SPARKWING_LEASE_TOKEN`;
  the `SPARKWING_PLAN_ADMISSION_*` trigger-env chain is removed. Node-
  level box/run-scoped groups become short-lived daemon acquisitions;
  global-scope groups keep the shared-store path. When a run process
  dies without releasing, the daemon frees its lease immediately and
  finalizes the run row as cancelled with an interrupted reason.
  Cluster runner pods are unaffected: work admitted by the Kubernetes
  scheduler never engages the daemon. See
  [migration](docs/migrations/v0.16.0.md#removed-cli-verbs-flags-and-environment-variables).
- **cli:** `sparkwing dashboard start` handshakes a running dashboard
  over a new unauthenticated version endpoint: a newer CLI drains and
  replaces an older resident dashboard, while an older CLI refuses to
  replace a newer one and leaves it running. A resident dashboard that
  observes the shared database migrate past the schema it understands
  now exits cleanly with a logged reason instead of serving 500s. The
  startup deadline is generous under load, fails fast when the
  supervisor exits early, and reports the new instance's own startup
  log on timeout.
- **cli (Breaking):** `sparkwing run` drops `--sw-box-slots` and
  `--sw-no-wait` (with the `SPARKWING_BOX_SLOTS_PIN` /
  `SPARKWING_BOX_NO_WAIT` variables): local runs no longer take box
  slots, so there is no per-run cap to pin and queue waits cancel
  cleanly with Ctrl-C. See
  [migration](docs/migrations/v0.16.0.md#removed-cli-verbs-flags-and-environment-variables).
- **cli (Breaking):** The `box-slots` command tree
  (`show`/`list`/`set`/`release`/`sweep`) and `sparkwing maintenance` are
  removed, along with the `SPARKWING_BOX_SLOTS` cap baseline and the
  `SPARKWING_BOX_SLOT_STALL_TTL` override. The admission daemon owns host
  admission and converges local state on its own, so the inspect-and-tune
  and manual-sweep verbs have no remaining purpose. Read live admission
  with `sparkwing queue`; clear provably-dead leftovers with the new
  `sparkwing doctor`. See
  [docs/migrations/v0.16.0.md](docs/migrations/v0.16.0.md#removed-cli-verbs-flags-and-environment-variables).
- **sdk (Breaking):** `ConcurrencyLimit.HostAdmission` and
  `Plan.HostAdmission()` are removed: host admission is universal and
  implicit under the daemon, and `ScopeBox` means locality only. See
  [migration](docs/migrations/v0.16.0.md#hostadmission-removed-from-the-sdk).
- **sdk (Breaking):** Local runs handle SIGINT/SIGTERM: the run cancels
  cleanly and its row finalizes as `cancelled` naming the signal,
  instead of exiting with the row stuck `running`. See
  [migration](docs/migrations/v0.16.0.md#interrupted-runs-finalize-themselves).
- **controller (Breaking):** The trigger API drops the `plan_admission`
  request block; spawned children no longer inherit plan-level
  concurrency holders through the controller. See
  [migration](docs/migrations/v0.16.0.md#the-trigger-api-drops-plan_admission).
- **store (Breaking):** The runs-store schema advances from version 6 to
  10 for the concurrency rebuild -- the admission ledger, measured
  per-command CPU and memory columns, queue-wait and contended-run
  bookkeeping, and a minimum-version stamp. The store migrates a database
  forward on first open by a newer binary and the step is one-way; a
  binary older than the database needs refuses to open it by name rather
  than printing a bare schema number. Bump every sparkwing pin that
  shares the machine in one sitting. See
  [migration](docs/migrations/v0.16.0.md#runs-store-schema-moves-to-version-10).
- **sdk:** Resource measurement now covers a run's whole process tree,
  not just the orchestrator. Each `sparkwing.Bash` / `sparkwing.Exec`
  command's CPU and peak memory -- read from its `wait4` rusage, which
  aggregates the command's entire reaped subtree -- fold into the node's
  measured profile, so a pipeline whose work is a test suite, a linter,
  or a shell step is costed by what those subprocesses actually drew
  rather than the near-zero the orchestrator itself uses. Admission
  therefore stops over-admitting subprocess-heavy runs onto one box.
  Measurement also covers subprocesses a pipeline spawns directly, outside
  the `sparkwing.Bash` / `sparkwing.Exec` wrapper: their CPU is read from
  the run's `RUSAGE_CHILDREN` so raw `os/exec` work is costed too and no
  longer measures as zero. Measured costs change materially: existing
  capacity profiles re-learn from the runs after upgrade. Each spawned
  command also runs in its own process group, so cancelling a node tears
  down the whole subtree instead of orphaning forked grandchildren.

### Fixed

- **orchestrator:** Plan-level concurrency admission now bounds the initial
  store acquire call before dispatch. A wedged local store or admission
  backend surfaces as a concrete plan-concurrency acquire error instead
  of leaving a run heartbeat alive with every node still pending.
- **orchestrator:** A same-repo child trigger (a `RunAndAwait` to a
  sibling pipeline) now dispatches from the running parent's own compiled
  binary, so it works from a project directory that has no git identity
  instead of failing. When a child genuinely cannot be located the error
  names the real cause (no git identity to resolve a sibling checkout
  from) and real fixes, and no longer recommends `sparkwing pipeline add`,
  a verb the CLI does not have.
- **store:** Upgrading a database whose `pipeline_profiles` table
  predates the `cpu_measured` column now backfills the flag for carried
  rows with a positive measured peak, matching how admission qualifies
  them: a legacy positive peak could only have come from a sampler that
  measured CPU. Rows survive the migration column-add; a version bump
  no longer risks resetting learned capacity to cold start.
- **orchestrator:** A node-level `OnLimit:CancelOthers` concurrency group
  now preempts across runs through the daemon instead of silently
  queueing: the newest arrival evicts the older holder, the superseded
  run finalizes as cancelled naming the contested key and the superseding
  run, and a holder that ignores the eviction is force-released once its
  `CancelTimeout` elapses.
- **cli:** `sparkwing runs cancel` cancels a local run through the
  admission daemon first, so the recovery command the queue view
  recommends for a stalled holder works on a bare machine with no
  dashboard and no profile. It cancels a run in either admission state --
  a holder or a run still queued for admission -- so "get this out of
  line" works on a waiting run too: the daemon removes it from the queue,
  re-states the positions behind it, and winds it down to a cancelled
  status. The daemon signals the run on the same clean path an operator
  interrupt uses; cluster runs and runs the daemon does not hold still
  route through the controller.
- **orchestrator:** A pipeline that mostly waits (a poller, approval
  waiter, or lock holder) is now costed by measurement once it has enough
  samples, instead of being pinned at the conservative cold-start default
  forever. A healthy sampler that measured a genuine near-zero CPU peak
  admits the run at its measured memory plus a small core floor; a
  platform whose sampler cannot measure CPU still holds the conservative
  default, so a blind zero is never mistaken for a real measurement.
- **cli:** `sparkwing queue` no longer prints "clears in ~-" when no clear
  estimate is available; the header simply omits the clause. `sparkwing
  runs stats --capacity` prints a pin-drift warning as a footnote below
  the table rather than crammed into a column, so the table stays aligned.
- **orchestrator:** A SIGINT-cancelled run names the signal as `SIGINT`
  (and SIGTERM as `SIGTERM`) in its terminal reason, instead of the bare
  lowercase "interrupt".
- **cli:** Compiling a `.sparkwing` project nested inside another Go
  module's workspace no longer fails with a bewildering "main module does
  not contain package". When an enclosing `go.work` does not list the
  project, the build ignores that workspace and compiles the project as
  the self-contained module it is.
- **wingd:** A self-spawned admission daemon reliably writes its log at
  `<home>/wingd/d.log`. The spawn now creates the daemon directory before
  opening the log and rotates the log once past a size cap, and the daemon
  records election, headroom transitions, reattach-grace outcomes,
  evictions, orphan finalizations, and drains -- the log is no longer
  empty exactly when someone needs it to debug the daemon.

## [v0.15.12] - 2026-07-12
### Fixed

- **admission:** Weighted queue admission now backfills smaller waiters when
  the oldest waiter cannot currently fit, without allowing younger backfilled
  holders to starve that older waiter.

## [v0.15.11] - 2026-07-12
### Fixed

- **orchestrator:** Dispatch wait timeouts now distinguish bounded
  admission queue waits from wedged node dispatch, so queued work is not
  failed while it is still waiting within its configured queue policy.
- **release:** Release branches can now cut maintenance tags with a remote
  branch freshness fence, and release commits include the `.sparkwing`
  module checksums needed for the pinned SDK version.

## [v0.15.10] - 2026-07-12
### Fixed

- **cli:** `sparkwing runs cancel` without `--profile` now cancels runs in the
  local state store and releases any local concurrency budget they held or were
  waiting on, so orphaned daemonless runs no longer leave phantom admission
  pressure behind.
- **store:** Added `Store.CancelRun` for local run cancellation that marks the
  run and unfinished nodes cancelled, then releases concurrency waiters and
  holders through the same promotion path used by normal waiter cancellation.

## [v0.15.9] - 2026-07-12
### Fixed

- **store:** Coalesced cache followers now execute fresh when their leader is
  cancelled, superseded, lost, or otherwise exits without an inheritable node
  result, instead of inheriting a synthetic cancellation or executor-loss
  failure. Live followers also survive the maintenance sweep long enough to
  promote and re-run the work.
- **cli:** Source-built `sparkwing pipeline new` scaffolds now fall back to
  the current SDK release when no build-version stamp is available.

## [v0.15.8] - 2026-07-11
### Fixed

- **sdk:** Cancelling `sparkwing.Bash` or `sparkwing.Exec` now terminates
  the command's process group on Unix, so shells and tools that spawn
  children do not leave work running after the Sparkwing command is
  cancelled. Windows continues to cancel the direct child process.

## [v0.15.7] - 2026-07-11
### Fixed

- **store:** Concurrency maintenance and waiter promotion now preserve queued
  waiters whose runs are still live, while reclaiming abandoned waiters before
  they can consume a freed slot. This keeps queued plan and node admission from
  being evicted by a maintenance sweep or by an abandoned FIFO head.
- **cli:** A pipeline scaffolded by a source-built `sparkwing` (one with no
  release version stamp, e.g. `go install`ed from a checkout) now pins the
  current SDK release in its generated `.sparkwing/go.mod` instead of a stale
  fallback, so `sparkwing pipeline new` followed by a build is reliably green.
  The pre-push version-freshness gate now also fails when that scaffold fallback
  pin falls behind the latest released SDK, keeping it honest as the SDK advances.

## [v0.15.6] - 2026-07-10
### Fixed

- **orchestrator:** Local runs that declare plan-level concurrency now release
  their provisional `box-slots` holder while queued for plan admission, so
  queued runs do not consume a host execution slot before their first node can
  dispatch.

## [v0.15.5] - 2026-07-10
### Fixed

- **store (Breaking):** The runs-store schema moved from version 5 to 6 so
  existing databases gain `concurrency_holders.queue_arrived_at` before
  admission state queries read it, and exported concurrency state structs now
  carry queue-arrival timestamps. The store auto-migrates on open; upgrade all
  Sparkwing binaries that share a state database before running mixed-version
  admission workloads. See [the migration guide](docs/migrations/v0.15.5.md#runs-store-schema-5-to-6).
- **orchestrator:** Parent node timeouts now pause while `RunAndAwait` children
  are queued for plan-level admission, then resume once admission clears, when
  the await uses the parent job timeout. Explicit `WithFreshTimeout` values
  still bound the total child wait.

## [v0.15.4] - 2026-07-10
### Changed

- **sdk:** (Breaking) Repeated plan-level `Concurrency` calls now compose
  independent whole-run budgets instead of replacing the prior gate. Call
  `plan.Concurrency(nil)` before the replacement when preserving overwrite
  behavior; see [the migration guide](docs/migrations/v0.15.4.md).
- **sdk:** (Breaking) `ConcurrencyLimit` and
  `client.TriggerPlanAdmission` added fields for host admission. Callers using
  unkeyed Go struct literals must switch to keyed literals before upgrading;
  see [the migration guide](docs/migrations/v0.15.4.md).
- **sdk:** Plan-level `Concurrency` groups can now opt into host admission
  with `ConcurrencyLimit.HostAdmission`, giving local runs one plan-owned
  queue for host execution budget instead of double-holding the default
  `box-slots` queue. Exactly one plan-level group may own host admission.

### Fixed

- **controller:** Inherited plan admission now verifies that a parent plan
  holder actually owns host admission before passing that ownership to child
  runs, so a normal plan-level queue cannot be upgraded by request payload.
- **orchestrator:** Local runs that wait on host-admission plan concurrency
  release the provisional `box-slots` holder while queued, reacquire pinned
  slots only after admission, and always release the plan holder if the
  pinned reacquire fails.
- **store:** Concurrency admission now prunes holders whose runs already
  reached a terminal state before computing budget or promoting waiters, and
  local maintenance uses an owned in-progress claim so startup sweeps stay
  bounded without suppressing retries after failure.

## [v0.15.3] - 2026-07-09
### Fixed

- **controller:** Host box-slot admission now grants freed slots to queued
  waiters in arrival order instead of letting whichever waiter polls first
  claim the slot. The queue head retries on a short drain cadence, so a freed
  slot does not sit idle behind the normal jittered poll interval; `NoWait`
  callers also respect the existing queue.

## [v0.15.2] - 2026-07-09
### Fixed

- **orchestrator:** A run queued for a box slot now reaps a stalled holder
  automatically instead of blocking behind it indefinitely. When the wait-path
  stall probe flags a holder whose run has gone silent past the stall TTL, the
  waiter SIGTERMs (then SIGKILLs) it to free the slot, so one wedged run no
  longer deadlocks every other run on the host. Set `SPARKWING_BOX_NO_AUTOREAP=1`
  to restore the previous report-only behavior.
- **orchestrator:** Plan-level `Concurrency` runs superseded before their first
  node dispatch now finish as `cancelled` instead of `failed`, so admission
  churn is distinguishable from a real test or job failure.

## [v0.15.1] - 2026-07-08
### Fixed

- **controller:** Queued concurrency waiters now recover when a holder row
  disappears without a release notification. Waiter polling promotes the FIFO
  head under the same per-key lock as admission, and the controller's notify
  stream uses that waiter-resolution path while preserving the documented
  `key_not_found` terminal event.

## [v0.15.0] - 2026-07-08
### Fixed

- **orchestrator:** Inherited plan-level `Concurrency` admissions now
  refresh the inherited holder lease while the child run is active, so a
  child that outlives its parent keeps the shared budget reservation
  visible instead of letting overlapping work enter the same gate.
- **sdk:** `Plan.Concurrency` now accepts an optional cost argument, matching
  node-level `Concurrency(group, cost)`, so whole-run gates can participate in
  cost-weighted budgets instead of always charging one unit.
- **run:** Local child runs launched through `sparkwing.Bash` or
  `sparkwing.Exec` inherit active plan-admission handles through
  context-scoped command env, and nested `sparkwing run` processes read those
  handles back into inherited plan admission without mutating global process
  environment.

## [v0.14.1] - 2026-07-08
### Fixed

- **orchestrator:** `RunAndAwait` child runs now inherit active
  plan-level `Concurrency` admissions from parent and ancestor runs, so
  nested child workflows do not queue behind an ancestor-held plan gate
  and stall until timeout. Admission sets are preserved across local,
  controller, mirrored, and S3 trigger enqueue paths, and inherited
  children observe holder state without extending the owning run's lease.

## [v0.14.0] - 2026-07-02
### Added

- **cli:** `sparkwing box-slots list` prints one row per box-slot holder
  lock file -- owner pid, claim time, run id, live/stale (a non-blocking
  flock probe), and lock path -- and `sparkwing box-slots release
  <lockfile>` frees a slot: a stale file is removed outright, a live
  holder is refused unless `--force`, which SIGKILLs the owner (guarded
  against pid recycling) before removing the file. Both verbs read only
  the filesystem and flock state, never the state database, so they work
  while `state.db` is wedged.
- **run:** local runs annotate their box-slot holder lock file with a
  `run=<runID>` line once the run id exists, so a wedged holder is traced
  to its run by reading the file. The lock file layout is now a
  documented, versioned contract -- see
  [docs/box-slot-lockfile-contract.md](docs/box-slot-lockfile-contract.md).
- **store:** `SPARKWING_SQLITE_BUSY_TIMEOUT_MS` overrides the SQLite
  `busy_timeout` (default 30000 ms) for both read-write and read-only
  opens. A set-but-invalid value fails the open loudly instead of
  silently reverting to the default.
- **cli:** `sparkwing box-slots sweep` reports *stalled* holders -- live
  processes whose annotated run's envelope log has gone silent past a
  threshold (default 30m, `SPARKWING_BOX_SLOT_STALL_TTL`), or that held
  a slot that long without ever starting a run. The envelope's mtime is
  the stall signal because a live-but-wedged process keeps heartbeating;
  the envelope only moves when the run makes progress. Report-only by
  default; `--reap` kills each stalled owner via SIGTERM, a 10s grace,
  then SIGKILL, with every signal re-verified against the same lock
  file, pid, and flock so a recycled pid is never killed. Reads only the
  filesystem and flock state, so it works while `state.db` is wedged.
  See [docs/box-slot-lockfile-contract.md](docs/box-slot-lockfile-contract.md).
- **run:** a run queued for a box slot now names its blocker: while
  waiting, it probes for stalled holders about every 30 seconds and
  prints the pid and evidence, pointing at `box-slots sweep` /
  `sweep --reap`. The wait path never kills anything itself.
- **cli:** `box-slots sweep` rows split the stall age into the
  envelope-write age and a corroborating newest-file age under the run's
  directory, so a healthy run inside one long output-quiet node shows
  fresh node-file writes despite a silent envelope; and each `--reap`
  attempt and store wedge verdict emits one structured log line with
  stable `outcome` / `kind` fields for dashboards to count.

### Fixed

- **run:** plan-level `Concurrency` now honors the group's `Scope` and
  `QueueTimeout`; both were silently dropped. The whole-plan slot used
  to coordinate on the bare group name, so a `ScopeBox` group and a
  global group sharing a name aliased onto one budget; the plan key now
  goes through the same scope-qualified scheme node-level groups use
  (a plan group and a node group with the same name and scope now
  correctly share one budget). A queued plan also waited forever
  regardless of any configured `QueueTimeout`; a non-zero timeout now
  bounds the wait and fails loud, naming the group, the timeout, and
  the current holder. Zero keeps the wait-forever behavior.
- **run:** store-polling loops (concurrency waiter resolve, slot and
  run/node heartbeats, approval polls, child-run waits, trigger claims)
  no longer spin invisibly against a state database wedged by another
  live process. Each loop carries a wedge budget: once every store call
  has failed continuously for longer than `SPARKWING_STORE_WEDGE_BUDGET`
  (default 5m; an invalid value errors loudly at loop start), the loop
  fails with an error naming the condition, the elapsed time, the last
  store error, and the `box-slots list` command that locates the wedging
  holder. A SQLite "locking protocol" error fails immediately -- that
  state never clears by retrying. Waiter-resolve loops previously failed
  a queued node on the first transient error; they now ride out a streak
  up to the budget, so a one-off `SQLITE_BUSY` no longer kills a queued
  node.
- **cli:** `sparkwing update` no longer strands an unpublished build on an
  unsupported version line. A CLI installed from a commit (a pseudo-version
  such as `v1.6.2-0.<timestamp>-<hash>` left over from the pre-1.0 v1.x
  tags) sorts above the published `v0.x` latest, so the downgrade guard
  used to refuse the move to the real latest without `--force`. The guard
  now protects only between two published releases; an unpublished
  pseudo-version or `+dirty` build re-baselines to the published latest
  with a clear note instead of being treated as newer.

## [v0.13.0] - 2026-06-23
### Changed

- **run:** The host box-slot semaphore is on by default. With
  `SPARKWING_BOX_SLOTS` unset, `sparkwing run` now caps concurrent
  orchestrator processes on a host at `max(1, NumCPU/workers-per-run)`
  instead of running uncapped. Overlapping local runs queue ("waiting
  for box slot...") rather than all proceeding, which stops concurrent
  runs against a shared local SQLite backend from saturating the single
  writer and collapsing under lease-heartbeat failures. A single run
  never blocks on itself and cluster mode does not use the semaphore.
  Restore the previous behavior with `SPARKWING_BOX_SLOTS=off` (or
  `--sw-box-slots off`).

## [v0.12.0] - 2026-06-22
### Added

- **storage:** Mode 2 (S3-only) deployments now coordinate across runners
  without a database -- dispatch claims, debug pauses, approval gates, and
  pipeline-trigger enqueue with child-trigger idempotency -- as discrete
  object-store records mutated under conditional-write compare-and-swap
  (S3 If-None-Match/If-Match and the GCS/Azure equivalents). When the
  configured endpoint does not enforce write preconditions, these
  operations report not-supported so callers fall back to Postgres
  (Mode 3) or a hosted controller (Mode 4) rather than coordinate
  unsafely. Heavily contended coalesce keys see higher tail latency than
  Postgres: each mutation is a read-modify-write retry against one object.
- **install:** A Mode 3 (Postgres) Terraform module
  (`install/terraform/mode3-postgres`) provisions the managed-Postgres state
  backend for cross-runner coordination, shipping with an offline `terraform
  plan` test harness and a CI gate. Mode 3 is the database-backed path callers
  fall back to when their object store does not enforce write preconditions.

### Fixed

- **store:** A shared `state.db` no longer fails live runs with `database is
  locked (SQLITE_BUSY)` when many `sparkwing run` processes write
  concurrently. The state DSN now sets `synchronous=NORMAL` (the
  WAL-recommended setting: fsync at checkpoint, not on every commit), and the
  concurrency lease heartbeat became policy-aware -- `CancelOthers` keeps a 3s
  cadence so a supersede is observed within ~3s, while the other policies
  refresh on `lease/3` with a `lease/4` busy-wait bound, cutting heartbeat
  write volume ~20x without changing reclaim latency. No schema change, no
  migration.

## [v0.11.2] - 2026-06-20
### Fixed

- **controller:** Local orphan reconciliation now folds the run-level
  heartbeat into its liveness check, so a live run parked waiting on a
  plan concurrency slot (no nodes dispatched yet) is no longer falsely
  reaped as `orphaned`. `started_at` stays the backstop, so a crashed
  orchestrator that never heartbeats is still reaped exactly as before.
  No schema change, no migration.

## [v0.11.1] - 2026-06-18
### Fixed

- **cache:** Artifact staging rejects a producer manifest whose entry path
  escapes the consumer workspace (v0.11.1, patch). A `../` traversal already
  errored; an absolute path now errors too instead of being silently rooted
  back under the workspace. Staging writes nothing outside the consumer
  workspace. Defense in depth: manifests are produced internally today, but
  staging writes blobs to disk at manifest-declared paths, so an untrusted
  path is the realistic vector. No schema change, no migration.
- **release:** The schema-break gate and the `--bump` version baseline now
  resolve the previous release from the highest `v0.x` tag, skipping the
  retracted `v1.x` tombstone line. Previously they picked the highest semver
  tag overall (the `v1.6.1` tombstone, kept only to hold the Go module
  `@latest` pointer), so the gate saw a phantom schema change and demanded a
  `(Breaking)` marker on every release even when the runs-store schema was
  unchanged. No schema change, no migration.

## [v0.11.0] - 2026-06-17
### Added

- **sdk:** Node artifacts move files between nodes. A producer declares
  its output files by glob with `Outputs`; a consumer pulls a producer's
  published files with `Consumes` (which implies `Needs`), and `Into`
  relocates the staged set under a prefix. `JobGroup.Outputs` and
  `JobGroup.Consumes` apply the same at group scope. Files stage into the
  consumer's workspace before it runs; data values still travel as typed
  `Ref[T]`. See the [node artifacts guide](docs/artifacts.md).
- **cache + sdk:** Artifact capture and staging. A producer publishes its
  declared files content-addressed every run, and a consumer stages an
  immutable snapshot of them before running. Publishing and staging are
  independent of memoization: a cache hit carries the producer's artifact
  manifest forward, so a downstream `Consumes` stages the same files
  whether the producer ran or hit. Artifacts flow identically in
  in-process and distributed execution.
- **controller:** Node artifact-manifest endpoint
  (`POST /api/v1/runs/{id}/nodes/{nodeID}/artifact-manifest`) records a
  node's published-artifact manifest digest, so distributed workers
  persist artifact edges through the controller the way the local store
  does.

### Changed

- **store (Breaking):** The runs-store schema moved from version 4 to 5 so
  an existing schema-4 database gains the `nodes.artifact_manifest` column
  on open. The column shipped with node artifacts, but the schema-version
  constant stayed at 4, so a database already at schema 4 (anyone on v0.9.2,
  v0.9.3, or v0.10.0) never ran the additive migration and every node read
  failed with "no such column". The store auto-migrates on open, so a plain
  CLI or controller upgrade needs no action; a module that pins an older
  sparkwing and shares the same state database must bump the pin. See
  [migration guide](docs/migrations/v0.11.0.md#runs-store-schema-4-to-5).
- **storage (Breaking):** The exported `pkg/storage.StateStore` interface
  gained `SetNodeArtifactManifest(ctx, runID, nodeID, manifestDigest
  string) error`. The bundled backends implement it; a custom `StateStore`
  implementation must add the method to satisfy the interface. See
  [migration guide](docs/migrations/v0.11.0.md#statestore-implementers-add-setnodeartifactmanifest).
- **cli:** Managed git hooks (pre-commit, pre-push, post-commit) now render
  quietly by default: one progress line and a one-line pass/fail status with
  the run id, instead of streaming every step into the commit or push. On
  failure the hook surfaces the failing step's error; the full log stays
  retrievable with `sparkwing runs logs --run <id>`. A new
  `SPARKWING_LOG_FORMAT=quiet` selects this view for any run; export
  `pretty` or `json` before the git command to restore the full stream.
  Existing hooks pick up the default after re-running
  `sparkwing pipeline hooks install`.

### Docs

- **docs:** New "Node artifacts" concept page covers producer `Outputs`,
  consumer `Consumes` / `Into`, content-addressed edges, and both
  execution modes. The caching guide drops the cache-hit file-output
  limitation now that a cache hit carries a producer's artifact manifest
  forward.

## [v0.10.0] - 2026-06-14

This release re-versions the runs-store schema 3 → 4 change that first
shipped, under-versioned as a patch, in v0.9.2 and v0.9.3. There is no
functional change over v0.9.3 -- v0.10.0 is the canonical, correctly
versioned home of the break and the consolidated user-facing narrative
for everything that landed since v0.9.1. The v0.9.2 and v0.9.3 sections
below are kept for the audit trail and now carry an erratum.

### Changed

- **store (Breaking):** The runs-store schema moved from version 3 to 4,
  adding a `sparkwing_meta` table that backs throttle stamps and other
  small operational state. The store auto-migrates the database on open,
  so a plain CLI or controller upgrade needs no action. But a module
  that pins an older (schema-3) sparkwing and shares the same state
  database has its pre-commit / pre-push gate refuse the migrated
  database until the pin is bumped. See
  [docs/migrations/v0.10.0.md](docs/migrations/v0.10.0.md#runs-store-schema-3-to-4)
  for the upgrade steps.

### Added

- **cli:** `sparkwing maintenance` runs the controller-free janitorial
  pass over the concurrency tables in the local state database: it reaps
  lease-expired holders, deletes finished and aged waiter rows, and
  bounds the concurrency cache and entries by age and size. Local runs
  trigger the same pass inline (throttled); the command forces a full
  pass now, for cron or to reclaim a database that grew while idle.
  Controllerless boxes previously had no path to this cleanup, so
  finished-run waiter rows and the concurrency tables could grow without
  bound.
- **config:** Pipelines accept a `post_commit:` trigger alongside
  `pre_commit:` and `pre_push:`. `sparkwing pipeline hooks install`
  writes a managed `.git/hooks/post-commit` for any pipeline that
  declares it; the post-commit hook is non-blocking and always exits
  zero, whereas pre-commit and pre-push still abort the git action on
  the first failing pipeline.
- **cli:** `sparkwing version` reports the binary's embedded runs-store
  schema version (`schema_version` in JSON, a `schema:` line in the
  table), so a reader confirms which schema a binary speaks without
  opening a database. The release pipeline gates published assets on it:
  a pre-publish check refuses the release if any asset embeds a
  different schema than the tagged commit.
- **controller:** `sparkwing-controller` prints a build banner at
  startup -- its version, embedded schema version, and build commit --
  and refuses to start against a state database recorded at a newer
  schema than it understands, naming both versions and the remedy.

### Fixed

- **sdk:** Promoting queued waiters into freed concurrency slots now
  deletes and skips any waiter whose run has already finished, keeping
  FIFO order honest so a finished head can no longer wedge the live
  waiters queued behind it.
- **sdk:** Concurrency budgets stay correct under contention across both
  dialects: budget-mutating paths serialize on the key's row (closing a
  Postgres admit-past-capacity race), liveness decisions read the clock
  after the store lock is held (so a contended acquire can't revive an
  already-expired holder), and cancelled or re-acquired waiters drop
  their stale rows so a later release can't promote a phantom holder.

## [v0.9.3] - 2026-06-14

**Erratum:** this release under-versioned the runs-store schema 3 → 4
change as a patch. The break is correctly versioned and documented in
[v0.10.0](#v0100---2026-06-14); see its migration guide for upgrade
steps.

### Fixed

- **sdk:** Promoting queued waiters into freed concurrency slots now
  deletes and skips any waiter whose run has already finished, instead
  of minting a finished run into a holder that the reaper would only
  have to clean up. Skipping rather than stopping at the dead waiter
  keeps FIFO order honest, so a finished head can no longer wedge the
  live waiters queued behind it. Waiters with no runs-table row are
  left untouched: concurrency keys are decoupled from the runs table,
  so a missing row carries no liveness meaning and is reclaimed by the
  stale-waiter sweep.

## [v0.9.2] - 2026-06-14

**Erratum:** this release shipped the runs-store schema 3 → 4 change as a
patch, which under-versions a persisted record-shape break (it warrants a
minor bump pre-1.0). The break is correctly versioned and documented in
[v0.10.0](#v0100---2026-06-14); see its migration guide for upgrade
steps.

### Added

- **config:** Pipelines accept a `post_commit:` trigger alongside
  `pre_commit:` and `pre_push:`. `sparkwing pipeline hooks install` writes
  a managed `.git/hooks/post-commit` for any pipeline that declares it.
  The post-commit hook is non-blocking: the commit has already landed, so
  it runs its pipelines, tolerates failures, and always exits zero,
  whereas pre-commit and pre-push still abort the git action on the first
  failing pipeline.
- **cli:** `sparkwing version` reports the binary's embedded runs-store
  schema version (`schema_version` in JSON, a `schema:` line in the
  table). A reader confirms which schema a binary speaks without opening
  a database, and the release pipeline gates published assets on it: a
  pre-publish check rebuilds the schema reference from the tagged commit
  and refuses the release if any asset embeds a different schema, so a
  version string always implies one schema across every install path.
- **controller:** `sparkwing-controller` prints a build banner at
  startup -- its version, the runs-store schema version it embeds, and
  its build commit -- and refuses to start against a state database
  recorded at a newer schema than it understands, naming both versions
  and the remedy. A schema skew is now a one-line diagnosis in the logs
  instead of an opaque restart loop.
- **sdk:** The store verifies its concurrency invariants (live cost
  within effective capacity, holder and waiter shape, no participant
  both holding and waiting) at the end of every mutating transaction.
  Under `go test` a violation fails the operation; in production it is
  logged loudly. A seeded randomized property suite drives
  acquire/release/heartbeat/promote/cancel sequences -- sequential and
  concurrent -- against a real store to keep those invariants honest.
- **sdk:** `NewConcurrencyGroup` rejects an empty group name (all
  unnamed groups would silently share one budget) and unknown `Scope`
  / `OnLimit` values at construction, so a misspelled policy fails at
  the author's call site instead of silently coordinating with the
  backend default.
- **sdk:** Node ids are validated at plan time: slashes remain valid
  as spawn hierarchy separators (`parent/child`), but traversal
  references, empty segments, backslashes, and control characters
  panic at the `Job(...)` call site.

### Fixed

- **controller:** The log storage backends (filesystem and S3) reject
  run and node IDs containing path traversal or control characters at
  the boundary, so an ID arriving over HTTP can never escape the log
  root on disk or corrupt an object-key listing.
- **cache:** The registry proxy's cache key length-prefixes its
  (registry, path) input so a registry name embedding a slash cannot
  collide with another registry's path and serve its cached response.
  Existing entries keyed by the older form miss once and re-fetch.

- **sdk:** On Postgres (multi-writer modes), every budget-mutating
  concurrency path -- promotion on release, the reconcile sweep,
  waiter cancellation, heartbeat lease extension, and the first-ever
  acquire of a key -- now serializes on the key's entries row the way
  admission always did. Previously a promotion could race a concurrent
  acquire's grant and admit past the effective capacity. SQLite is
  unaffected (single-writer by construction).
- **sdk:** The lazy local-run orphan reconciler moved into the store
  and shares the exact cascade the controller-side stale-run reaper
  uses, so the two sweeps can't drift; it also now goes through the
  store's placeholder rewriting, so it works against Postgres instead
  of erroring on `?` parameters.
- **sdk:** Concurrency liveness decisions read the clock after the
  store transaction holds its lock. A timestamp captured before a
  contended `BEGIN` went stale while waiting, so an acquire or
  heartbeat could treat an already-expired holder as live and revive it
  after its budget had been reassigned -- two live holders on one
  budget.
- **sdk:** A queued participant that re-acquires its slot after the
  budget freed (crash or redeliver) no longer leaves its stale waiter
  row parked; the row could later be promoted on top of the
  participant's own live holder and abort an unrelated release.
- **sdk:** Promoting a waiter whose holder id still owns a
  lease-expired (not yet reaped) row reclaims the row, the same way
  admission does, instead of aborting the release transaction on the
  `UNIQUE` constraint.
- **sdk:** The operator state view (`cluster concurrency`, the state
  endpoint) derives used cost and effective capacity through the same
  accounting rules admission enforces, so a live holder predating
  declared-capacity tracking can no longer make the display claim more
  headroom than admission actually allows.
- **controller:** Holder lists returned by the resolve-waiter and
  force-release endpoints now carry `cost`, matching the acquire and
  state endpoints, and the client surfaces it.

## [v0.9.1] - 2026-06-10
### Added

- **cli:** `sparkwing commands -o markdown` renders the entire CLI
  surface (every command, flag, and argument) as a reference page,
  generating `docs/cli-reference.md`. The CLI reference is now derived
  from the command registry rather than hand-maintained, so it can't
  drift from the binary; a pre-push gate fails if the committed file is
  stale.
- **docs:** `docs/config-reference.md` is generated from the
  `sparkwing.yaml` schema structs, so the complete config field
  reference (top-level keys, pipeline-entry fields, trigger fields) is
  derived from the parser's own structs and can't claim a field that
  doesn't exist. A pre-push gate fails if it drifts.
- **docs:** `docs/sdk-reference.md` is generated from the `sparkwing`
  package via go/doc -- every exported function, type, method, and
  constant with its signature and synopsis. The SDK signature reference
  is now derived from source (offline-loadable, the same data
  pkg.go.dev shows) instead of hand-typed in `sdk.md`; a pre-push gate
  fails if it drifts.
- **docs:** `docs/api-reference.md` is generated from the controller and
  logs-service route registrations -- every method, path, and required
  scope. The HTTP API reference is now derived from the routing code, so
  it can't document endpoints that don't exist; a pre-push gate fails if
  it drifts.

### Fixed

- **docs:** the `observability.md` failure-reason table now matches the
  real `failure_reason` set: dropped the non-existent `pod_error`, added
  `verify`, `runner_lease_expired`, and `logs_auth`. A gate keeps the
  documented set complete against the `pkg/store` constants.
- **sdk:** A concurrency heartbeat that arrives after the lease has
  already expired no longer revives the holder. Admission may have
  freed and reassigned that budget once the lease lapsed, so reviving
  could put two live holders on a capacity-1 group; the stale heartbeat
  now fails instead.
- **sdk:** Re-acquiring a superseded-but-unreaped concurrency holder
  under the same holder id (deterministic `runID/nodeID`, reachable on
  crash or redeliver) no longer crashes with a `UNIQUE constraint`
  violation. The grant reclaims the row cleanly.
- **sdk:** A `Cache` node whose in-flight dedupe leader was *skipped*
  no longer stamps its coalesced followers `Success` with empty output.
  Followers now inherit the leader's actual node outcome, so a skipped
  or failed leader never produces bogus green followers.
- **sdk:** A parked low-capacity concurrency waiter no longer drags the
  effective capacity below the already-admitted holders, and no longer
  blocks a FIFO-head waiter that fits under its own declared capacity.
  Effective capacity is the minimum over admitted holders plus the
  arrival, not over non-admitted parked waiters.
- **sdk:** A `Concurrency` member whose cost exceeds its group capacity
  is now rejected at Plan time (with a store-side backstop) instead of
  queuing forever -- it could never be admitted.
- **sdk:** Cancelling a run whose node is queued or coalesced on a
  concurrency group now drops the waiter row, so a later release can no
  longer promote the cancelled node into a phantom holder that pins the
  budget until reaping. The plan-level wait path is fixed the same way.
- **sdk:** Scope-qualified concurrency keys are now scheme-tagged
  (`g:` / `r:` / `b:`) so a `Global` group whose name contains `@`
  cannot collide with a `Box` or `Run` group of the bare name on that
  host. `sparkwing cluster concurrency` labels the scope from the tag
  rather than inferring it from the presence of `@`.
- **controller:** `--no-cache` (bypass-read) now crosses the HTTP wire,
  so hosted and cluster runs that ask for fresh execution no longer
  silently replay a cached result.
- **controller:** A queued acquire's position, queue length, and
  current holders now cross the HTTP wire, so the dashboard renders the
  real queue depth instead of "0 ahead, held by unknown".
- **sdk:** The superseded-holder reclaim also covers waiter promotion,
  not just admission, so promoting a waiter onto a holder id that still
  carries a superseded row no longer aborts the release transaction.
- **sdk:** Re-acquiring an *expired* concurrency holder no longer
  revives it -- the acquire-path twin of the heartbeat-liveness guard.
- **sdk:** Budget arithmetic no longer overflows: a very large declared
  cost can't wrap the used-plus-cost sum negative and over-admit.
- **sdk:** A live holder carrying no declared capacity (a migration
  backfill or a promoted legacy waiter) no longer vanishes from the
  effective-capacity floor and over-admits; promotion never mints a
  zero-capacity holder.
- **sdk:** Cancelling a queued node also reclaims a holder it was
  promoted into during the cancel race, so the freed slot isn't pinned
  until the lease reaps.
- **sdk:** A fresh `Queue` arrival no longer barges a waiter already
  parked on the key when budget frees outside the atomic release and
  promote (e.g. a holder's lease lapsing before the reaper runs); strict
  FIFO is preserved.
- **sdk:** Scope-qualified keys also length-prefix the run/box
  qualifier, so a custom run id or box id containing the separator can't
  fold two distinct identities onto one key.
- **sdk:** `--no-cache` is honored end to end for memoized nodes: a
  coalesced follower no longer replays the leader's result through the
  resolve path, and a `--no-cache` node runs fresh instead of coalescing
  onto an in-flight leader.
- **sdk:** A coalesced follower of a *failed* leader now inherits the
  leader's categorized `failure_reason` instead of recording it as
  uncategorized.
- **sdk:** `CancelOthers` now grants the preempting node the slot
  immediately and reserves the freed budget, so a later arrival (or a
  second `CancelOthers`) can no longer steal the slot the canceller
  evicted others to take. It is documented as best-effort preemption:
  the canceller may briefly overlap a still-draining victim, so use
  `Queue` when you need strict mutual exclusion with no overlap.

## [v0.9.0] - 2026-06-09

> **Erratum -- runs-store schema skew in the published binaries.** The binary
> assets attached to this release were built from pre-schema-3 code and embed
> runs-store **schema 2**, while a build from the `v0.9.0` module tag
> (`go install github.com/sparkwing-dev/sparkwing/cmd/sparkwing-controller@v0.9.0`)
> expects and writes **schema 3**. Do not point both artifacts at one runs
> store: the module build forward-migrates the store from schema 2 to schema 3
> on its first write, after which the schema-2 release-asset controller can no
> longer read it and crash-loops on a blank dashboard. Pin a shared store to a
> single install path -- the module build, which is correct at schema 3 -- until
> a corrected asset is republished. A store written only by the release assets,
> or only by a module build, is unaffected.

### Added

- **cli:** `-C` / `--sw-cd <dir>` now works on the discovery verbs
  (`sparkwing pipeline list` / `describe` / `discover`), matching
  `run` and `pipeline new`, so you can inspect another repo's
  pipelines without changing directory. `pipeline new` and
  `pipeline templates` also print a template's prerequisite (e.g. a
  "run from the repo root" note) after scaffolding, so setup
  requirements are visible where you scaffold.
- **cli:** `sparkwing cluster concurrency` shows cost-summed budget
  (used / available / effective capacity), the group scope, and
  per-holder / per-waiter cost. `sparkwing pipeline explain` renders the
  split `Cache(ttl=...)` and `Concurrency(group=... cap=... cost=...
  scope=...)` facts.
- **controller:** the concurrency HTTP backend reaches parity with the
  in-process engine -- `cost` on acquire plus `resolve`,
  `cancel-waiter`, and `force-release` endpoints -- so cost-weighted
  admission, scope, and most-restrictive capacity hold under a hosted
  controller, not only in-process or Postgres-direct.

### Changed

- **sdk (Breaking):** `Cache` is now content-addressed memoization only:
  `Cache(key CacheKeyFn, opts ...CacheOption)` with `TTL(d)`, replacing
  `Cache(CacheOptions{Namespace, ContentHash, CacheTTL, ...})`. It is
  keyed on content alone, so two nodes with the same key share a result
  regardless of group or run, and in-flight dedupe of identical content
  is automatic (no policy to set). `DefaultCacheTTL` 7d, `MaxCacheTTL`
  35d. See
  [migration](docs/migrations/v0.9.0.md#cache-content-key-plus-options-no-more-cacheoptions).
- **sdk (Breaking):** Concurrency is a new, independent primitive:
  `NewConcurrencyGroup(name, ConcurrencyLimit{Capacity, Scope, OnLimit,
  QueueTimeout, CancelTimeout})` plus `(*JobNode).Concurrency(group,
  cost...)`. The scheduling fields that overloaded `CacheOptions`
  (`Max`, `OnLimit`, the timeouts) move here. Admission is cost-weighted
  and summed across the group's `Scope` (`ScopeRun`/`ScopeBox`/
  `ScopeGlobal`); capacity skew across pipeline versions resolves
  most-restrictive-wins. See
  [migration](docs/migrations/v0.9.0.md#concurrency-a-named-group-not-a-cache-namespace).
- **sdk (Breaking):** `OnLimit: Coalesce` and the `OnLimitPolicy` type
  are removed. In-flight dedupe is folded into `Cache` and keyed on
  content rather than a group. See
  [migration](docs/migrations/v0.9.0.md#onlimit-coalesce-is-gone).
- **sdk (Breaking):** `Plan.Cache(CacheOptions{...})` is replaced by
  `Plan.Concurrency(group)` for whole-run coordination; a plan never
  memoizes. See
  [migration](docs/migrations/v0.9.0.md#plancache-becomes-planconcurrency).

### Fixed

- **cli:** the `run_start` event reported its working directory as
  `.sparkwing/` (the pipeline binary's own cwd) instead of the repo
  root that steps actually execute from. It now reports the repo root,
  so the dashboard and run metadata point at the directory where
  relative paths and `go ./...` resolve.
- **docs:** a broad accuracy pass over the bundled docs and CLI help,
  correcting divergences verified against the binary and SDK source:
  run flags (`--from` → `--sw-ref`; `--mode` / `--workers` /
  `--no-update` → `--sw-*`); the project config filename
  (`pipelines.yaml` → `sparkwing.yaml`); removal of documented-but-
  nonexistent config keys that hard-errored on load (`runs_on`,
  `dispatch`, `pull_request`, `branches_ignore` / `paths_ignore`);
  the local store path (`state.db`) and per-run log
  location (`~/.sparkwing/runs/`); flag-only `cluster tokens` verbs
  (`--prefix`); and a rewrite of `scheduling.md` to the shipped label
  model (`requires:` plus `.Requires()` / `.Prefers()` /
  `.WhenRunner()`).

## [v0.8.1] - 2026-06-06
### Added

- **cli:** `sparkwing pipeline new` gained `-C` / `--sw-cd <dir>` to
  scaffold into a repo other than the current directory (matching
  `sparkwing run`), and its `--help` now leads with a pointer to
  `sparkwing pipeline templates` so the registry starters are
  discoverable from where you scaffold.
- **config:** pipeline entries in `.sparkwing/sparkwing.yaml` accept
  `hidden: true` to omit a pipeline from default `pipeline list`
  output; it stays invocable by exact name and appears under
  `pipeline list --all`.

### Fixed

- **controller:** a node's Verify-stage failure is attributed
  correctly when the node runs on a remote/cluster runner. The failing
  stage is recovered from the persisted failure reason instead of the
  in-process error type, so a failure-aware `OnFailure(ctx, Failure)`
  branches on `StageVerify` vs `StageAction` identically in-process and
  on the controller.
- **cli:** `sparkwing runs approvals` is usable again. The bare verb
  and its flags (`--run`, `-o json`) were parsed as unknown
  subcommands, so the documented ways to find a pending gate all
  errored; `approve` / `deny` were missing from `--help`; and the
  shipped examples referenced a non-existent `sparkwing approve`. The
  verb now defaults to `list`, dispatches `approve` / `deny` directly,
  lists them in help, and the examples use the real
  `sparkwing runs approvals approve|deny` path.
- **cli:** `sparkwing pipeline new --hidden` wrote a `hidden:` key the
  config parser rejected, leaving an unparseable `sparkwing.yaml`.
  `hidden` is now a recognized field and `pipeline list` / `--all`
  honor it.
- **cli:** a `sparkwing` binary built from a clean local checkout at a
  commit after the last release stamped its own pseudo-version
  (`vX.Y.Z-0.<ts>-<hash>`) into freshly-scaffolded `.sparkwing/go.mod`,
  so `go mod tidy` failed with "unknown revision". Pseudo-version
  detection now recognizes that form and falls back to the latest
  released SDK version, so dev-built CLIs produce resolvable scaffolds.
- **cli/docs:** the project registry file was referred to as
  `pipelines.yaml` in scaffolder output, `pipeline new --help`,
  `pipeline explain`, and several docs; the actual file is
  `sparkwing.yaml` (the legacy name is a hard error). All current
  references now name it correctly.
- **cli:** `sparkwing info` now notes when it resolved a `.sparkwing/`
  by walking up from the current directory (rather than finding one in
  it) and points at `-C`, so running in a fresh directory no longer
  silently reports an ancestor repo's pipelines as your own.
- **cli:** the `minimal` scaffold no longer emits literal `TODO:`
  placeholders (they tripped repos' own no-TODO lints); it uses neutral
  "replace this" wording.
- **sdk:** `services.WithServices` publishes a service's `Port` to
  `127.0.0.1:<Port>` instead of relying on `--network host`, so
  integration-test containers are reachable from the host test process
  on Docker Desktop (macOS/Windows), not only on Linux. A `Service`
  with `Port` unset still uses host networking.
- **docs:** the SDK reference and getting-started guide were refreshed
  to match the shipped API. Removed the deleted `JobFn`; corrected the
  Workable shape to `Work(w *Work) (*WorkStep, error)` with
  `sparkwing.Step(...)`; fixed the `JobFanOut` callback signature to
  `func(T) (string, any)`; replaced the bogus pipeline-entry fields
  (`tags`/`env`/`secrets`/`runs_on`) with the real schema
  (`entrypoint`/`guards`/`args`/`profile`/`requires`); documented the
  `Git` struct fields, `Retry`/`RetryBackoff`/`RetryAuto` semantics,
  `ExecResult` fields, `WithServices`, the `ContinueOnError` vs
  `Optional` distinction and the `Failure` struct; clarified that the
  file helpers (`WorkDir`/`Path`/`WriteFile`) are package-level
  functions taking no `ctx`; noted that `Bash` runs with no implicit
  `set -euo pipefail`; and marked the `Verify` postcondition proposal
  implemented.

## [v0.8.0] - 2026-06-03
### Added

- **sdk:** `Job.Verify(fn)` -- a postcondition checked after a node's
  action succeeds. The command exited 0, but if the check returns an
  error the node fails at the verify stage (eligible for `Retry`, routed
  to `OnFailure`), so "the command succeeded but the result is bad" is a
  first-class node outcome rather than a hidden state. Runs once per
  attempt; a cache hit skips the action and the check together. Also on
  `JobGroup` (applied to every member).
- **sdk:** `OnFailure` now also accepts a failure-aware recovery,
  `func(ctx context.Context, f sparkwing.Failure) error`. `Failure`
  carries `Stage` (`StageAction` / `StageVerify`) and the underlying
  error, so recovery can branch: converge forward on an action failure,
  roll back on a verify failure. The verify stage is recorded on the
  node's failure reason for the run ledger.
- **controller:** concurrency gate waits are now observable. A node
  queued behind a full `OnLimit: Queue` namespace previously blocked
  with no external signal. The `concurrency_wait` event now carries the
  waiter's `position` (0 == next in line), the `queue_length`, and the
  current `holders`; `GET /api/v1/concurrency/{key}/state` now reports
  each waiter's `position`. Position and holders are computed in the
  acquire transaction, so they're consistent with the queue the wait
  joined. A queued node's `status_detail` is set to a summary
  ("queued in <ns>: N ahead, held by <run>/<node>") so the dashboard
  and `sparkwing runs status` show the wait inline instead of a
  featureless spinner, and is cleared on promotion. The same summary is
  emitted as a `concurrency_wait` line into the run log stream (from the
  dispatcher, since the node hasn't started its runner yet), so it's
  visible while following live logs and in `runs logs`. The position is
  refreshed on each poll against the fully-committed queue, so it tracks
  downward as the queue drains and self-corrects the brief insert-time
  approximation possible when waiters arrive simultaneously. No schema
  change.
- **cli:** `sparkwing cluster concurrency --namespace <ns> --profile <p>`
  renders a namespace's current holders and its queue (each waiter with
  its position), so an operator can tell a wedged node from one waiting
  its turn. `-o json` for scripting.

### Fixed

- **`pipeline trigger` now requires a GitHub repository.** When the
  CLI was invoked from a non-git cwd, it silently sent an empty
  `GITHUB_REPOSITORY` to the controller. The warm-runner then fell
  into its baked-binary fallback (`$SPARKWING_BAKED_BINARY`), which
  in production pointed at a binary that doesn't ship in the runner
  image, producing a confusing `fork/exec /usr/local/bin/sparkwing:
  no such file` failure 80ms in. `pipeline trigger` now errors
  before sending if cwd has no github remote, with an actionable
  message ("Run from inside a checkout of a github repo, or pass
  --repo OWNER/NAME explicitly").

## [v0.7.1] - 2026-05-31
### Fixed

- **docs:** `_sidebar.json` now excludes `proposals/` and `migrations/`
  alongside the existing `design/` exclusion. Downstream sites that
  walk a release tag's docs (e.g. sparkwing.dev) failed prerendering
  when a new proposal landed without being categorized; both
  directories carry per-document content that doesn't belong in the
  user-docs sidebar, so they're flat-excluded instead.

## [v0.7.0] - 2026-05-31
### Changed

- **box-slot semaphore is now opt-in.** Default `SPARKWING_BOX_SLOTS`
  changed from `max(1, NumCPU/workersPerRun)` (resolving to 1) to
  `0` (disabled). Most pipelines aren't CPU-pegged -- they're I/O on
  Docker pulls, network, registry pushes -- so the conservative
  default surprised users with "waiting for box slot (1 active, max
  1)" whenever any other sparkwing process was running. Users on
  small boxes who launch concurrent CPU-saturating pipelines can
  re-enable explicitly: `export SPARKWING_BOX_SLOTS=2` (or any N).
  The primitive remains the right answer for explicit host
  throttling -- it's just no longer always-on.

## [v0.6.3] - 2026-05-31

### Added

- **`pre-push` now runs a repo-wide gofmt check.** The existing
  golangci-lint step runs in `.sparkwing/` only, so a struct-alignment
  fix at the top of the tree slipped past pre-push and got caught
  later by `sparkwing run lint`. Both gates now reject the same
  unformatted file.
- **Dashboard nav now shows the CLI version pill.** A small monospace
  pill renders next to the "sparkwing" logo (e.g. `v0.6.2`), reading
  the value the serving binary injects via the SPA template. Operators
  can see what build they're connected to without opening dev tools.
  Source builds without an `-ldflags` version stamp fall back to the
  Go build-info pseudo-version so the pill is still informative.

### Changed

- **install.sh installs only `sparkwing`.** Previous revisions also
  dropped `sparkwing-local-ws` and `sparkwing-web` into `~/.local/bin`;
  both are now removed on next install (sweep is silent if absent).
  Cluster-side binaries (`sparkwing-cache`, `-controller`, `-logs`,
  `-runner`, `-web`) run only as pods and are published as Docker
  images; install.sh sweeps them from `$DEST` and from `$GOPATH/bin`
  on every run so a stale `go install ./cmd/sparkwing-<x>` artifact
  cannot keep shadowing the laptop CLI on PATH. `sparkwing-local-ws`
  is superseded by `sparkwing dashboard start` and is no longer
  published as a release binary.

### Removed

- **`cmd/sparkwing-local-ws/`** is gone. Its job (long-lived local
  dashboard server) is fully owned by `sparkwing dashboard start`,
  which spawns a detached supervisor under the same `pkg/localws`
  code path. The dev scripts (`bin/dev-start.sh` /
  `bin/dev-stop.sh` / `bin/dev-restart.sh`) now drive the supervisor
  via `sparkwing dashboard {start,kill}` instead of forking the
  retired binary directly.

### Fixed

- **`sparkwing pipeline new` scaffold now produces a working project
  out of the box.** Three bugs converged to break the first-run
  experience: (a) the scaffold wrote `.sparkwing/pipelines.yaml`
  while every other CLI command reads `.sparkwing/sparkwing.yaml`,
  so `pipeline list`, `pipeline describe`, and `pipeline hooks
  install` all reported "no .sparkwing/sparkwing.yaml found"; (b)
  the generated `go.mod` pinned a non-existent fallback SDK version,
  so `go mod tidy` failed and the compile cycle never recovered;
  (c) the generated `jobs/*.go` mixed `sw.` and `sparkwing.` aliases
  in the same file, so the file didn't compile. All three are fixed
  and a fresh `sparkwing pipeline new --name X` → `git commit` (with
  a pre_commit trigger and `sparkwing pipeline hooks install`)
  now scaffolds + builds + dispatches end-to-end.
- **Postgres state from a laptop + `RunAndAwait` now works
  end-to-end.** The parent's local trigger dispatcher forwards its
  active profile (`--profile <name>`) to the child `handle-trigger
  --local`, which resolves the same profile and opens the same state
  backend the parent used. Previously the child defaulted to local
  sqlite and could not find the trigger row the parent had enqueued
  in postgres, producing a 30s timeout with a misleading error.
- **Controller profiles no longer need `controller: <self>` on every
  surface.** When `InheritControllerDefaults` fills URL+Token onto a
  surface from the profile's top-level `controller:` block, it now
  also fills the surface's `controller:` (profile-name reference) so
  the lookup callback can resolve it. A profile that just declares
  `controller: { url, token }` + `state/cache/logs/secrets: { type:
  controller }` is now a complete, working spec.
- **dashboard:** `sparkwing dashboard start` now fails fast with a clear
  error when the bind address is already in use, naming the holding
  process (e.g. `address 127.0.0.1:4343 already in use by
  sparkwing-local-ws (pid 37326)`). Previously the supervisor would
  silently crash, the PID file never got written, and `sparkwing
  dashboard kill` would then report "not running" even though something
  was visibly serving the port. `start` also treats listener-not-ready
  and missing-PID-file as hard errors, surfacing the tail of
  `dashboard.log` instead of printing a success banner with a dead PID.
- **dashboard:** `sparkwing dashboard start` now restarts an existing
  supervisor it owns instead of refusing. After upgrading the CLI,
  re-running `sparkwing dashboard start` is enough to pick up the new
  embedded SPA bundle -- no manual `kill` step needed. Foreign
  processes on the bind address are still left alone (the error path).
- **flake:** `TestApproval_ApprovedFlowsToSuccess` previously silently
  swallowed errors from the test resolver goroutine (`store.Open`,
  `ListPendingApprovals`, `ResolveApproval`), so any transient failure
  there surfaced as a misleading `status = "failed"` from the
  orchestrator's downstream timeout. The resolver now reports its own
  errors via `t.Errorf`, the approval window was widened from 5s to
  30s, and the test joins the resolver goroutine before returning.
  Verified clean under `go test -race -count=100`.

## [v0.6.2] - 2026-05-30

### Fixed

- **dashboard:** `sparkwing dashboard start` no longer ships a stale
  embedded dashboard bundle. Two binaries embed it via
  `//go:embed all:next-out`: `sparkwing` (powers `dashboard start`)
  and `sparkwing-web` (cluster pod). The release workflow previously
  rebuilt the bundle only for `sparkwing-web`, so released
  `sparkwing` binaries used whatever stale `internal/web/next-out/`
  was on the runner cache (committed `.gitkeep` only). `bin/install.sh`
  also skipped the rebuild. Both paths now call `bin/build-web.sh`,
  so every install + every released artifact ships the current
  dashboard SPA. Set `SKIP_WEB_BUILD=1` on `install.sh` to bypass
  during Go-only iteration.

## [v0.6.1] - 2026-05-30

### Fixed

- **orchestrator:** `BindPipelinesFromYAML` now runs before
  `parseTypedFlags`, so YAML-only pipeline names (multiple pipelines
  sharing one entrypoint via `RegisterEntrypoint`) resolve correctly.
  Previously the typed-flag parser called `sparkwing.Lookup` and got
  "unknown pipeline" because the bind happened after.

## [v0.6.0] - 2026-05-29

### Added

- **sdk:** `RegisterEntrypoint[T](name, factory)` declares a Go work
  unit by its entrypoint type name. Combined with the new
  `BindPipelinesFromYAML(cfg)` bootstrap, one entrypoint can back
  many pipelines -- each pipeline in YAML names the entrypoint and
  supplies its own policy.
- **sdk:** Typed-args system via `sparkwing.WithArgs[T]` + optional
  `Schema()` method (`Required` / `RequiredWhen(predicate)` /
  `Default` / `Computed(fn)` / `OneOf` / `Min` / `Max` / `Range` /
  `Positive` / `Custom(fn)` / group rules). Predicate vocab:
  `ArgEq`/`ArgNeq`/`ArgIn`/`ArgSet`/`ArgUnset` plus `And`/`Or`/`Not`
  and `Local`/`Remote`/`Profile(name)`/`Always`. `sparkwing.Arg[T]`
  reads a resolved arg by CLI flag name.
- **cli:** `sparkwing run <pipeline> --help` lists every transitive
  `WithArgs[T]` flag declared by jobs the pipeline registers,
  annotated with `[from job <id>]` so authors can trace each flag
  back to its owning job.
- **config:** Top-level `defaults:` block (`profile`, `args`,
  `guards`, `requires`) supplies per-pipeline fallbacks. `profile`,
  `guards`, `requires` replace wholesale at pipeline level when
  declared; `args` merges per-key (pipeline wins per-key).
- **config:** Project YAML grows a `profiles:` map (same shape as
  `~/.config/sparkwing/profiles.yaml`). A pipeline references one
  via `pipeline.profile: NAME`; `defaults.profile: NAME` provides
  the project-wide default.
- **config:** Pipeline `guards:` block. Token vocabulary normalized
  to `namespace:rest`: `profile:local`, `profile:controller`,
  `profile:name=NAME`, `git:branch=NAME`, `git:branch=default`,
  `arg:FLAG=VALUE`. `require:` is AND-composed; `reject:` is
  OR-composed and fires first.
- **config:** Pipeline `requires: [labels]` lists runner labels
  every job in the pipeline must satisfy (unioned with each job's
  own `Job.Requires(...)` declarations). The reserved `local` label
  pins execution to in-process (same effect as `--sw-local-only`).
- **config:** Backend specs gained `token_env: VAR` for sourcing
  the controller token from an env var instead of inlining it --
  intended for checked-in project YAML where inline tokens are a
  non-starter.
- **config:** Backend spec gained `type: none` (valid only on the
  `secrets` surface). Profile validator requires every profile to
  declare all four surfaces (`secrets`, `state`, `cache`, `logs`);
  pipelines with no secrets-resolving jobs use `type: none` to
  satisfy the requirement explicitly.
- **config:** Per-surface controller fields (`url`/`token`/
  `token_env`) inherit from the profile's top-level `controller:`
  block when omitted. A profile that routes every surface through
  the same controller writes the URL/token once instead of five
  times.
- **sdk:** `Git.DefaultBranch` populated from origin's HEAD
  symref. Feeds `git:branch=default` guard evaluation.

### Changed (Breaking)

- **config:** Source/backend specs unified. The standalone `sources`
  registry and `sources.Source` type are gone; secrets are a fourth
  `backends.Surfaces` field alongside `state`/`cache`/`logs`. Valid
  secrets `type:` values: `controller`, `filesystem`, `env`, `none`.
- **config:** Pipeline `defaults:` field renamed to `args:`. Same
  semantics, clearer name.
- **config:** Pipeline `dispatch:` block removed wholesale. Its
  former contents (`source`, `requires_approval`, `protected`,
  `backend`, `runners`) are gone or relocated: source resolution
  now flows through the active profile's `secrets:` surface;
  approval is a job-level concern (declare an approval job); the
  "protected" gate is expressed via `guards.require: [git:branch=default]`;
  per-pipeline backend overrides are gone (use `--profile` to swap
  the bundle); runner allowlists moved to job-level
  `Job.Requires(...)` labels + pipeline-level `requires:`.
- **config:** Project YAML's `runners:` and `sources:` registries
  removed. Job-level `Job.Requires(...)` labels replace runner
  registration; inline `secrets:` surface on the active profile
  replaces named source registries.
- **profile:** Profile resolution is `--profile NAME` only -- no
  laptop fallback, no `default:` field in profiles.yaml, no
  `sparkwing.yaml profile:` hint, no env-detect rules. When no
  profile is selected, the orchestrator runs against a sqlite-only
  test/dev shape; remote-controller verbs (`pipeline trigger`,
  `users`, `gc`, `approvals`, `debug replay`) refuse to run without
  a profile that has a `controller:` block.
- **profile:** `--profile X` wins wholesale -- the named profile's
  full backend bundle applies; per-pipeline `profile:` selections
  are discarded. Keeps state/cache/logs/secrets coherent so a run
  can't have its logs in one place and its state in another.
- **config:** Guard token grammar rewritten to `namespace:rest`.
  `profile-local` -> `profile:local`, `profile-controller` ->
  `profile:controller`, `profile-name:NAME` -> `profile:name=NAME`,
  `git-branch:NAME` -> `git:branch=NAME`, `git-branch:default` ->
  `git:branch=default`. Old syntax errors at parse time.
- **config:** Pipeline-level trims: `tags`, `hidden`, `on.manual`,
  `on.deploy`, `description` rationalized; `dispatch.runners`
  allowlist gone (use `requires:`); `dispatch.approvals` enum gone
  (approval is a job).
- **config:** Profile `controller:` is a nested block with `url:` +
  `token:` (was two flat fields).
- **config:** Profile fields removed: `gitcache`, `cost_per_runner_hour`,
  `auto_allow`, `default_runner`, `log_store`, `artifact_store`,
  `detect`. The CLI discovers the cache pod via the controller's
  `GET /api/v1/services` endpoint; the other fields were unused or
  footguns.
- **sdk:** `PipelineConfig[T]`, `ConfigProvider`,
  `ResolvePipelineConfig`, `InspectPipelineConfig`, `ConfigField`,
  `WithPipelineConfig` removed. Use `WithArgs[T]` with YAML `args:`
  for per-deployment overrides, or hardcode constants in Go.
- **sdk:** `OnTarget(...)` on Job/WorkStep/JobGroup removed.
  `sparkwing.Target(ctx)` removed. Split multi-target pipelines into
  one pipeline per target shape.
- **cli:** `--target` removed. Pipeline name is the deployment
  selector.
- **controller:** New `--cache-pod-url` flag (or `CACHE_POD_URL`
  env var) on `sparkwing-controller`. When set, the controller
  announces the URL via `GET /api/v1/services` so operator CLIs
  can discover it.

### Fixed

- **release:** `prepare-changelog` and `bump-self-replace` no longer
  race on `git commit`. They previously ran in parallel and both did
  `git add <file>` + `git commit -m ...` without path scoping, so
  whichever committed second found "nothing to commit." Now
  `bump-self-replace` is serialized after `prepare-changelog`.
- **sparks:** The resolver no longer errors when a `go.work` is in
  scope. The overlay's `.resolved.sum` write is skipped (with a
  single-line warning) instead of failing, matching the existing
  workspace-mode tolerance in `internal/bincache`.

### Docs

- **docs:** v0.6.0 migration guide at `docs/migrations/v0.6.0.md`
  walks the entrypoint-vs-pipeline split, the unified backend
  model, the new `defaults:` and `profiles:` blocks, the
  `namespace:rest` guard grammar, and the `--profile`-wholesale
  resolution.

## [v0.5.1] - 2026-05-28
### Changed

- **release:** The `release` pipeline now composes the `PreCommit`
  and `PrePush` job types directly into its plan as `gate-pre-commit`
  and `gate-pre-push` nodes, gating every mutating step on their
  success. Previously a release tag pushed via `sparkwing run release`
  skipped both pipelines entirely, so lint / em-dash / race / vuln
  regressions catchable by an everyday push could ship past the
  release path. The gates run in parallel after `check-clean-tree`
  and block `prepare-changelog` + `bump-self-replace` + `push-tag`
  -- if either fails, no commit lands. See
  `docs/proposals/release-pipeline-gates.md` for the DAG, the
  alternatives considered (subprocess, `RunAndAwait`), and the
  general lesson on local-composition vs remote-dispatch primitives.
  Wall-clock cost: about 35 seconds added per release.

## [v0.5.0] - 2026-05-28
### Added

- **sdk:** `CacheOptions.QueueTimeout` for queue-shaped concurrency.
  When set, a queued arrival under `OnLimit: Queue` that doesn't get a
  slot within the duration fails cleanly with `failure_reason:
  queue_timeout` instead of waiting indefinitely. Zero (the default)
  preserves the wait-forever behavior.
- **cli:** `sparkwing pipeline trigger <name> --profile <p>` submits a
  trigger to the named profile's controller and tails the remote run by
  default; `--detach` for fire-and-forget. Replaces `sparkwing run --on`
  for remote dispatch. `sparkwing run` now exclusively means "execute
  here."
- **cli:** `sparkwing profile` prints the resolved profile and the
  resolution chain (flag, project hint, default) without running
  anything.
- **config:** Per-profile `detect:` block in `profiles.yaml` for
  environment auto-selection. Replaces the `environments:` block in
  `backends.yaml`. `gha` and `kubernetes` ship as built-in profiles
  that detect their respective env vars.
- **config:** Per-profile `mirror_local:` flag (default `true`) controls
  whether local execution against a remote profile also writes to local
  SQLite for offline post-hoc viewing.

### Changed

- **cli:** The `run_summary` headline now leads with the
  root-cause node -- the one that actually errored -- and a one-line
  error tail, then reports cascaded cancellations separately
  ("N nodes cancelled by the failure"). The node tally splits
  `cancelled` (an upstream-failure cascade) from `skipped` (a SkipIf /
  filter decision) instead of lumping both, so a single broken leaf no
  longer reads as a wall of failures.
- **orchestrator (Breaking):** A node that spawns a child pipeline via
  `RunAndAwait` now emits structured `child_run_start` and
  `child_run_finish` events into the parent's stream, replacing the
  prior single `pipeline_await_spawned` audit event. `child_run_finish`
  carries the child's `run_id`, terminal `status`
  (success/failed/cancelled/timeout), and `duration_ms`, so the parent
  links to the child without inlining its output. Read the child's own
  logs with `sparkwing runs logs --run <child_id>` or
  `sparkwing runs logs --run <parent> --tree`. See
  [migration guide](docs/migrations/v0.5.0.md#audit-stream-events-for-spawned-children).
- **config (Breaking):** Project YAML collapses to a single
  `.sparkwing/sparkwing.yaml` file. See
  [migration guide](docs/migrations/v0.5.0.md#single-sparkwingsparkwingyaml-per-repo).
  The separate `pipelines.yaml`, `backends.yaml`, `runners.yaml`,
  `sources.yaml`, and `sparks.yaml` files are no longer read; sparkwing
  errors at startup if any of them exist in a `.sparkwing/` directory.
- **config (Breaking):** `~/.config/sparkwing/profiles.yaml` profiles
  now carry the full backend triple (`state`, `cache`, `logs`) alongside
  any `controller` / `token`. See
  [migration guide](docs/migrations/v0.5.0.md#profiles-absorb-all-backend-specs).
- **cli (Breaking):** `--on` and `--sw-on` are removed; `--profile`
  replaces them for storage / dispatch addressing. See
  [migration guide](docs/migrations/v0.5.0.md#--profile-is-the-only-where-flag).
- **cli (Breaking):** `--sw-target` is renamed to `--target` (same
  semantics -- the pipeline-internal deployment-environment selector,
  moved out of the `--sw-` namespace). See
  [migration guide](docs/migrations/v0.5.0.md#--profile-is-the-only-where-flag).
- **cli (Breaking):** `sparkwing run --on prof` no longer dispatches
  to a remote controller; use `sparkwing pipeline trigger ... --profile prof`.
  See
  [migration guide](docs/migrations/v0.5.0.md#sparkwing-pipeline-trigger-for-remote-execution).
- **orchestrator (Breaking):** Local execution against a remote profile
  dual-writes state to local SQLite + the profile's backend. Previously
  state went only to the resolved backend. See
  [migration guide](docs/migrations/v0.5.0.md#dual-write-state-when-local-execution-writes-to-a-profile).

### Removed

- **config (Breaking):** `.sparkwing/backends.yaml` is removed. State,
  cache, and logs specs move to per-profile entries in
  `~/.config/sparkwing/profiles.yaml`. See
  [migration guide](docs/migrations/v0.5.0.md#profiles-absorb-all-backend-specs).
- **config (Breaking):** `.sparkwing/sources.yaml`, `.sparkwing/runners.yaml`,
  `.sparkwing/sparks.yaml`, and `.sparkwing/pipelines.yaml` are removed
  as standalone files. Their content moves under top-level keys in
  `.sparkwing/sparkwing.yaml`. See
  [migration guide](docs/migrations/v0.5.0.md#single-sparkwingsparkwingyaml-per-repo).

### Fixed

- **orchestrator:** The dispatcher no longer hangs indefinitely when a
  per-node goroutine fails to terminate. `dispatch` bounds its
  post-DAG `wg.Wait` with `Options.DispatchWaitTimeout` (env
  `SPARKWING_DISPATCH_WAIT_TIMEOUT`, default 30m). On timeout it emits
  a `dispatch_wait_timeout` event with the list of stuck nodes and a
  full goroutine stack dump, then returns -- which fires the deferred
  concurrency-namespace release so a wedged run can't lock the rest
  of the fleet behind a process that will never make progress. Set to
  a negative duration (or `SPARKWING_DISPATCH_WAIT_TIMEOUT=off`) to
  restore the historical wait-forever behavior.
- **store:** `SQLITE_BUSY` under concurrent writers no longer fails the
  run. The state store opens with a 30s `busy_timeout` and takes its
  write lock at transaction start, so multiple `sparkwing run`
  invocations sharing one `state.db` wait their turn instead of aborting
  with `database is locked`. The local dashboard reads through a
  read-only connection so it can't starve out active runs.

### Docs

- **docs:** New "Gate-shaped pipelines" section in `docs/caching.md`
  documenting `OnLimit: Queue` plus `QueueTimeout` as the recommended
  pattern for CI gates contended across processes, instead of
  hand-rolling poll-and-retry around `OnLimit: Fail`.
- **docs:** New migration guide at `docs/migrations/v0.5.0.md` covering
  the config flatten, the new `pipeline trigger` verb, the `--profile`
  unification, and the dual-write state model.

## [v0.4.0] - 2026-05-20

A large release that converges on the v1-ready API surface. Two
foundational reshapes ship here: the **author-facing SDK** (`sparkwing/`)
is cleaned up -- `*Node`/`*NodeGroup` types renamed to `*JobNode`/`*JobGroup`,
30+ orchestrator-only plumbing symbols moved out, `Needs()` typed via the
new `Dep` / `WorkDep` interfaces, and the cache / spawn / risk APIs
reshaped -- and the **package layout** finalizes the public/private
boundary (`orchestrator/` → `internal/`, `logs/` → `pkg/logs/`,
`secrets/` → `internal/`, and several more moves). Adopters hit a lot of
compile errors in one release; this is deliberate so the rest of the
v0.x line can stay quiet.

Other major adds: declarative target/runner config via new `backends.yaml`
/ `runners.yaml` / `sources.yaml`; OpenAPI 3.0 spec for the controller
HTTP API; `.apidiff/` snapshots for every covered package; storage +
cipher conformance test suites; release tooling that auto-rewrites
`[Unreleased]` to a versioned section and uses the CHANGELOG entry as
the GitHub Release body.

### Added

- **web:** `Tab` / `Shift+Tab` cycles the active tab in the runs view
  (Summary, Logs, Resources, DAG, Timeline, Setup) with wrap-around.
  Works from any column once a run is open, so operators can flip
  through tabs without first moving their cursor.
- **sdk:** `sparkwing.Dep` and `sparkwing.WorkDep` closed interfaces for
  typed dependency wiring. Implementations are limited to sparkwing-defined
  handles -- Plan-layer `Dep` is `*JobNode` / `*ApprovalGate` /
  `*JobGroup`; Work-layer `WorkDep` is `*WorkStep` / `*StepGroup` /
  `*SpawnSpec` / `*SpawnGenSpec`. The two interfaces are disjoint, so a
  `*WorkStep` in `*JobNode.Needs` (or vice versa) is a compile-time
  error.
- **sdk:** `sparkwing.NoCache` typed sentinel for explicit cache opt-out
  from a `CacheOptions.ContentHash` function. Distinct from the zero
  `CacheKey`: operators see an "explicit opt-out" log line vs a "missing
  key" warning, so deliberate skips no longer look like hashing bugs.
- **sdk:** `EnvVarDocer` optional interface. Pipelines implementing
  `EnvVars() []EnvVarDoc` declare the environment variables they read as
  inputs; `sparkwing pipeline describe` and `sparkwing run <pipeline>
  --help` surface them under an "environment variables" section
  alongside typed `Inputs`. Prefer typed `Inputs` for user-controlled
  values; `EnvVarDocer` is for process-wide config or external-system
  integration that already uses env.
- **sdk:** `OnTarget(...)` verb on `*JobNode` / `*WorkStep` and a
  `sparkwing.Target(ctx)` accessor for per-target dispatch. Pairs with
  the new `targets:` block in `pipelines.yaml` and the `--sw-target`
  CLI flag.
- **sdk:** `Workable` optional interfaces for declarative runner
  selection: `Requires() []string`, `Prefers() []string`, `WhenRunner()
  []string`. Chainable equivalents on `*JobNode` (`Requires`, `Prefers`,
  `WhenRunner`) for direct authoring; the Workable form lets shared job
  types carry their own constraints.
- **sdk:** Pipelines can implement optional `Config() any` and `Secrets()
  any` methods. The orchestrator resolves them at run-start from
  `pipelines.yaml` `values:` / `secrets:` blocks, the matched trigger
  spec, and any `targets[<active>]` overlay; step bodies read them via
  `sparkwing.PipelineConfig[T](ctx)` and
  `sparkwing.PipelineSecrets[T](ctx)`.
- **sdk:** Node body errors are automatically prefixed with the node ID
  when the author hasn't already prefixed them. Bare `return err` or
  `errors.New("boom")` from a step surfaces in dispatch logs as
  `<node-id>: boom` so failure messages identify the failing node by
  default; authors writing richer messages keep their full content.
- **config:** New declarative YAML surfaces for target + runner
  configuration. `backends.yaml` selects cache / logs / state backends
  per environment with `match:` rules. `runners.yaml` declares named
  runner pools with label constraints. `sources.yaml` declares config +
  secrets sources per target. `pipelines.yaml` gains `targets:`,
  `runners:`, `values:`, and `secrets:` fields. `profiles.yaml` gains
  `default_runner:`.
- **controller:** Cluster controller now exposes `GET
  /api/v1/runs/{id}/attempts` (the retry-tree listing the dashboard's
  Attempts dropdown reads) and supports `?full=1` on `POST
  /api/v1/runs/{id}/retry` for the "rerun all" mode. Matches the laptop
  controller's surface.
- **controller:** `pkg/controller.Server` functional options
  `WithArtifactStore` (enables `GET /api/v1/artifacts/{key}` for laptop
  mode) and `WithReconcileHook` (runs a sweep closure before list-runs /
  get-run reads, eliminating stale "running" rows from crashed in-process
  orchestrators). Pool routes (`GET /api/v1/pool*`) are registered only
  when `AttachPool` is also called.
- **controller:** Stdout logs backend (`pkg/storage/stdoutlogs`) for
  cluster runs that route logs to container stdout.
- **controller:** SQLite state backend wired through the backend factory.
- **cache:** `sparkwing-cache` accepts pflag-based command-line flags
  for every setting (`--addr`, `--data-dir`, `--proxy-cache-dir`,
  `--fetch-interval`, `--proxy-cache-ttl`, `--proxy-max-age`,
  `--api-token`, `--auto-register-repos`, `--ssh-key-dir`,
  `--git-fork-limit`). Each falls back to the corresponding env var so
  existing k8s ConfigMap-style configurations work unchanged.
- **wire:** OpenAPI 3.0 spec at `api/openapi.yaml` covering every public
  controller route -- runs, nodes, steps, events, triggers, approvals,
  concurrency, debug pauses, tokens, users, secrets, auth, agents,
  trends, pipelines -- plus the mode-conditional pool (cluster) and
  artifacts (laptop) routes. Two security schemes (`Authorization:
  Bearer <token>` for service callers, `Authorization: Session <id>` for
  dashboard browser flow) wired to the operations that require auth. 26
  component schemas mirror `pkg/store` types. The HTTP surface is now a
  formal contract (see VERSIONING.md).
- **wire:** Checked-in API surface snapshots under `.apidiff/` for every
  covered public package (21 files). The new `cmd/apidiff` tool walks
  each package's AST and emits a deterministic text representation of
  the exported declarations with godoc stripped. `sparkwing run lint`
  regenerates snapshots into a tempdir and diffs against the checked-in
  tree; drift fails CI with an educational message. Authors refresh the
  baseline via `bash bin/regen-api-snapshot.sh` and review the snapshot
  diff in the PR as the surface-change artifact.
- **wire:** Conformance test suites for the three plug-in interfaces:
  `pkg/storage.ArtifactStore`, `pkg/storage.LogStore`, and
  `pkg/controller.Cipher`. Each suite lives in a sibling conformance
  subpackage and exposes a `TestX(t, factory)` function any
  implementation can call from its own `*_test.go` to verify it
  satisfies the contract. Operations a partial implementation opts out
  of (e.g., `Read` on the write-only `stdoutlogs.LogStore`) skip rather
  than fail.
- **wire:** `pkg/storage.ErrNotSupported` sentinel for operations a
  partial implementation deliberately doesn't perform. Conformance
  suites use `errors.Is` against this to know which subtests to skip.
- **release:** `sparkwing run release` auto-rewrites `## [Unreleased]`
  to `## [vX.Y.Z] - YYYY-MM-DD` and commits before tagging, so the
  tagged commit ships with the versioned section in place. The
  GH-Actions workflow extracts that section as the GitHub Release body
  via `bin/extract-changelog-section.sh` -- the curated CHANGELOG entry
  is the release page, not a commit log dump.
- **release:** Hard refusal of any `v1.0.0+` tag. Pre-1.0 lock requires
  a deliberate code change to unlock (bumping to v1+ commits the API
  surface; this shouldn't happen by typo or `--bump major`). Companion
  `pre_v1_policy.go` linter catches doc drift -- CHANGELOG must not
  carry a `## [v1.x.x]` section, VERSIONING.md must not assert v1 has
  shipped, and any local `v1.0.0+` git tag is surfaced as a warning.
- **release:** CHANGELOG style + structure enforced by `changelog_lint.go`
  (`LintChangelog(body, migrations fs.FS)`), wired into `sparkwing run
  lint`. Two checks: no duplicate `### <Category>` sub-headings within a
  single section; every `(Breaking)` entry in a versioned section links
  to a real `docs/migrations/v<X.Y.Z>.md#<anchor>` whose file exists,
  anchor resolves to an H2, and version matches.
- **cli:** `sparkwing docs migrations` subcommand for in-CLI access to
  per-version migration guides. `list` shows every guide the binary
  embeds (with date + one-line summary); `read --version vX.Y.Z`
  prints one guide; `between --from --to` concatenates every guide in
  a version range with `---` separators. Default `-o markdown` so
  agents pipe straight into context. Stale-CLI hint surfaces in `list`
  when newer guides exist on the web.
- **cli:** `sparkwing docs versions` subcommand. Lists known versions
  (embedded by default; embedded + remote when `--web` is set), flags
  the latest, and surfaces source (`embedded` vs `remote`). Exits
  non-zero when `--web` discovery fails so scripts detect.
- **cli:** `--web` flag on `sparkwing docs read|list` and
  `sparkwing docs migrations read|list|between` fetches cross-version
  content from `sparkwing.dev` when the requested version isn't in
  the binary's embed. The CLI stays hermetic by default; `--web` is
  opt-in. Pairs with `--version vX.Y.Z|latest` to pick the target
  version. Companion `--no-cache` flag bypasses the on-disk cache for
  one invocation.
- **cli:** `sparkwing docs cache info` / `cache clear` for inspecting
  and resetting the on-disk web cache at `$XDG_CACHE_HOME/sparkwing/web/`
  (default `~/.cache/sparkwing/web/`). 24h TTL on `versions.json` and
  `*/index.json`; indefinite TTL on per-version `.md` content (tags
  are immutable).
- **cli:** `SPARKWING_DOCS_BASE_URL` environment variable overrides the
  default `https://sparkwing.dev` base for the web fetcher. Useful for
  testing against a local mirror; falls through to the default when
  unset.
- **cli:** `sparkwing info` advertises four new URLs for agent
  discovery: `docs_index_url`, `migration_guides_url`,
  `migration_guides_agent_url`, `migration_guides_index_url`.
- **cli:** `--sw-only=<glob>` runs a partial DAG by `path.Match` over
  JobNode IDs. Transitively pulls `Needs()` ancestors so the dispatch
  stays self-consistent -- a glob hitting only the leaves still
  schedules their preconditions. Fails fast on a malformed glob or a
  pattern that matches nothing. Mutually exclusive with
  `--sw-start-at` / `--sw-stop-at` (step-level vs job-level filter
  modes).
- **cli:** `--sw-no-cache` disables cache READS on this run's per-node
  `Cache()` lookups. Cache WRITES still occur on success, so the next
  run over the same content hits cache normally. Distinct from the
  bincache (compiled-pipeline-binary cache) gated by
  `SPARKWING_NO_BINCACHE`.
- **release:** `sparkwing run release` refuses to ship a version when
  `CHANGELOG.md` `[Unreleased]` has no entries. Pairs with the existing
  PR-time CI gate (`bin/check-changelog.sh`) that catches missing
  entries at review time.
- **release:** Pre-commit and pre-push pipelines (`sparkwing run
  pre-commit` / `pre-push`) with version-freshness gating, govulncheck,
  and a refusal-on-`replace` directive in `go.mod`.
- **release:** `.golangci.yml` at the repo root with a balanced linter
  set (gofumpt, goimports, govet, staticcheck, errcheck, errorlint,
  bodyclose, copyloopvar, ineffassign, misspell, nolintlint, unconvert,
  usestdlibvars, bidichk). Wired into the existing lint pipeline.
- **docs:** `VERSIONING.md` defines the stability promise for `pkg/`,
  `sparkwing/`, CLI flags, wire protocols, and YAML config formats;
  spells out what counts as a breaking change; documents the pre-1.0
  hard-cut stance.
- **docs:** `docs/changelog-style.md` documents the CHANGELOG conventions
  the pre-release manicuring agent applies. `docs/migrations/` carries
  per-version migration guides.
- **docs:** Curated godoc with `Example*` test functions across
  `sparkwing/` and every covered `pkg/` package (`storage`, `store`,
  `controller` + `client` + `pool`, `logs`, `pipelines`, `backends`,
  `runners`, `sources`, `runner`, `docs`, `color`, `localws`). Top-tier
  types use `[Type]` cross-reference links so `go doc` and pkg.go.dev
  render them as navigable.
- **docs:** `sparkwing.Bash` and `sparkwing.Exec` godoc now document the
  signal-propagation contract end-to-end (SIGKILL to direct child on
  `ctx` cancel, terminal SIGINT reaches the foreground process group,
  grandchildren are not torn down on programmatic cancel).

### Changed

- **web:** Arrow keys and `j`/`k`/`h`/`l` in the runs view now
  auto-select the focused run or node as the cursor moves -- pressing
  `Enter` is no longer required to load detail for the row under the
  cursor. Cursor movement clamps at the top and bottom of each list
  instead of wrapping. Arrow navigation into the tabs column has been
  removed; use `Tab` instead.
- **cli:** Tab-completion descriptions for pipeline-defined flags now
  carry an `[arg, optional]` / `[arg, required]` tag so they're
  visually distinguishable from sparkwing-owned flags like
  `--sw-profile` or `--help` in the flat menu. The internal
  `_complete-flags` and `_complete-pipeline-flags` helpers now emit
  two tab-separated columns (`--flag<TAB>description`) instead of
  three -- the group column was unused after the shell-side flatten
  step and the bucketing code in the zsh script has been removed.
- **docs:** Example struct names in sparkwing's own examples,
  documentation, and template scaffolders normalized to drop the
  redundant `Job` suffix (`&BuildJob{}` → `&Build{}`, `*BuildJob` →
  `*Build`, etc.). The constructor verb (`sparkwing.Job(...)`)
  provides "this is a job" context; the struct doesn't need to repeat
  it. No SDK behavior change; adopter code that names its own structs
  differently is unaffected.
- **sdk (Breaking):** `*Node` → `*JobNode`, `*NodeGroup` → `*JobGroup`,
  and `Node.RunsOn` / `NodeGroup.RunsOn` / `Node.RunsOnLabels` →
  `Requires` / `Requires` / `RequiresLabels`. The package-level
  `sparkwing.Job` and `sparkwing.JobGroup` constructors keep their
  names; only the Go type names change. JSON wire tags (`node`,
  `node_id`, `runs_on`, `node_start`, ...) are preserved for log /
  snapshot compatibility. See
  [migration guide](docs/migrations/v0.4.0.md#node-job-rename).
- **sdk (Breaking):** `Needs(...any)` and `NeedsOptional(...any)` on
  every dep-accepting type replaced with typed-dep signatures:
  `Needs(...Dep)` for Plan-layer methods, `Needs(...WorkDep)` for
  Work-layer methods. By-name string references to upstream nodes /
  steps are no longer supported -- the interfaces are intentionally
  closed to live handles. Patterns that built deps from yaml or other
  runtime sources via string IDs must do a two-pass construction (create
  all nodes / steps, store handles, then wire deps using the handles).
  See [migration guide](docs/migrations/v0.4.0.md#typed-dep-interfaces).
- **sdk (Breaking):** `CacheOptions.Key` → `Namespace`,
  `CacheOptions.CacheKey` → `ContentHash`, `HasKey()` → `HasNamespace()`.
  The new names match the actual concept (`Namespace` is a coordination
  scope; `ContentHash` is the content-addressed key driver) and remove
  the ambiguity that let two unrelated nodes collapse into one cache
  entry when an upstream input was missing. See
  [migration guide](docs/migrations/v0.4.0.md#cacheoptions-rename).
- **sdk (Breaking):** `JobSpawn(...)` returns `*SpawnSpec` (was
  `*SpawnHandle`); `JobSpawnEach(...)` returns `*SpawnGenSpec` (was
  `*SpawnGroup`). Chainable methods (`Needs`, `SkipIf`) now live on the
  spec types directly; the `Spec()` accessors are gone -- the handles
  were thin wrappers around the specs. Code that chains
  `sw.JobSpawn(w, ...).Needs(...)` is unchanged. See
  [migration guide](docs/migrations/v0.4.0.md#spawn-types).
- **sdk (Breaking):** `WorkStep.Destructive()` / `.AffectsProduction()`
  / `.CostsMoney()` replaced by `.Risk("destructive")` /
  `.Risk("prod")` / `.Risk("money")`. Labels are now author-defined
  (any kebab-case string works, e.g. `.Risk("rotates-key")`). Profile
  `auto_allow` switches from per-marker booleans to a list of labels.
  See [migration guide](docs/migrations/v0.4.0.md#risk-labels).
- **sdk (Breaking):** Roughly 30 orchestrator-only plumbing symbols
  relocated from the `sparkwing` package to `internal/sparkwingruntime`.
  Pipeline authors never called these -- they were always for code
  rebuilding the orchestrator. Runtime-mutator methods
  (`Plan.InsertChild`, `Plan.InsertExpanded`, `JobGroup.Finalize`,
  `WorkStep.Fn`, `WorkStep.MarkDone`, `SpawnSpec.SetResolvedID`,
  `SpawnSpec.MarkDone`) are no longer methods on the spec types; call
  them via `sparkwing.RuntimePlumbing.Fns.<Name>(...)`. `RuntimePlumbing`
  itself gains a `{Keys, Fns}` shape. See
  [migration guide](docs/migrations/v0.4.0.md#runtime-plumbing).
- **sdk (Breaking):** Author-facing surface cleanup. Renames:
  `JobNode.OnTargetList()` → `OnTargets()`, `WorkStep.OnTargetList()` →
  `OnTargets()`. Removals: `JobNode.OnFailureNodeID()`,
  `JobNode.Dynamic()`, `JobNode.IsDynamic()`, `sparkwing.ToKebabCase`,
  `sparkwing.LookupInstance`, `sparkwing.Runtime()` alias,
  `sparkwing.WithJob` / `JobFromContext` / `JobStackFromContext`,
  `sparkwing.SetDebug` (unexported -- `SPARKWING_DEBUG` at process
  start is the only supported toggle). See
  [migration guide](docs/migrations/v0.4.0.md#sdk-surface-cleanup).
- **sdk (Breaking):** `TriggerInfo.Env` removed. Trigger-supplied values
  now flow through the pipeline's typed `Config` struct via the
  trigger's `values:` block in `pipelines.yaml` (e.g. `on.push.values`)
  with a matching `sw:"..."` tag on a Config field, read in step bodies
  via `sparkwing.PipelineConfig[T](ctx)`. See
  [migration guide](docs/migrations/v0.4.0.md#trigger-values).
- **runtime (Breaking):** Package layout reorganized to finalize the
  public / private boundary:
  - `orchestrator/` → `internal/orchestrator/`. User repos MUST migrate
    to `pkg/runner.Main()`.
  - `secrets/` → `internal/secrets/`. External consumers implement
    `pkg/controller.Cipher` (two methods, `Seal` + `Open`).
  - `logs/` → `pkg/logs/` (promoted: now part of the public surface).
  - `controller/client/` → `pkg/controller/client/` (promoted).
  - `logutil`, `bincache`, `otelutil`, `profile`, `repos` → `internal/`
    (demoted: implementation detail).
  - `internal/local/` collapsed into `pkg/controller/`; mode is now
    determined by functional options (`AttachPool` for cluster;
    `WithArtifactStore` + `WithReconcileHook` for laptop).
  - `InProcessDispatcher` moved to `internal/inprocdispatch/`.

  See [migration guide](docs/migrations/v0.4.0.md#package-relocations).
- **runtime (Breaking):** Maintenance methods on `pkg/store.Store` hidden
  behind the `store.Maintenance` bridge. The 9 reaper / sweep methods
  (`ReapExpiredTriggers`, `FailNodesInRun`, `FailStaleQueuedNodes`,
  `FailExpiredNodeClaims`, `ReapStaleConcurrencyHolders`,
  `ReapStaleConcurrencyWaiters`, `SweepExpiredConcurrencyCache`,
  `SweepLRUConcurrencyCache`, `ReconcileConcurrencyKeys`) are no longer
  on the public `Store` API. Call them via
  `store.Maintenance.<Name>(s, ctx, ...)`. See
  [migration guide](docs/migrations/v0.4.0.md#store-maintenance).
- **controller (Breaking):** `pkg/controller.Server.WithSecretsCipher`
  now takes a `pkg/controller.Cipher` interface instead of a concrete
  `*secrets.Cipher`. Concrete-type callers continue to work via
  structural typing; external consumers can now supply custom cipher
  implementations without depending on sparkwing's secrets package. See
  [migration guide](docs/migrations/v0.4.0.md#cipher-interface).
- **cli (Breaking):** Five CLI flag renames:
  - `--sw-change-directory` → `--sw-cd` (the `-C` short form is unchanged)
  - `--sw-for` → `--sw-target` (the `Job.OnTarget("...")` author API is
    unchanged)
  - `--sw-on` → `--sw-profile`
  - `--sw-from` → `--sw-ref` (env-var bridge `SPARKWING_FROM` →
    `SPARKWING_REF`)
  - `--sw-allow-destructive` / `--sw-allow-prod` / `--sw-allow-money`
    collapsed into one `--sw-allow LABEL[,LABEL...]` flag (repeatable;
    comma-separated).

  See [migration guide](docs/migrations/v0.4.0.md#cli-flag-renames).
- **cli (Breaking):** Retired flags. `--sw-retry-of` / `--sw-full` use
  `sparkwing runs retry RUN_ID [--failed | --all]`. `--sw-job` /
  `--sw-prefer` declare runner selection in the pipeline via
  `Job.Requires` / `Job.Prefers`. `--sw-backends-env` -- fix `match:`
  rules in `backends.yaml` or `DetectEnvironment` logic.
  `--sw-config` preset feature removed. `--help-all` removed
  (`--help` now shows everything). Flag-group section headers in
  `--help` and tab-completion dropped (one flat list). See
  [migration guide](docs/migrations/v0.4.0.md#cli-retired-flags).
- **cli (Breaking):** `wing` CLI binary retired. `sparkwing run` is the
  only entry point. Scripts that invoked `wing ...` must update to
  `sparkwing run ...`. See
  [migration guide](docs/migrations/v0.4.0.md#cli-retired-flags).
- **cli (Breaking):** `--json` and `--pretty` flag aliases removed
  across every command. They were soft duplicates of `--output json` /
  `--output pretty`. Update scripts and shell aliases to use the
  canonical `-o`/`--output` form (e.g. `sparkwing runs list -o json`).
  See [migration guide](docs/migrations/v0.4.0.md#cli-output-aliases).
- **cli (Breaking):** `SPARKWING_NO_CACHE` env var renamed to
  `SPARKWING_NO_BINCACHE`. The new `SPARKWING_NO_CACHE` env var (and
  its CLI flag `--sw-no-cache`) gates the per-node result cache --
  what most operators mean when they say "no cache." Update shell
  aliases or CI configs that set `SPARKWING_NO_CACHE` expecting
  bincache-bypass behavior. See
  [migration guide](docs/migrations/v0.4.0.md#no-cache-env-rename).
- **config (Breaking):** `pipelines.yaml` `group:` field and the matching
  `--group` flag on `sparkwing pipeline new` removed. The field had no
  backing on the `pipelines.Pipeline` struct, so strict YAML parsing
  rejected any file that used it. Strip `group:` lines from existing
  `.sparkwing/pipelines.yaml` files. Plan-DAG UI grouping
  (`sw.GroupJobs`, `GroupSteps`) is a separate feature and is
  unaffected. See
  [migration guide](docs/migrations/v0.4.0.md#pipelines-yaml-group).
- **wire (Breaking):** `LogRecord` JSON shape loses the (always-empty)
  `job` and `job_stack` fields, following the removal of
  `sparkwing.WithJob` / `JobFromContext` / `JobStackFromContext`.
  Consumers of JSON log streams that explicitly read these fields will
  see them as missing rather than empty. See
  [migration guide](docs/migrations/v0.4.0.md#logrecord-fields).
- **cli (Breaking):** `sparkwing info -o json` field names normalized
  on the `docs` sub-object. The previously-flat `web` key splits into
  named URL fields with `_url` suffixes: `web` → `web_url`,
  `llms_full` → `llms_full_url`, `llms_txt` → `llms_txt_url`. Three
  new fields (`docs_index_url`, `migration_guides_url`,
  `migration_guides_agent_url`, `migration_guides_index_url`) join
  the object. Consumers parsing `sparkwing info -o json` against the
  `docs` sub-object must update field reads. See
  [migration guide](docs/migrations/v0.4.0.md#info-docs-json).
- **sdk (Breaking):** `pkg/docs.Entry` and `pkg/docs.MigrationEntry`
  reshaped to align with the web's `/docs/index.json` and
  `/migrations/index.json` JSON schemas. `Entry` drops its `Path`
  field (the cache-internal relative path) and now matches
  `{Slug, Title, Summary, Bytes}`. `MigrationEntry` is
  `{Version, Slug, Title, Date, Summary, Bytes}` (with `Slug` ==
  `Version` for parity with the web schema). External consumers
  reading `pkg/docs.List()` or `pkg/docs.MigrationsList()` results
  must update field names; the underlying JSON shape now matches
  what the web emits so agents can consume either source with one
  schema. See
  [migration guide](docs/migrations/v0.4.0.md#pkg-docs-entry-reshape).
- **cache:** `sparkwing-cache` business logic moved from
  `cmd/sparkwing-cache/main.go` (~1700 LOC) into a new `internal/cache`
  package. HTTP wire protocol unchanged; same routes, same shapes;
  existing clients (`pkg/storage/sparkwingcache` adapter, etc.) work
  without modification. Knobs (`APIToken`, `AutoRegisterRepos`,
  `SSHKeyDir`, `GitForkLimit`) resolved from `cache.Config` instead of
  ad-hoc env / hardcoded path reads inside the package; env-var
  fallback now lives at the binary entry point.
- **code-health:** `.golangci.yml` adoption cleared 135 findings across
  the tree. Mechanical mix: gofumpt + goimports formatting, US-locale
  spelling normalization (with `cancelled` / `Cancelled` exempted
  because it's the persisted `Outcome` constant), `usestdlibvars` (HTTP
  verbs / statuses pinned to stdlib constants), `errcheck` wraps,
  `bodyclose`, `errorlint` `%w`, `nolintlint` directives, idiomatic
  naming (`SparkAscii` → `SparkASCII`, etc.). No behavior changes.

### Fixed

- **cli:** `sparkwing run` no longer fails with `-modfile cannot be
  used in workspace mode` when a `go.work` is in scope. When sparkwing
  detects a workspace, it skips its `.resolved.mod` overlay so the
  workspace's module resolution wins, and prints a one-line warning to
  stderr so it's clear sparks pinning is dormant for that build. Honor
  `GOWORK=off` and the explicit `GOWORK=<path>` form. Sparks resolve
  itself (`sparkwing sparks resolve`) still requires no workspace in
  scope and now returns a friendly error instead of the raw toolchain
  message. The canonical multi-module local-dev pattern is documented
  in `docs/sparks.md` -- list every repo you're editing in
  `.sparkwing/go.work`.
- **controller:** `TrendPoint.avg_wait_ms` is now actually computed
  (`started_at - created_at` averaged per bucket, excluding zero-created
  / clock-skew rows). The dashboard's "avg wait" chart shows real
  intake-to-start latency instead of flat zero.
- **controller:** Cluster controller's retry response now returns the
  canonical shape (`{"status":"pending", "trigger_source":"retry",
  "started_at":<creation time>}`) matching the laptop controller. Prior
  cluster behavior used inconsistent field names (`trigger` vs
  `trigger_source`) and status values (`running` vs `pending`);
  dashboards talking to a cluster controller no longer need to
  special-case the response.
- **controller:** Cluster controller pre-allocates the Run row in
  `pending` state before invoking a retry trigger, eliminating the
  window where the retry had been accepted but no row existed yet.
- **controller:** Dead route registration for `GET /api/v1/auth/session`
  removed. The route was registered twice in `pkg/controller/server.go`;
  Go's `http.ServeMux` specificity made the outer (unauthenticated)
  registration win, leaving the inner copy as unreachable dead code.
  Resolved to the intended unauthenticated path (the handler reads
  `Authorization: Session <id>`, not a bearer token).
- **controller:** Stale `handleWaiterNotify` doc comment referenced a
  `coalesced` SSE event that the handler never emits. Rewritten to
  match the three terminal events the handler actually sends (`ready`,
  `superseded`, `stream_end`).
- **cache:** Fragile `init()` ordering in `sparkwing-cache` where
  directory creation ran at package-load time against hardcoded
  `/data/*` paths, before env-var parsing could rebind those paths.
  Directory creation now happens inside `cache.New(cfg)` AFTER the
  resolved Config is in hand. `backgroundFetchLoop` /
  `proxyCleanupLoop` accept the cancellable ctx and exit cleanly on
  shutdown (the prior shape blocked SIGTERM for the full sleep
  interval).
- **cli:** `RunLocal` now surfaces `res.Error` when a run-lifecycle
  failure occurred (previously dropped).
- **cli:** sqlite state without an explicit path falls back to
  `DefaultStateDB` (previously empty-string).
- **cli:** `opts.SparkwingDir` is now treated as the directory, not the
  `pipelines.yaml` path.
- **cli:** Tab-completion wires `--sw-target` / `--sw-prefer` /
  `--sw-backends-env` / `--sw-job` correctly.
- **cli:** OnTarget-skipped jobs are hidden from the CLI plan listing
  (UI metadata still surfaces the skip), and when shown they render
  dimmed with a `[skip: target]` marker.

### Removed

All breaking removals in this release are paired with replacements and
listed above under **Changed**. Quick inventory: `sparkwing.SetDebug`
(debug flag now `SPARKWING_DEBUG`-only), `JobNode.OnFailureNodeID()`,
`JobNode.Dynamic()` / `IsDynamic()`, `sparkwing.ToKebabCase`,
`sparkwing.LookupInstance`, `sparkwing.Runtime()` alias,
`sparkwing.WithJob` / `JobFromContext` / `JobStackFromContext`,
`LogRecord.Job` / `JobStack` fields (and the always-empty `job` /
`job_stack` JSON tags), `TriggerInfo.Env`, `pipelines.yaml` `group:`
field, `--group` flag on `pipeline new`, `--sw-retry-of` / `--sw-full`
/ `--sw-job` / `--sw-prefer` / `--sw-backends-env` / `--sw-config` /
`--help-all` CLI flags, the `wing` CLI binary, `internal/local/`
package (collapsed into `pkg/controller/`).

Non-breaking removals (no replacement needed): `PoolListForTesting` on
`pkg/controller.Server` (had zero callers anywhere; add a same-package
test helper in a `*_test.go` file if you need PVC introspection in
tests). Vestigial `sdk_doc.go` files under `pkg/store/`, `pkg/logs/`,
and `pkg/controller/client/` (replaced by `doc.go` files describing the
actual public surface).

## [v0.3.0] - 2026-05-13

Pre-changelog snapshot. Detailed history wasn't tracked in this file
for releases before v0.4.0; the git log (`git log v0.2.1..v0.3.0`) is
the source of truth. Subsequent versions are documented here in full.

## [v0.2.1] - 2026-05-07

Pre-changelog snapshot. See `git log v0.2.0..v0.2.1`.

## [v0.2.0] - 2026-05-06

Pre-changelog snapshot. See `git log v0.1.0..v0.2.0`.

## [v0.1.0] - 2026-05-06

Initial public release.
