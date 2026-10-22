# Local admission policy

Sparkwing's admission daemon decides when work may consume CPU and memory on one
machine. It does not assess job risk. Risk declarations and approvals remain
deterministic pipeline contracts and are unaffected by admission mode.

The store owns logical concurrency capacity in every execution mode. It commits
plan admission with permission to release work. An executing host requests its
complete job group set under its actual award, then acquires machine CPU and
memory. A queued set holds no partial grant. Every job needs a committed start
permit, including jobs without concurrency groups.

An executable job evaluates `SkipIf` and memo first on its executing host, under
that permit. The coordinator acquires no job groups for evaluation. A job that
skips still waits for its slot and briefly occupies a machine, then settles skip
and releases its groups. Approvals cannot declare concurrency groups. Acceptance
refuses them with an error pointing at the job after the approval. An approval's
`SkipIf` runs on the run's coordinator before requesting approval, holding no
capacity. A true predicate settles as skipped through the same verdict owner.
The store never runs SDK code.

Recovery reconciles released work immediately. If plan capacity has lapsed,
running jobs finish and their results settle while the run waits to reacquire.
Only new ready, approval and expansion releases wait for that capacity. A live
waiter keeps its arrival order through owner handoff.

For a run that waited, was refused, or lost its daemon, see [Diagnosing admission](diagnosing-admission.md).

The local controller starts on demand. It stays up for runs, queued triggers,
held leases and open dashboard connections. `sparkwing dashboard start --stay`
keeps it running until stopped. An installed systemd service or launchd agent
restarts it after failure. See [Local execution](local-execution.md).

Lease holders and queued runs retry with capped backoff for up to twelve minutes
after a connection breaks. A missing daemon host fails immediately, and a daemon
host that exits before serving gets at most three attempts. Each handshake can
wait a minute for an overloaded daemon. The replacement daemon keeps restored
leases for two minutes after it starts, so clients can reattach after a
restart. These waits require no admission setting.

The default `classic` mode preserves Sparkwing's admission behavior from before
selectable modes. To change the machine-wide
policy, set it in the `admission` section of `~/.config/sparkwing/config.yaml`
(see [Machine settings](machine-config.md)) and restart the daemon.
`sparkwing queue` reports the active mode.

```yaml
admission:
  mode: classic # classic, off, auto, or custom
```

The modes are:

- `classic` keeps strict priority and FIFO ordering, permits one opportunistic
  backfill past a blocked head, then protects that head. It is the default so
  upgrading Sparkwing does not change admission behavior.
- `off` does not gate CPU or memory. The daemon still owns run lifecycle,
  measurement and machine queue visibility. Store-owned concurrency groups still apply.
- `auto` uses measured duration and resource profiles, weighted workload
  classes, bounded short-job backfill, and aging.
- `custom` runs the deterministic scheduler with operator-selected weights,
  aging, and backfill-delay budgets.

## Workload classes

Classes express latency sensitivity without becoming absolute priority:
`critical`, `interactive`, `normal`, and `batch`. Managed pre-commit and
pre-push hooks are interactive; post-commit and scheduled work are batch; other
work is normal. Sparkwing never infers critical work. A pipeline can override
the inference when it has a stable operational reason:

```go
plan.AdmissionClass(sparkwing.AdmissionInteractive)
```

Higher-weight classes are considered more frequently, while aging raises older
work until it overtakes newer arrivals. `Plan.Priority` and `--sw-priority`
remain explicit strict ordering overrides and are separate from workload class.

Admission class is resolved into the sealed execution options. Local run and
job admission consume that winner. Explicit request settings outrank pipeline
declarations, which outrank the inferred class.

Auto backfill uses measured p99 duration. Unknown-duration jobs receive the
single opportunistic backfill retained for first-run liveness, but cannot keep
passing a protected waiter. Measured short jobs may continue using spare
capacity until the older waiter's delay budget is spent.

## Custom policy

Custom fields tune the deterministic scheduler:

```yaml
mode: custom
custom:
  class_weight:
    critical: 12
    interactive: 8
    normal: 4
    batch: 0
  aging_every: 4
  backfill_delay:
    critical: 0s
    interactive: 2s
    normal: 10s
    batch: 30s
  interactive_burst:
    cores: 1
    max_p99: 2s
    min_samples: 3
```

`aging_every` is the number of newer admissions that add one point to an older
waiter's class score. Zero disables class-weighted ordering. Omitted values keep
the `auto` defaults. The interactive burst is a single CPU-only lane: eligible
work must have at least `min_samples`, fit within both `cores` and `max_p99`, and
be interactive or explicitly critical. It may exceed CPU reservations by at
most `cores`; memory and semaphores remain hard limits, and a second burst waits.

## Stress-testing admission

The manual `admission-stress` pipeline supplies repeatable synthetic workloads;
it is not part of `gate` or release checks. Its profiles pin the resource
dimension under test so each admission mode sees the same charge while the work
itself performs real sleeps, hashing, allocation, and page touching. The
CPU-heavy and light-sequential profiles intentionally leave memory unpinned so
host memory pressure cannot obscure a CPU admission experiment.
`light-sequential` pins its complete run so its roughly one-second end-to-end
duration becomes the learned admission unit:

```bash
sparkwing run admission-stress-light-sequential -- --class interactive
sparkwing run admission-stress-light-parallel -- --class normal
sparkwing run admission-stress-medium-fan-in -- --class batch
sparkwing run admission-stress-cpu-heavy -- --class batch
sparkwing run admission-stress-heavy -- --class batch
sparkwing run admission-stress # the complete phased matrix, class=batch
```

Each workload has a stable pipeline name so its measured history cannot be
mixed with a different shape. To exercise duration-aware contention, start one
or more `admission-stress-cpu-heavy` runs as batch work, then submit
`admission-stress-light-sequential` as interactive work. Use
`admission-stress-heavy` to verify that hard memory limits remain intact.
Repeat the same sequence after each admission-mode change and compare queue
decisions and elapsed time. The profiles are intentionally short and bounded:
each heavy node runs for four seconds with two CPU workers; the
combined-resource profile has three such nodes using 512 MiB each.
