# Local Dashboard (native mode)

The "native mode" idea started as a daemonized local controller. It was
overkill. Running `sparkwing` already executes pipelines in-process on your
laptop. The only thing missing was seeing them side by side when you
have multiple runs going.

So the shipped design is much smaller:

1. Every local `sparkwing` run writes records to the SQLite store under
   `~/.sparkwing/`.
2. `sparkwing dashboard start` spawns a detached local web server
   (`pkg/localws`) against that store, exposing the dashboard and the
   JSON / logs APIs on one port (default `http://127.0.0.1:4343`).
   `sparkwing dashboard status` / `kill` manage its lifecycle.

No daemon. No controller pod. No queue. No cluster lifecycle commands.

## What gets written per run

Run state lives in the local SQLite store at `~/.sparkwing/sparkwing.db`
plus per-run log files under `~/.sparkwing/logs/`. The dashboard reads
both. Run IDs sort chronologically.

## Running the dashboard

```
sparkwing dashboard start          # spawn detached server (idempotent)
sparkwing dashboard status         # is it up? prints URL
sparkwing dashboard kill           # stop it
```

That is it. The CLI binary ships with the dashboard embedded; nothing
else needs to be installed. `start` writes a PID + log file under
`$SPARKWING_HOME` and prints the URL; re-running while it is already up
just prints the URL again.

## Why not a daemon

A daemon buys you a queue, a scheduler, and a shared HTTP API. Locally
none of that is worth the complexity:

- **Concurrency**: if you run 5 `sparkwing`s at once, you get 5 entries.
  That is the user's call, not the tool's.
- **History**: the local store *is* the history.
- **Webhooks / remote triggering**: that is the cluster's job.
- **Background runs**: `sparkwing ... &` works in any shell.

## Multi-run demo

Open two terminals. In each:

```
sparkwing run build &
sparkwing run test &
```

Open a third:

```
sparkwing dashboard start
```

Point your browser at `http://127.0.0.1:4343`. Both runs stream live;
when they finish, status flips to `passed` or `failed`.

## What still lives in the controller

Only cluster mode. The controller binary still dispatches Kubernetes
Jobs, ingests GitHub webhooks, and tracks team-wide history. None of
that is relevant when you are iterating on your own laptop.
