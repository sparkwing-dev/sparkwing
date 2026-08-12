# Local Execution

Sparkwing pipelines run anywhere -- on a Kubernetes cluster, on your
laptop, or both. This is a core design advantage: your CI/CD is not a
black box in the cloud, it is a portable program you can run yourself.

## Why local execution matters

Most CI systems only run inside their own infrastructure. If GitHub
Actions is down you can't deploy; if your Jenkins server crashes,
builds stop. Your ability to ship depends on their uptime.

Sparkwing pipelines are Go programs. You can run them on any machine
with a Go toolchain; Docker only matters when a pipeline step builds
container images. This means:

- **Deploys don't stop when services go down.** GitHub down? Your
  laptop can still build, push images, and update your cluster.
- **Fast iteration.** Local Docker cache, local Go module cache, no
  upload round-trips. Edit -> build -> deploy in seconds.
- **Debuggable.** When a pipeline fails, run it locally with the same
  code and see what happens. No "push and pray."

## How it works

```bash
# Run locally -- uses your Docker, your caches, your machine
sparkwing run build-deploy

# Run locally, but record state to a remote profile's backend
sparkwing run build-deploy --profile prod

# Trigger remote execution -- the cluster runs it via the controller
sparkwing pipeline trigger build-deploy --profile prod
```

`sparkwing run` always executes on the machine you invoke it from.
`--profile` only changes *where state/cache/logs live* and which auth is
used to reach them; the work still happens locally. To hand execution to
a cluster, use `sparkwing pipeline trigger` (covered below). All three
run the same pipeline code -- the difference is where the work happens and
where its records land.

### Local execution

```
Your laptop:
  1. sparkwing run compiles the pipeline from .sparkwing/
  2. Pipeline runs whatever its code says (test, build, deploy, etc.)
  3. sparkwing run records the run to ~/.sparkwing/
     (SQLite + per-run log files)
```

Your laptop runs the pipeline directly. No sparkwing controller is
involved. Each invocation writes its outcome to the local SQLite store
under `~/.sparkwing/`, which is what `sparkwing dashboard start` reads.
Run `sparkwing dashboard start` once and leave it up to watch
concurrent runs in a browser without needing any remote service.

Local pipelines share a small admission daemon named wingd. It starts on
demand and normally needs no operator attention. `sparkwing daemon status`
reports whether it is running; JSON output includes the serving binary and its
source revision. `sparkwing daemon restart` replaces only an answering daemon
with the installed Sparkwing build. Existing holders reconnect and reattach to
their durable leases, while a deliberately stopped daemon stays stopped. A
release-pinned pipeline can use that refreshed daemon without replacing it
with the older release build.

When you run locally against a remote profile (`sparkwing run X --profile
prod`), the run dual-writes state to both the profile's backend and the
local SQLite store. The remote is canonical; the local copy is a free
byproduct, so `sparkwing runs list` on your laptop sees the run afterward
even with no network. Set `mirror_local: false` on a profile to skip the
local copy for automated workers that fire off many runs.

See [native-mode.md](native-mode.md) for the full local-mode design.

### Remote execution

```
Your laptop:
  1. sparkwing pipeline trigger tarballs .sparkwing/ + working tree
     (incremental sync)
  2. sparkwing POSTs the upload + a trigger to the profile's controller

Cluster:
  3. Controller records the trigger; a polling runner claims it
  4. Runner clones the upload, compiles, runs the pipeline
  5. Your laptop streams logs back via the logs service
```

The controller is the gatekeeper for prod-side execution: only the
cluster can push to ECR, update gitops, and dispatch warm runners.

`sparkwing pipeline trigger <pipeline> --profile prod` submits the trigger
to the profile's controller for remote execution. The chosen profile must
have a `controller:` set; passing a controller-less profile errors with a
clear message. By default the command follows the remote run until it
reaches a terminal state -- full log streaming when the profile defines a
logs URL, node-status updates from the controller otherwise. Pass
`--detach` to return as soon as the trigger is registered without
following.

A follow exits on the run's outcome, the same way a local `sparkwing run`
does: 0 when the run succeeded, 1 when it failed or was cancelled, with the
run's status block and failing-node errors printed to stderr so a
`> run.log` redirect still shows why. If the follow ends without a readable
terminal status -- a dropped connection, a controller restarting mid-run --
the command exits 3 rather than guessing an outcome, and names the run to
re-check with `sparkwing runs status --run <id> --profile <p>`. `--detach`
always exits 0: it reports that the trigger was queued, not how the run
ended.

## Authorization model

Sparkwing intentionally does **not** try to be a permissions boundary
between developers and infrastructure. Authorization is enforced where
it actually lives: the registry, the gitops repo, kubectl. A
developer with ECR push and gitops write access can deploy with or
without sparkwing.

**What sparkwing controls:**

- Which clusters a pipeline can dispatch to (via the `--profile` target's
  controller and its bearer token / scope).
- Audit trail of who ran what, when, from where (in the runs store).
- Consistent workflow (tests always run before deploy, declared once
  in the Plan).

**What infrastructure controls:**

- Who can push images to ECR (IAM roles).
- Who can push to the gitops repo (GitHub permissions).
- Who can `kubectl` into the cluster (RBAC).
- Who can call the controller API (bearer tokens scoped per principal;
  see [auth.md](auth.md)).

If you want to prevent a developer from deploying to production, the
right approach is to not give them the credentials -- not to rely on
sparkwing to block them.

## When to choose which mode

| Mode | Where it runs | Speed | When to use |
|------|--------------|-------|-------------|
| `sparkwing run <pipeline>` | Your laptop | Fast (local caches) | Day-to-day development, fast iteration, local-only deploys |
| `sparkwing run <pipeline> --profile prof` | Your laptop | Fast | Local execution that records state to a shared profile's backend |
| `sparkwing pipeline trigger <pipeline> --profile prof` | Cluster | Medium (remote build) | Production deploys, deploys requiring cluster credentials, parity with webhook flow |
| Git push -> webhook | Cluster | Medium | Automated CI/CD on every commit |

## More than one sparkwing on one machine

A machine can end up with more than one sparkwing binary. `go install`
drops one in `$GOBIN`, the source installer builds one into
`~/.local/bin`, a package manager may leave a third in `/usr/local/bin`.
Each is a complete sparkwing.

That only matters when they disagree, and they eventually do. PATH is
not one list: an interactive shell orders it from your shell profile,
while a launchd job, a systemd unit, or a cron entry carries whatever
PATH its own configuration sets. The two can resolve `sparkwing` to
different builds, so the same command resolves to a different program
depending on how it was launched.

Sparkwing handles this by keeping the copies from corrupting each
other's state and by telling you they exist -- it never picks a winner
or touches a binary it did not install.

### Version memory is per install

Each install records the version it last ran as in its own file under
`~/.sparkwing/last-version.d/`, keyed by a digest of the binary's
resolved path (so a symlink or a `/tmp` vs `/private/tmp` alias is one
install, not two). The upgrade notice -- the one-line changelog pointer
printed after a binary changes -- compares each install only against
what it, itself, last ran as. Two copies taking turns can no longer
rewrite a shared record into upgrades and downgrades that never
happened. A separate `SPARKWING_HOME` keeps separate stamps.

### Finding a split install

Every surface that knows about installs reports them, read-only:

- `sparkwing doctor` scans your PATH *and* the well-known install
  directories (`~/.local/bin`, `$GOBIN`, `$GOPATH/bin`,
  `/usr/local/bin`, `/opt/homebrew/bin`) -- a scan that trusted only the
  caller's PATH would report a clean machine from the very shell whose
  neighbor is the conflict. Copies reachable through a symlink collapse
  into one entry. The finding makes a sweep unclean, and each copy is
  printed with the exact reversible `mv` that retires it.
- `sparkwing info` reports the running binary's resolved path and any
  other installs (`executable.path` / `executable.other_installs` in
  `-o json`), and `sparkwing info --for-agent` includes the same
  identity so an agent knows which build its evidence came from.
- `sparkwing update` names any other copies after installing, and its
  `go install` fallback states exactly where the new binary landed --
  including when the binary you ran was *not* the one replaced.
- `bin/install.sh` (the source installer) reports copies outside its
  destination and never modifies anything outside `$DEST`.

### Fixing it

Pick one:

- **Keep one copy.** On Unix, retire the others with the printed guarded
  `mv -n`; it refuses when `<path>.superseded` already exists, and its printed
  undo likewise refuses if the original path has been recreated. Paths with
  whitespace or shell metacharacters are quoted as one shell word. On Windows,
  Sparkwing prints both exact paths and asks you to rename with File Explorer
  or a command quoted for the shell you chose. It does not pretend one command
  can safely quote every legal filename in both cmd.exe and PowerShell. Nothing
  in Sparkwing runs the remedy for you, because it cannot know which copy you
  meant to keep.
- **Keep both, fix the caller.** Point the launchd plist, systemd unit,
  or cron entry at an absolute path rather than at a bare `sparkwing`
  on a PATH you do not control. This is the right fix when the two
  copies are deliberate.

Nothing here ever deletes or renames a binary.

## Per-host concurrency

Two `sparkwing run` invocations on the same machine compete for the same
CPU. Local runs are arbitrated by a per-host admission daemon
(`wingd`) -- invisible infrastructure you never install, start, or
tune. The first run that needs admission brings one up: a lock file under
the sparkwing home makes the race safe, so one process wins and the rest
connect to the winner. The daemon exits on its own once the machine has
been idle for a while, coming back the next time a run needs it.

### Who hosts the daemon

**The installed Sparkwing distribution owns daemon lifecycle. Pipeline
clients declare required capabilities and use the running daemon; they
never host, replace, or upgrade it.**

The daemon is always an installed sparkwing build -- the CLI, or
`sparkwing-runner` on a runner box -- and never a per-repo pipeline
binary. `sparkwing run` hands each run the exact CLI that launched it as
the daemon host, through `SPARKWING_WINGD_BIN`; a pipeline binary invoked
directly falls back to the `sparkwing` on PATH. You can export
`SPARKWING_WINGD_BIN` yourself to point a directly-invoked pipeline
binary (a systemd unit, a deploy box) at a sparkwing that is not on PATH,
and `sparkwing run` will not overwrite a value you set.

A newer *installed* sparkwing transparently replaces a running older
daemon. Pipeline binaries never do, so one repo bumping its `.sparkwing/`
SDK pin cannot churn the daemon every other repo on the box shares.

### Running with no daemon available

Two situations look similar and are not.

**Nothing is arbitrating the box** -- no daemon running, no sparkwing
installed to start one. Most runs proceed uncoordinated, saying so once
on stderr. What they lose is host arbitration: CPU and memory charges are
not held against anything, so concurrent runs on that box can
oversubscribe it. What they keep is `.Concurrency()`: box- and run-scoped
groups are enforced through the shared store instead of the daemon, so
"one deploy at a time on this box" still holds. The difference is crash
cleanup -- the daemon frees a killed run's slot the instant the kernel
closes its socket, while a store slot survives until `sparkwing doctor`
reclaims it.

The exception is a pipeline that **reserves host capacity** with a
plan-level or node-level `.Resources()` pin. That run fails, naming the
fix. CPU and memory have no fallback arbiter, so there is no weaker
version of the reservation to fall back to -- there is only quietly not
making it, while other work on the box does the same.

**A daemon is running but this binary cannot speak to it** -- its
protocol is older than the pipeline binary's SDK pin, and pipeline
binaries never replace a daemon. Every run fails here, pinned or not.
Something is actively holding capacity for other work, and joining it
unadmitted oversubscribes the machine rather than merely going
uncoordinated. The error names both versions and the release to install.

`SPARKWING_ALLOW_UNADMITTED=1` forces the uncoordinated path in either
case, for an operator who knows what else runs on the box. It is an
environment variable rather than a flag because the runs that need it are
the ones no CLI launched, and it is read strictly: only the exact value
`1` turns the check off. Dry runs (`--sw-dry-run`) are exempt from both
refusals -- they mutate nothing and finish in seconds.

The fix in every case is the same: install or update the sparkwing CLI on
the host, or point `SPARKWING_WINGD_BIN` at an installed one.

One in-body feature does change shape without a daemon.
`sparkwing.ToolSlot(ctx, group)` -- the budget a job body takes out
around a tool that manages its own parallelism -- returns
`granted=false`, its documented fallback, and the body uses whatever
private serialization the tool ships with. Job bodies already have to
handle that return.

These rules are evaluated once, at run start. A daemon that dies
*during* a run is a different path: the run's client reconnects and
reattaches its lease, spawning a replacement through the resolved host
binary if one exists, and if none does the next admission the run needs
fails loudly rather than silently continuing unarbitrated.

The process connects when it needs admission. Explicit run resources and
plan-level `.Concurrency()` groups are admitted at run start and held by
the open connection for the run's lifetime. Unpinned host CPU and memory
are admitted per node as the DAG dispatches, so a fast early node can run
while a later heavy node waits for capacity. While work waits it prints a
single queue-position line on stderr (`queued for local admission:
position 2 of 3 ...`) and Ctrl-C cancels the wait cleanly. When a run
process dies -- crash, kill, or power event -- the kernel closes the
connection and the daemon releases the lease immediately, finalizes the
orphaned run record, and admits the next waiter. There are no heartbeats,
leases to tune, or polling loops. Nested runs never double-charge the
host: a parent passes its active lease to children it spawns (via
`RunAndAwait` or a step that shells out to `sparkwing run`), and each
child attaches to the parent's lease instead of re-admitting.

The ledger survives daemon restarts the same way runs survive daemon
handoffs: every transition is persisted, and a restarting daemon
restores the ledger and holds a short window for clients to reclaim
their leases before releasing the unclaimed rest. Restored unguarded
grants the current budget cannot hold are shed. A file that cannot be
parsed may describe guarded commands, so the daemon refuses admission
instead of guessing it is safe to release them. The startup error names
the file and the explicit recovery command. After verifying those
commands have stopped, `sparkwing daemon recover-state --yes` preserves
the bytes as `state.json.corrupt-<time>` for `sparkwing doctor` to report
and allows the next daemon to start cleanly.

### Declare nothing; sparkwing measures

The daemon measures the machine's real cores and memory and admits into
the headroom that is actually free, counting non-sparkwing load against
capacity. It also measures each pipeline and node over their first few
runs. Explicit run resources use the pipeline profile. Unpinned local
work uses the node profile at dispatch, so "one heavy build at a time"
emerges from measurement with no configuration. Declare nothing and it
works.

`sparkwing runs stats --capacity` shows what was learned: duration
percentiles, CPU and memory distributions (p50/p95/peak across recent
runs), and queue-wait p50/p99. The distributions tell you whether work
is steady or spiky and whether the box is too small; admission always
charges the measured peak, never a percentile, because under-reserving a
spiky node recreates exactly the oversubscription the daemon exists to
prevent. Percentiles inform, peak admits.

A pipeline may pass a cold-start hint with
`.Resources(sparkwing.Cores(n), sparkwing.MemoryGB(n))`, and may pin an
explicit cost when it must -- but a pin is policed, not trusted blindly:
when it drifts from what the pipeline actually uses, `sparkwing queue`
flags the gap so the pin can be corrected or dropped. The posture is
declare nothing and let sparkwing measure; pin sparingly, and sparkwing
polices the pin.

The same measurements answer the murkiest recurring question on a shared
box: is sparkwing slow, or is the machine busy? A holder is flagged
`(contended)` only when three things line up at once -- its elapsed time
has run well past its own measured p99, the host has been saturated by
non-sparkwing load for a sustained share of the run, and it has enough
duration samples to have a trustworthy baseline. An unprofiled run, a run
that is merely at the slow end of its own distribution, and a run on an
idle host are all left unflagged. When a contended run finishes it prints
a one-line attribution (`took 12m vs p50 8m30s; host saturated 62% of the
run`), and `sparkwing runs stats --capacity` shows each pipeline's
contended share, so "the tool is slow" becomes a measurement instead of a
guess. Detection is observability only; it never changes an admission
decision.

`.Concurrency(group)` is for *logical* mutual exclusion only -- a deploy
lock, a shared fixture -- never host sizing. A run- or box-scoped group
is local to the machine; a global-scoped group pools across the whole
fleet through the controller's shared state (see [sdk.md](sdk.md)).

### Recovering from bad measurements

Measurement drives admission, so a wrong reading needs an escape hatch
that does not mean "wait for the window to age out." There are two:

- **A host sensor that cannot read.** The `EXTERNAL` column carries the
  reading the availability math ran on, and the view prints how old that
  effective reading is and how recently the host sensor successfully read
  at least one pressure dimension. The deadband absorbs small wiggles, but
  reapplies its newest effective value within 30 seconds, so a recovered
  reading near an admission threshold cannot strand a waiter indefinitely.
  A failed sample updates neither timestamp and cannot apply its returned values.
  When a dimension cannot be read at all -- macOS with no
  `kern.memorystatus_level`, a platform with no host-pressure sensor -- the
  cell prints `unmeasured` rather than a figure, an `external: unmeasured on
  <dimension> (host sensor unavailable); no external load subtracted from
  available` line says what admission did about it, and the JSON row carries
  `"external_source": "unmeasured"`. A sampler response with no measured
  pressure dimension does not refresh the measurement age. Nothing is
  subtracted for a dimension nobody read, because a substituted figure in a
  measurement's format is what pinned memory headroom at zero on every box.
- **A misreading host sensor.** If the external-load reading is wrong and
  admission is queuing runs against phantom pressure, add `ignore-external`
  to the machine budget. Admission then plans against total capacity minus
  the reserve, subtracting no external load. The `EXTERNAL` column in
  `sparkwing queue` still shows the real reading -- observability stays
  truthful -- with an `external: ignored (operator setting, ...)` line that
  names which setting turned it on, and contention detection keeps using
  the real saturation. Use it alone (`ignore-external`) or alongside a cap
  (`50%,ignore-external`). Put it in the budget config file rather than the
  environment when you want it to outlive the daemon running now: the
  daemon is started on demand by whichever run needs it first and inherits
  that process's environment, so an exported variable lasts only as long as
  that one daemon. `sparkwing doctor` reports a machine admitting with
  external load ignored, so the state is findable without suspecting it
  first.
- **A poisoned learned profile.** One freak run can record an absurd peak
  that inflates a pipeline's charge for the rest of the window, and
  sustained external load can ratchet a still-measuring pipeline's demand
  floor upward. The floor self-corrects: each contended run that measures
  below it halves it, mirroring the ceiling-hit doubling that raised it.
  That correction only works if the run is admitted, so two rules keep it
  reachable. A charge resolved from measurement is capped at the machine's
  grantable ceiling on **both** cores and memory, and the run says so,
  naming the profile; `sparkwing doctor` flags the same state. And with
  nothing admitted, the queue head is granted on either dimension no matter
  what external load is reading, so a pipeline can always measure its way
  back down instead of being locked out by a floor it can never disprove.
  To clear a floor immediately anyway, reset with
  `sparkwing runs stats --reset --pipeline <name>` (profiles are keyed
  `repo/pipeline` for runs launched inside a git repo, exactly as
  `runs stats --capacity` shows them; a bare pipeline name reaches every
  repo-scoped key that carries it and the summary names each one): the
  learned samples, peaks, floors, waits, and contention tally are dropped
  so the pipeline re-learns from a cold start, and the command prints what
  it removed -- including a floor with no measured samples behind it, which
  is the state a pipeline that never finished a clean run is priced off. An
  explicit `.Resources()` pin is preserved -- admission keeps charging the
  pin while the profile re-learns. To reset every pipeline at once, use
  `sparkwing runs stats --reset --all --yes`.

A request that no release could ever satisfy -- a pin larger than the
machine -- is refused at submit with the arithmetic (`needs 12GiB of
memory, this machine has 8GiB`) rather than queued to a timeout. A box
that is merely busy still queues, because a holder finishing fixes that.

### Operating it

Day-to-day operation runs through two commands, and neither can hurt the
machine:

- `sparkwing queue` -- the truthful view of local admission: every
  resource holder with the repo it came from, how long it has held, and its
  cost; connected run registrations that hold no resources, labeled separately;
  every waiter in admission order with its position, priority, estimated start,
  and the resources that actually hold it under the ledger's liveness rules,
  plus a health flag on any holder that is not running cleanly:
  `(stalled)` for one that is alive but idle while runs wait behind it,
  and `(contended)` for one that is measurably slower than its profile
  while the host is saturated. A child run riding its parent's lease
  renders indented under that parent. The header summarizes the last day
  of admission outcomes in one line -- runs granted, median wait,
  evictions by key, queue timeouts, how many runs were contended, and how many
  younger backfills activated waiter protection -- so a chronic pattern shows
  up before it becomes an incident. It also
  names the serving daemon's version and uptime, and warns when an
  older-pinned pipeline binary is admitting outside the daemon. Every
  view states whether the daemon was reached: an idle machine and a
  socket that would not answer are different answers, and the second
  exits 4 with the dial failure named rather than printing an empty
  queue it never looked at.
- `sparkwing doctor` -- the one repair verb. It removes only provably-
  dead state (an interrupted run's leftover row, an orphaned lock file
  whose owner is gone) and reports what it found and did. Every report
  opens with the daemon's state -- serving with its version and protocol,
  none running, or unreachable -- because the checks below it only run
  when the daemon answered, so their emptiness means nothing on its own.
  An unreachable daemon is never a clean bill, and the run-row repair is
  skipped there rather than risk finalizing a run that daemon is holding.
  Standing problems it cannot safely repair -- repeated admission
  rejections, a daemon version skew, a contention-poisoned capacity
  profile, a daemon serving another sparkwing home at a version no
  release carries -- are reported with the exact fix instead. It never
  kills a process and never touches live admission, so it is safe to run
  at any time; on a healthy machine it finds nothing and says so.

Each sparkwing home keeps its own daemon, so a daemon for one home is
invisible from another even though both show up side by side in a process
listing. That makes a scratch daemon -- one a build with a local `replace`
directive left running, which reports version `v0.0.0` -- easy to mistake
for the machine's resident one and read its log as production state.
`sparkwing doctor` names any it finds, with the socket it answers on, so
the mistake is caught before it explains an unrelated failure.

The daemon writes an operational log to `wingd/d.log` under the sparkwing
home (`~/.sparkwing/wingd/d.log` by default) for when you want to see
what it did.

If a daemon is ever busy in a way its log does not explain -- burning CPU
with nothing queued, or answering nothing -- send it `SIGUSR1`
(`kill -USR1 <pid>`, POSIX only). It appends a line counting its
connections, holders, waiters, leases, and guards, followed by a stack
for every goroutine, to that same log. The daemon keeps running, so you
can capture the state before deciding whether to stop it, and the dump is
what a bug report about a stuck or spinning daemon should carry. Each
dump adds up to 2MB to `d.log`, which is rotated only when a daemon
starts, so ask for a few of them rather than a few hundred.

### Capping sparkwing's share of the machine

Measured admission is the primary mechanism, and for most machines it is
the only one you need. When you want a hard ceiling -- "CI may use at most
half my laptop" -- set one machine budget. It takes a core count, a
percentage, or both a core and a memory term, plus optional `enforce` and
`ignore-external` terms:

```
6               # at most 6 cores
50%             # at most half the machine's cores
6,8gb           # 6 cores and 8 GiB
50%,enforce     # half the cores, hardened at the OS level
ignore-external # admit against total capacity, ignoring external load
```

#### Where to set it

The same value is read from several settings, resolved in the order the
rest of the wing family uses -- the more specific setting wins:

| Setting | Reaches | Lives until |
|---|---|---|
| Internal daemon `--budget` argument (not a public CLI command) | that daemon process | that daemon exits |
| `SPARKWING_BUDGET` in the environment | any daemon spawned from that environment | that daemon exits |
| `~/.config/sparkwing/budget` (or `$XDG_CONFIG_HOME/sparkwing/budget`) | every daemon on the machine | you edit or delete the file |

The config file is the durable one, and it is the setting to reach for
when you mean "this machine, from now on". The admission daemon is
started on demand by whichever run needs it first, inheriting that
process's environment, so a budget exported in one shell applies to
whatever daemon that shell happened to spawn and disappears with it. The
file is read at daemon startup, so a budget written there is in force
again the moment a daemon respawns.

The file holds one setting line. Blank lines and `#` comments are
skipped, so the reason a budget is in force can live next to the budget:

```
# host sensor over-reads external load on this box
50%,ignore-external
```

A value that will not parse fails daemon startup rather than being
dropped, whichever setting it came from. A budget that silently does
nothing is worse than one that says it is wrong.

#### Seeing which one is in force

The budget caps the admission ledger below the machine total, so it holds
everywhere admission already runs, with no other change to how runs are
scheduled. `sparkwing queue` shows it as its own row in the headroom
arithmetic, naming the setting behind it
(`budget 6.0 cores (machine 10.0) (from config ~/.config/sparkwing/budget)`),
so an operator can revoke a cap they did not set themselves. With no
budget set anywhere the row says so rather than staying silent, because a
machine admitting against everything it has looks exactly like a
deliberate whole-machine budget from the outside. `sparkwing doctor`
reports a non-default budget too, and says out loud when the one in force
came from an environment that the next respawn may not carry.

A requested cap above the machine total is clamped to the machine, and the
daemon logs a one-line note when it does.

### Containers: the daemon respects its own cgroup

You do not have to set a budget to keep sparkwing inside a container. On
Linux the daemon reads its own cgroup v2 limits at startup (`cpu.max` and
`memory.max`, with a cgroup v1 fallback) and clamps capacity to them, so a
6 GiB container on a 24 GiB host plans against 6 GiB, never the host it
sits on. External-load sensing follows suit, measuring the container's own
CPU and memory usage rather than the machine's. `sparkwing queue` shows the
clamp as a `container limit: 6.0 cores (host 24.0), 6.0 GiB memory (host 24.0 GiB)`
row, and a machine budget still caps below the detected limit. macOS has
no cgroups and so no container path -- capacity there is always the host.

Add `enforce` to harden the cap at the operating-system level as well as
in admission:

- **Linux** places admitted run processes in a daemon-managed cgroup v2
  with `cpu.max` and `memory.max` matching the budget, a kernel wall. When
  the cgroup filesystem is absent or unwritable (an unprivileged laptop),
  the daemon logs a note and the admission cap still applies.
- **macOS** has no cgroups, so it demotes admitted runs to background QoS
  (the `taskpolicy -b` equivalent: efficiency-core scheduling and
  throttled I/O) and raises their scheduler nice. This is advisory
  scheduling that yields to foreground work, not a hard cap.

This is the one machine-level knob. It complements measured admission; it
does not replace it.

### Whoever owns the machine owns admission

The gate is host-local by design: two laptops pointed at the same shared
backend (Mode 2 / 3 / 4) each run their own daemon, and nothing
coordinates raw CPU across machines. On a Kubernetes runner the pod's CPU
is already bounded by the kube scheduler and the warm-runner pool's own
budget, so admission there belongs to the cluster, not to a sparkwing
daemon -- runner pods do not start one. Cross-machine coordination is the
job of global-scope `.Concurrency()` groups, which pool through the
controller's shared state.

## Pipeline configuration

Local vs remote is decided at invocation time (`sparkwing run` for here,
`sparkwing pipeline trigger` for the cluster), not declared per-pipeline.
Pipelines themselves only declare *triggers*:

```yaml
# .sparkwing/sparkwing.yaml
pipelines:
  - name: build-test-deploy
    entrypoint: BuildTestDeploy
    description: Build, test, and deploy
    on:
      push:
        branches: [main]
```

If a pipeline is locally-runnable (most are), `sparkwing run build-test-deploy`
just works. If a step needs cluster credentials it cannot reach from a
laptop, the pipeline author either dispatches the whole run remotely with
`sparkwing pipeline trigger`, or splits the deploy into a sub-pipeline that
runs on the cluster (`RunAndAwait`, reading typed output via `Ref[T]`; see
[sdk.md](sdk.md)).
