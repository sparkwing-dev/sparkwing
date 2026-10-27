# Diagnosing admission

The daemon keeps a bounded JSON Lines journal in `<SPARKWING_HOME>/wingd/`, beside
`d.log` and `state.json`. The default home is the one Sparkwing reports in
`sparkwing daemon status`. The journal survives daemon exit and needs no runs
store. `events.jsonl` and its two numbered rotations retain at most 48 MiB.
On Unix, SIGUSR1 captures diagnostic stacks in `d.log.stacks`, capped at 2 MiB.
Each daemon start generates a fresh random diagnostic `incarnation`. `seq`
increases within a source and incarnation. Journal read errors are reported.

```sh
sparkwing daemon events --run RUN_ID -o json
sparkwing daemon events --since 30m --kind shutdown
sparkwing daemon events --run RUN_ID --explain
sparkwing daemon events --run RUN_ID --explain -o json
```

The command reads files directly, even when the daemon is down. `events` can
filter by run, elapsed duration, repeated or comma-separated kind, or incarnation.
It shows the newest 50 matching records by default. `--limit` accepts a positive
output count; increase it to include older matching records. Output keeps
chronological order.

JSON output is one event per line followed by a `kind: page` record whose
`truncated` field says whether older matching records were omitted. JSON is the
default when piped. Plain and pretty output warn on stderr when records are
omitted. With no retained journal, human output prints the directory it checked
and JSON emits an empty final page. Unreadable records are skipped, and their
count is printed on stderr. Records are ordered
by timestamp, incarnation, then sequence.

`--explain` renders a run's admission timeline in sentences, including descendant
job slot requests and attached children owned by that run. Its JSON output
preserves all structured records without page metadata. It requires `--run` and
cannot be combined with list filters, or limits.

A retained record carries a timestamp, source,
incarnation, sequence, kind, run identity where known, and decision inputs in
snake_case `data` fields. Connection records carry the peer PID when the operating
system supplies it. After a connection identifies its run, later records carry
its run ID and pipeline.

`start`, `ready`, `drain_begin`, `drain_end`, and `shutdown` describe lifecycle.
`request`, `queued`, `grant`, `denied`, `rejected`, `eviction`, `superseded`, `backfill`,
`reprioritize`, `contended`, `queue_timeout`, `cancellation`, and `cancel`
describe admission decisions. `child_attach_request`, `child_attach`,
`reattach_request`, `reattach_accepted`, `reattach_refused`, `release`, and
`grace_expiry` describe lease ownership. `headroom_sample` records capacity
inputs. `connection_opened`, `connection_handshake`, `connection_closed`,
`handshake_refused`, and `message_refused` describe transport. Individual health
probe connections are omitted.
`dropped` counts records lost when the bounded writer buffer filled.

`child_attach` records carry the requested parent ID and the resolved live
parent ID. A rejected child attach names the requested parent and, when an
ancestor blocks it, the resolved parent and blocking ancestor. `cancel` records
carry the requesting peer's identity, affected live runs, and blocked descendant
IDs, including departed descendants that cannot attach again.

Journal writes do not hold admission. Policy endpoint URLs in records keep only
the scheme, host and path. Userinfo, query parameters and fragments are omitted.

## Queue views

```sh
sparkwing queue list -o json
sparkwing queue list --profile prod --group g:deploy -o pretty
sparkwing queue priority --run RUN_ID --set front
```

`queue list` reads one controller snapshot. A profile selects a remote
controller. Without a profile, the CLI starts the local controller on demand.
Bare `queue` shows help. Failed reads return nonzero with no success record on
stdout, so an unreachable controller cannot look like an empty queue.

The admission snapshot retains capacity, holders, connected runs, ordered
waiters, pressure measurements and runner headroom. `--group` reads one
concurrency group, including its effective capacity, used cost, separate run
and job IDs, holder leases, supersession and waiter positions. JSON returns one
snapshot. Plain output uses tab-separated records.

`queue priority` changes local machine admission priority. It accepts an
integer, including zero, or `front`/`back`, resolved against the other runs in
the queue. It runs on the machine whose admission queue holds the run and
refuses `--profile`. The result preserves prior and new priorities and
positions. A holder keeps its existing lease. A missing run
returns a receipt and exits 1.

## Doctor reports

```sh
sparkwing doctor -o json
sparkwing doctor --timeout 15s -o pretty
sparkwing doctor --dry-run -o plain
```

Doctor inspects the project Go toolchain, repository hooks and competing CLI
installs. Local controller findings come through its lifecycle interface.
Both modes use the same diagnostic path. The CLI never opens `state.db`,
repairs private-home permissions or reaps processes during this command.

JSON output is one report. `controller_checked` is true only after the lifecycle
check completes. `findings` retains each finding's `code`, `message`, optional
`unit` and `hint`. When inspection fails, `controller_error` names the failure,
the report retains any completed findings, and the command exits nonzero.
Toolchain, hook-survey and install results remain in the same report. An empty
finding array with `controller_checked: false` does not establish controller
health.

The timeout bounds inspection and its subprocesses. It defaults to ten seconds
and accepts any positive duration. Cancellation keeps completed observations.

<!-- design:begin -->
Native runtime construction is not connected in this rewrite build. Doctor
reports machine observations and returns an unavailable controller error.
The machine hook survey uses the final machine reader. Lifecycle tests with
fakes cover the CLI composition. Native store-schema,
service-skew and removed-timer checks still require the runtime implementation.
Permission repair and process cleanup need controller-owned operations before
they can be exposed again. The retained sweep implementation keeps successful
cleanup results when an outcome-event write fails, including after run pruning.
Its report carries a `record_error` warning beside that session. Plain reports
count these as `unrecorded_step_outcomes`. A failed sweep still returns an error.
This does not connect process cleanup to the lifecycle CLI.
<!-- design:end -->
