# Local dashboard (native mode)

Running `sparkwing` executes pipelines on your laptop, each job in its own process. Native mode adds one thing on top of that: a way to watch several runs side by side.

The design is small:

1. Every local `sparkwing` run writes records to the SQLite store under `~/.sparkwing/`.
2. `sparkwing serve start` spawns a detached local server (`pkg/localws`) against that store, hosting the embedded dashboard SPA, the JSON API, and the log endpoints on one port (default `http://127.0.0.1:4343`). `sparkwing serve status` and `sparkwing serve stop` manage its lifecycle.

No daemon, no controller pod, no queue, no cluster lifecycle commands.

## What gets written per run

Run state lives in the SQLite store at `~/.sparkwing/state.db`. Per-run artifacts live under `~/.sparkwing/runs/<runID>/`: one `.log` file per node. A run you invoke yourself also gets `_envelope.ndjson` there for run-level events (run start, plan, finish); a child run dispatched locally by `sparkwing.RunAndAwait` writes only its node logs, since the envelope belongs to the invocation that owns the terminal. The dashboard reads both the store and the logs.

Run IDs are timestamp-prefixed, so they sort chronologically.

You do not have to assemble that path yourself. Each run records the directory as `log_path` on the `run_start` event and on the run's stored invocation, so `sparkwing runs status --run <id>` prints it (and `-o json` carries it as a top-level `log_path`). The recorded path belongs to the machine that executed the run, so a run you read back through `--profile` may name a directory that does not exist here; the text output marks those. Runs whose logs go to a controller or an object store leave the field out entirely.

`SPARKWING_HOME` overrides the `~/.sparkwing` root; see [config-reference.md](config-reference.md).

## Running the dashboard

```
sparkwing serve start    # start only when no instance is running
sparkwing serve status   # report owned process, URLs and readiness
sparkwing serve stop     # stop it
```

The CLI binary embeds the dashboard. `start` detaches a child and returns after an HTTP readiness response identifies that exact instance. A repeated `start` leaves the PID and effective options unchanged, even if the service is unhealthy or the installed binary changed. Its warning and build comparison explain the difference. Use `sparkwing serve restart` to replace the owned instance; unchanged flags retain the running options.

`stop` sends TERM, waits five seconds, then forces the same owned process to exit and waits up to two more seconds. Linux uses a process handle and boot/birth identity. macOS checks boot/birth immediately before each signal; a PID-reuse race between that check and the signal remains possible. Other platforms refuse verified lifecycle actions. An old numeric PID file without the new ownership record is unknown; automatic stop and replacement refuse it. The original process must be stopped through its existing owner before a new instance can be created.

`status` and mutation receipts share dashboard/API URLs, effective bind, PID, ownership, readiness, state paths and build comparison. Matching artifact SHA256 establishes a match; version labels and revisions alone do not. Running executable hashes remain unknown where the operating system cannot expose the executing artifact independently of its install path. Endpoint scope describes the bind, not verified access from another machine.

Piped output is one compact JSON service record; `--output pretty|json|plain` overrides detection. Plain prints the state word. Repeated start and stop succeed; stopped status exits 1, while unknown ownership or failed readiness exits 2. `sparkwing serve logs` reads the last 40 lines from a bounded 1 MiB tail; `--limit 0` skips history and `--follow` waits for new lines. Log records are compact JSON when piped.

For the bind address and the other `serve start` flags, see [cli-serve.md](cli-serve.md).

## Why no resident process

Locally, nothing needs to stay up between runs:

- **Concurrency**: run five `sparkwing`s at once and you get five entries. That is the user's call.
- **History**: the local store is the history.
- **Webhooks and remote triggering**: that is the cluster's job.
- **Background runs**: `sparkwing ... &` works in any shell, and `sparkwing run --sw-detached` hands the run to a consumer that exits when the queue drains.
- **Schedules**: an OS timer runs `sparkwing crons tick` every minute and sparkwing evaluates the cadences inside that tick, so the machine holds a timer rather than a scheduler. See [crons.md](crons.md).

The one daemon on a local machine is `wingd`, the admission daemon. It starts on demand when a pipeline needs a concurrency decision, serves the runs that asked for it, and exits when it goes idle. `sparkwing daemon status` reports it and never starts one.

## Multi-run demo

In one terminal, start a couple of background runs:

```
sparkwing run build &
sparkwing run test &
```

In another, start the dashboard:

```
sparkwing serve start
```

Point your browser at `http://127.0.0.1:4343`. Both runs stream live; when they finish, status flips to `passed` or `failed`.

## What lives in the controller instead

Cluster mode. The controller queues work for runners that poll and claim it, ingests GitHub webhooks, and tracks team-wide history -- none of which applies when you iterate on your own laptop.

