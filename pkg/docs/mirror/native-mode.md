# Local dashboard (native mode)

Running `sparkwing` executes pipelines in-process on your laptop. Native mode adds one thing on top of that: a way to watch several runs side by side.

The design is small:

1. Every local `sparkwing` run writes records to the SQLite store under `~/.sparkwing/`.
2. `sparkwing dashboard start` spawns a detached local server (`pkg/localws`) against that store, hosting the embedded dashboard SPA, the JSON API, and the log endpoints on one port (default `http://127.0.0.1:4343`). `sparkwing dashboard status` and `sparkwing dashboard kill` manage its lifecycle.

No daemon, no controller pod, no queue, no cluster lifecycle commands.

## What gets written per run

Run state lives in the SQLite store at `~/.sparkwing/state.db`. Per-run artifacts live under `~/.sparkwing/runs/<runID>/`: one `.log` file per node, plus `_envelope.ndjson` for run-level events (run start, plan, finish). The dashboard reads both the store and the logs.

Run IDs are timestamp-prefixed, so they sort chronologically.

`SPARKWING_HOME` overrides the `~/.sparkwing` root; see [config-reference.md](config-reference.md).

## Running the dashboard

```
sparkwing dashboard start    # spawn detached server (replaces any running one)
sparkwing dashboard status   # report liveness, print URL
sparkwing dashboard kill     # stop it
```

The CLI binary ships with the dashboard embedded; nothing else needs to be installed. `start` detaches a child process, writes its PID to `$SPARKWING_HOME/dashboard.pid`, appends output to `$SPARKWING_HOME/dashboard.log`, and returns once the listener accepts connections. Re-running it drains any dashboard already on file -- stopping the running server -- and starts a fresh one in its place. It refuses only when the resident dashboard is a newer version than the CLI, telling you to run `sparkwing version update --cli` or `sparkwing dashboard kill` first.

For the bind address and the other `dashboard start` flags, see [cli-reference.md](cli-reference.md).

## Why no daemon

A daemon would buy a queue, a scheduler, and a shared HTTP API. Locally that complexity is not worth it:

- **Concurrency**: run five `sparkwing`s at once and you get five entries. That is the user's call.
- **History**: the local store is the history.
- **Webhooks and remote triggering**: that is the cluster's job.
- **Background runs**: `sparkwing ... &` works in any shell.

## Multi-run demo

In one terminal, start a couple of background runs:

```
sparkwing run build &
sparkwing run test &
```

In another, start the dashboard:

```
sparkwing dashboard start
```

Point your browser at `http://127.0.0.1:4343`. Both runs stream live; when they finish, status flips to `passed` or `failed`.

## What lives in the controller instead

Cluster mode. The controller dispatches Kubernetes Jobs, ingests GitHub webhooks, and tracks team-wide history -- none of which applies when you iterate on your own laptop.

