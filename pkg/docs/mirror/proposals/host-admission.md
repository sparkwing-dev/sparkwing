# Host-resource admission control (proposal)

**Status:** queued. Not implemented. Feedback wanted before any code lands.

## The gap

Sparkwing today has one concurrency primitive: namespace + `Max=N` +
`OnLimit=Queue|Fail|Coalesce|CancelOthers|Skip`. It's a clean
*coordination* primitive: "only one prod-deploy at a time," "single-flight
this build behind a content hash." It is **not** a *throttling*
primitive: "regardless of namespace, don't let more than M sparkwing
processes use this host."

When users hit host-capacity constraints (two heavy runs would
oversubscribe the box) the only knob in the toolbox is `Max=1` on a
coordination namespace. Coordination and throttling then ride the same
mechanism, with two concrete consequences observed in the field:

1. **A coordination bug becomes a host-wide deadlock.** The
   v0.4.0 dispatcher hang -- a wedged `state.wg.Wait` that never
   released the namespace slot -- locked every other run on the host
   until the wedged process was killed. The watchdog landed in v0.5.0
   (see [CHANGELOG](../../CHANGELOG.md)) bounds the failure, but the
   class of "coordination bug cascades into a throttling failure"
   remains as long as the two mechanisms are entangled.
2. **Users can't partition state per logical caller** without losing
   the host throttle they were getting as a side effect. The throttle
   was the only thing preventing oversubscription; dropping it to
   improve isolation is a bad trade.

Other systems all separate the two:

| System | Coordination | Host throttle |
|---|---|---|
| Kubernetes | service / pod affinity | node `resources.requests` + `limits` |
| systemd | slice / scope dependencies | `CPUQuota`, `MemoryMax` |
| Bazel | `tags = ["exclusive"]` | `--jobs`, `--local_ram_resources` |
| sparkwing | namespace `Max=N` | -- |

## Proposed shape (v1)

A new primitive, **`HostAdmission`**, enforced before any namespace
acquisition:

- **`SPARKWING_HOST_MAX_RUNS=N`** env var (plus `--sw-host-max=N`
  flag at the CLI layer) caps concurrent `sparkwing run` processes on
  this machine. Default: unset = no cap (current behavior).
- **`SPARKWING_HOST_ADMISSION_POLICY={queue|fail}`**: what a process
  does when the host is full. Default `queue`.
- **`SPARKWING_HOST_ADMISSION_TIMEOUT=30m`**: how long `queue` will
  wait before promoting to `fail`. Zero = wait forever. Matches the
  `QueueTimeout` shape on `CacheOptions`.

Wired in at `internal/orchestrator/main.go` before `RunLocal`, parallel
to the existing `acquireBoxSlot` host-local semaphore but with explicit,
configurable, documented behavior and a stream event so the dashboard
shows the wait. The existing `acquireBoxSlot` becomes the implementation
hook; this proposal formalizes and exposes it.

## Concrete design sketch

### Backing store

A separate small SQLite file `~/.sparkwing/host-admission.db` (or
flock-only file at `~/.sparkwing/host-admission.lock`) tracks N tokens.
**Deliberately separate from `state.db`** -- the whole point is
decoupling. If `state.db` contention is what's wedging runs (the v0.4.0
failure mode), the admission gate must not share its lock surface.

For v1 a single `host_admission_holders` table is enough:

```sql
CREATE TABLE host_admission_holders (
    pid          INTEGER NOT NULL,
    run_id       TEXT NOT NULL,
    pipeline     TEXT NOT NULL,
    claimed_at   INTEGER NOT NULL,
    lease_expires_at INTEGER NOT NULL,
    PRIMARY KEY (pid)
);
```

Acquire = count active (non-expired) holders, insert if `count < N`,
else queue. Release = delete row by `pid`. Lease expiry is the safety
net for crashed processes: a stale `pid` whose lease has passed gets
swept on the next acquire.

### CLI surface

```
sparkwing host-admission status     # show current holders + queue
sparkwing host-admission release    # operator escape hatch
```

### Stream events

- `host_admission_queued`: `{slot_max, position, wait_started_at}`
- `host_admission_granted`: `{slot_max, waited_ms}`
- `host_admission_timeout`: `{slot_max, waited_ms, policy}`

These let the dashboard render "waiting for host slot 2/4 (1m32s)"
without per-process polling.

## Non-goals (explicit)

- **No CPU / memory quotas.** That's a kubernetes / systemd job, not a
  sparkwing job. Docs point users at those for the rich version.
- **No replacement for namespace concurrency.** These are separate
  primitives, not alternatives. A run can be gated by BOTH a host slot
  AND a namespace slot; they compose.
- **No cross-host admission.** That would require a coordinator (the
  controller). A future v2 could add it, with the namespace-concurrency
  store as the rendezvous, but v1 is host-local.
- **No fairness across pipelines / users.** First-come first-served by
  `arrived_at`. Priority queues are a future optimization.

## Open questions

1. **Should `acquireBoxSlot` (today's implicit semaphore) be the
   implementation, or a separate primitive that runs alongside it?**
   The cleanest answer is "promote `acquireBoxSlot` from internal
   helper to documented public surface, and have it back the new
   env-var / flag knobs." That avoids two host-throttle mechanisms.
2. **What's the default for `SPARKWING_HOST_MAX_RUNS`?** Unset = no
   cap is the safe rollout, but a sensible default (e.g.
   `max(1, NumCPU/2)`) would benefit users who never set it
   explicitly. Risk: backwards-incompat for users running >N
   concurrent processes today who don't expect to be queued.
3. **`pipeline trigger`'s `--detach` semantics.** When a detached
   trigger fires and the host is full, does the controller queue it,
   or fail-fast? Today there is no controller-side host admission;
   this proposal is host-local only. Cluster admission is the v2
   discussion.
4. **Interaction with the dispatch watchdog (v0.5.0).** The watchdog
   releases the namespace slot on timeout; it should similarly release
   the host slot. Implementation: `defer hostRelease()` at `Run` entry,
   parallel to `defer planRelease()`.

## Out-of-scope for this proposal

- Anything cross-host.
- Anything that requires a schema change to `state.db`. The
  admission store is deliberately separate.
- Replacing namespace concurrency. Strictly additive.

## What "ship it" looks like

A single PR adding:

- `pkg/hostadmission/` with the acquire/release/sweep API.
- Wiring in `internal/orchestrator/main.go` before `RunLocal`.
- `sparkwing host-admission status|release` subcommands.
- Tests: contention burst (N+1 racing acquires; exactly N grants, 1
  queued); crash recovery (stale pid sweep); timeout (queued waiter
  ages out cleanly).
- Docs: a new `docs/host-admission.md` covering the env vars, the
  stream events, and the relationship to namespace concurrency.
- CHANGELOG entry under `### Added` (cli + config scopes).

## Decisions wanted before implementation

- Q1: yes, promote `acquireBoxSlot` rather than a parallel mechanism.
- Q2: ship with unset default; revisit after one release of field
  data.
- Q3: defer to the cluster-admission v2 discussion; v1 is host-local.
- Q4: yes, watchdog releases both.

If those land roughly right, this is ~a-day of focused work.
