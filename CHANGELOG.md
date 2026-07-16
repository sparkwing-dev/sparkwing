# Changelog

All notable changes to **sparkwing** are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). The release
pipeline refuses to ship a new version without a matching entry below.

## How to read this

Each entry leads with a bold scope (`**sdk:**`, `**cli:**`, `**controller:**`,
`**cache:**`, `**config:**`, `**release:**`, `**docs:**`, ...) so you can
scan for the surface that affects you. Breaking changes get an inline
`(Breaking)` marker after the scope and a link to a section in that
release's [migration guide](docs/migrations/) -- click through for
before/after code, ordering guidance, and gotchas the inline summary can't
fit.

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
