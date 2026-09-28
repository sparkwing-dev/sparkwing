# Diagnosing admission

The daemon keeps a bounded JSON Lines journal in `<SPARKWING_HOME>/wingd/`, beside
`d.log` and `state.json`. The default home is the one Sparkwing reports in
`sparkwing daemon status`. The journal survives daemon exit and needs no runs
store. `events.jsonl` and its numbered rotations hold daemon records;
`supervisor-events.jsonl` and its rotations hold supervisor records. The two
streams together retain at most 50 MiB. An `incarnation` file increments when a
daemon wins election. `seq` increases within a source and incarnation.

```sh
sparkwing daemon events --run RUN_ID -o json
sparkwing daemon events --since 30m --kind replacement
sparkwing daemon explain --run RUN_ID
sparkwing daemon explain --run RUN_ID -o json
```

Both commands read files directly, even when the daemon is down. `events` can
filter by run, elapsed duration, repeated kind, or incarnation. It shows the
newest 50 matching records by default. Use `--offset 50` for the previous 50 or
`--limit 0` for every retained record. New arrivals can shift an offset between
reads. JSON output is one record per line and is the default when piped; a
terminal gets human output. With no retained journal, human `events` output
prints the directory it checked. `explain` renders a run's admission timeline in sentences, including
node slot requests and attached children owned by that run. Its JSON output
preserves the structured records. A retained record carries a timestamp, source,
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
probe connections are omitted; supervisor failure episodes record their health.
`dropped` counts records lost when the bounded writer buffer filled.

The supervisor records `probe_failure_start` and `probe_failure_end` once per
failure episode. A `replacement` record gives the failed probe count, last
error, heartbeat counter, stale duration, whether the continuous failure
ceiling fired, largest supervisor tick gap, and the `dump_path`. On Unix, before
stopping the daemon, the supervisor signals its SIGUSR1 diagnostic handler and
saves the result as `dump-<timestamp>.txt` in the same directory. A failed
capture, including on Windows where SIGUSR1 is unavailable, appears in
`dump_error`; replacement still proceeds after a bounded wait.
