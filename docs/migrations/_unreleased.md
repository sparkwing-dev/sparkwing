# Migrating to the next release

## Metric sample kinds

Metric producers must identify interval readings and completed-command
reports explicitly. `MetricSample.OneShot()` uses `Kind`; it no longer
infers a command from positive CPU time.

Before:

```go
sample := store.MetricSample{TS: at, CPUMillicores: 200}
```

After:

```go
sample := store.MetricSample{Kind: store.MetricInterval, TS: at, CPUMillicores: 200}
```

Set `store.MetricCommand` for completed commands, even when `CPUTime` is
zero. Interval readings must leave `CPUTime` zero. HTTP metric requests use
`"kind": "interval"` or `"kind": "command"`. Missing kinds and unavailable CPU or RSS readings remain unknown
and do not qualify for learning. A later successful reading does not erase
an earlier unavailable reading.

Stop producers and consumers before upgrading controllers, workers and
local executors together. Older HTTP servers reject the new request field;
older readers can ignore it and retain the previous incorrect inference.
Rebuild pinned SDK consumers that produce metrics. A database requirement
protects direct database access, not mixed-version HTTP readers.

The database migration labels historical positive-CPU-time points as command
reports. Historical zero-CPU-time points remain unknown because their kind
cannot be recovered. Learned profile format 9 discards incompatible learned
estimates on access or update while preserving explicit pins and wait
statistics. New observations rebuild estimates; leave resource pins in place
until their replacement estimates have been verified for the workload.

## Profile observations

Profile observation callers must set `CPUMeasured: true` (HTTP
`"cpu_measured": true`) only when CPU was measured. An omitted or false flag
leaves existing profiles unchanged after input validation, including memory
and duration values. Measured zero CPU remains valid.

Host and worker admission require that flag before using learned CPU or
memory values, including demand floors and previous-version costs. Explicit
pins retain precedence. Duration estimates displayed for existing profiles
are separate from resource admission.
