# Diagnosing admission

The daemon keeps a bounded JSON Lines journal in `<SPARKWING_HOME>/wingd/`, beside
`d.log` and `state.json`. The default home is the one Sparkwing reports in
`sparkwing daemon status`. The journal survives daemon exit and needs no runs
store. `events.jsonl` and its numbered rotations hold daemon records;
`supervisor-events.jsonl` and its rotations hold supervisor records. The two
streams together retain at most 50 MiB. The newest ten replacement dumps add at
most 20 MiB. The latest `d.log.stacks` source adds up to 2 MiB, so journal
and replacement evidence retain at most 72 MiB. An `incarnation` file
stores the daemon identity. Each replacement advances a readable stored value
by one; a missing or unreadable file uses a time-derived identity. File errors
are logged, and admission continues. `seq` increases within a source and
incarnation.

```sh
sparkwing daemon events --run RUN_ID -o json
sparkwing daemon events --since 30m --kind replacement
sparkwing daemon events --run RUN_ID --explain
sparkwing daemon events --run RUN_ID --explain -o json
```

Both commands read files directly, even when the daemon is down. `events` can
filter by run, elapsed duration, repeated kind, or incarnation. It shows the
newest 50 matching records by default. Use `--offset 50` for the previous 50 or
`--limit 0` for every retained record. New arrivals can shift an offset between
reads. Unreadable records are skipped, and their count is printed on stderr.
Records are ordered by timestamp, incarnation, then sequence. JSON output is
one record per line and is the default when piped; a terminal gets human output.
With no retained journal, human `events` output prints the directory it checked.
`--explain` renders a run's admission timeline in sentences, including descendant
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

`child_attach` records carry the requested parent ID and the resolved live
parent ID. A rejected child attach names the requested parent and, when an
ancestor blocks it, the resolved parent and blocking ancestor. `cancel` records
carry the requesting peer's identity, affected live runs, and blocked descendant
IDs, including departed descendants that cannot attach again.

The supervisor records `probe_failure_start` and `probe_failure_end` once per
failure episode. A `replacement` record gives the failed probe count, last
error, heartbeat counter, stale duration, whether the continuous failure
ceiling fired, largest supervisor tick gap, and the `dump_path`. On Unix, before
stopping the daemon, the supervisor signals its SIGUSR1 diagnostic handler and
saves up to 2 MiB as `dump-<timestamp>.txt` in the same directory. It keeps the
ten most recently modified dumps; the next stack capture replaces `d.log.stacks`. A
failed capture, including on Windows where SIGUSR1 is unavailable, appears in
`dump_error`; replacement proceeds after a wait of at most one second. A daemon
that never became ready records `daemon not ready, no dump` without waiting.
Supervisor records keep their enqueue time and daemon identity, and buffered
records drain on supervisor shutdown. Journal writes do not hold admission or
replacement. Policy endpoint URLs in records keep only the scheme, host, and
path; userinfo, query parameters, and fragments are omitted.
