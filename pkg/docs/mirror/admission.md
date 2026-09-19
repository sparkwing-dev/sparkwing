# Local admission policy

Sparkwing's admission daemon decides when work may consume CPU and memory on one
machine. It does not assess job risk. Risk declarations and approvals remain
deterministic pipeline contracts and are unaffected by admission mode.

The default `auto` mode needs no configuration. To change the machine-wide
policy, create `~/.config/sparkwing/admission.yaml` as an owner-only file and
restart the daemon. `sparkwing queue` reports the active mode.

```yaml
mode: auto # off, auto, jev, or custom
```

The modes are:

- `off` does not gate CPU or memory. The daemon still owns run lifecycle,
  measurement, queue visibility, and deterministic concurrency groups.
- `auto` uses measured duration and resource profiles, weighted workload
  classes, bounded short-job backfill, and aging. It is the default.
- `jev` starts from `auto` and asks TypeSafe Jev whether a well-measured short
  candidate should spend additional bounded backfill time. Every hard resource
  and semaphore check remains in code. Missing credentials, timeouts, malformed
  responses, and low-confidence answers fall back to `auto`.
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

Auto backfill uses measured p99 duration. Unknown-duration jobs receive the
single opportunistic backfill retained for first-run liveness, but cannot keep
passing a protected waiter. Measured short jobs may continue using spare
capacity until the older waiter's delay budget is spent.

## Custom policy

Custom fields also tune the deterministic baseline and fallback used by `jev`:

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

## Jev policy

Set `TYPESAFE_API_KEY` in the daemon's server-side environment, then select
`jev`:

```yaml
mode: jev
jev:
  model: jev-latest
  timeout: 300ms
  min_confidence: 0.5
  min_probability: 0.7
  max_backfill: 5s
```

Jev receives queue resource and duration summaries, pipeline/repository labels,
and the candidate's workload class. It does not receive pipeline arguments,
logs, source, or secrets. Its answer is a typed choice between waiting and a
short backfill. Sparkwing validates confidence and duration bounds, then
rechecks the live deterministic ledger before any grant. Queue JSON reports Jev
attempt, admit, and fallback counters for comparison with `auto`.
