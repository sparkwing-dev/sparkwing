# Controller HTTP API

The controller exposes its HTTP API under the
`/api/v1` base path. The CLI, runners, the dashboard, and pipelines'
cross-run refs are all clients. Responses are JSON.

The complete route surface -- every method, path, and the scope each
requires -- is generated from the routing code in
[api-reference.md](api-reference.md). This page covers the cross-cutting
behavior that table doesn't.

## Authentication

Requests carry a bearer token, and each route declares the scope it
needs; `admin` satisfies any check. Token kinds, the scope set, the
unauthenticated endpoints, and first-visit admin bootstrap are in
[auth.md](auth.md).

## Run completion

`POST /api/v1/runs/{id}/finish` accepts `success`, `failed`, or `cancelled`.
Another status returns `400` without finishing the run. Repeating the same
status and error text succeeds without moving its finish time; a different
verdict after completion returns `400`. `POST /api/v1/runs` cannot attach a
finish time to a pending or running row, or reopen a pending row that already
has one.

## Payment references

A paid payment reference belongs to one team across prepaid grants and card
settlements. Repeating a card settlement for that team changes nothing. Reporting
it for another team returns `409` and leaves both teams’ balances and grants unchanged.

## Data downloads

The cache-grant download, upload and commit routes have been removed. The
controller no longer advertises `data_download_url` or `direct_data`, and
there is no cache-service fallback.

The verified byte-storage components use signed object URLs. Their authenticated
controller adapter is still being implemented. See [Data storage](data-uploads.md)
for the available components and the remaining integration.

Node logs use controller-proxied log reads, separate from byte-storage downloads.

## Webhooks

`POST /webhooks/github-app` ingests the [GitHub App](github-app.md)'s
deliveries, the only signed GitHub trigger. It is verified by HMAC
(`X-Hub-Signature-256`) against the App's webhook secret rather than a bearer
token, since GitHub can't carry one, and answers `401` when the signature does
not verify. The installation a delivery names picks the team, and the team's
subscriptions pick the pipelines; a pull request from a fork starts nothing. A
redelivery answers `duplicate` with the runs it already started, and a
delivery with no `X-GitHub-Delivery` header answers `400`. See
[security.md](security.md).

## Node logs

The controller serves node logs, which runners upload as segments (see
[Log completeness](observability.md#log-completeness)). Every read needs
`logs.read` and answers only for the caller's team:

- `GET /api/v1/runs/{id}/logs` reads the whole run.
- `GET /api/v1/runs/{id}/logs/{node}` reads one node, filtered by `tail`,
  `head`, `lines` or `grep`.
- `GET /api/v1/runs/{id}/logs/{node}/stream` follows a node over SSE
  through every attempt of it in this run, in order, holding the node's
  live-view watch while open, and ends with a `stream_end` event once the
  node is done with an outcome other than failed, or its run is over (only
  a failure can be retried), and its last attempt is sealed. Each line carries the cursor just past
  it as its event id; sending the last one back as `Last-Event-ID` (or
  `cursor=`, which the header overrides) resumes after it.
- `GET /api/v1/runs/{id}/logs/{node}/completeness` returns the node's
  completeness verdict.
- `GET /api/v1/runs/{id}/logs/search` searches one run, and
  `GET /api/v1/runs/grep` searches across runs; each reads at most 256 MiB
  and scans for at most 10 seconds.

On a node read, `grep=TEXT&line_numbers=1` returns NDJSON objects with
`line_no` (the position in the full node log) and `line`. Matching is
case-sensitive. `max_matches=N` caps the response; omitting it returns all
matches. Without `line_numbers=1`, the read returns plain text.

A runner compresses and hashes each log segment, asks
`POST /api/v1/runs/{id}/nodes/{nodeID}/log-segments` under its node or
trigger claim for one URL signed for that segment's exact size and
Content-MD5, and puts the segment there. It seals the attempt by posting
the manifest to `POST /api/v1/runs/{id}/nodes/{nodeID}/log-manifest`,
which the controller checks against the segments it signed. A launcher
pod's claim or a runner's node claim may finish both for its own attempt
for 60 seconds after its node reports its outcome. The routes and their scopes are in
[api-reference.md](api-reference.md).

## Run coordination

`POST /api/v1/runs/{id}/nodes/{nodeID}/execution-start` records one
attempt for the node's claim generation and ordinal. Repeating that exact
request is safe. The execution-start and node `touch` routes answer a
transient PostgreSQL lock or serialization conflict with `503` and
`Retry-After: 1`. Runners retry execution-start up to three times after
those responses or a transient connection error; a different store error
remains `500`.

A pipeline binary needs more than node state from whatever holds its
runs: it dispatches its own child triggers, and it measures what the run
cost so the next run of the same pipeline is priced from evidence. Those
reach the controller as routes too -- `/api/v1/runs/{id}/pending-triggers`
and `/api/v1/triggers/{id}/claim` for the child-trigger loop,
`/api/v1/pipelines/{name}/profile/observations`, `/contention`, and
`/waits` for the capacity profile, `/api/v1/runs/{id}/nodes/{nodeID}/usage`
for a reaped process's accounting, and
`/api/v1/maintenance/reconcile-orphans` for the sweep that closes runs
whose orchestrator died.

A capacity write names a pipeline rather than a run, so it is bound to a
live claim on a run of that pipeline: a node claim for a runner executing
one node, or the run's trigger claim for the orchestrator, which records
the queue wait before the first node exists and the run's measurement
after the last one is gone.

## Concurrency

The `.Memoize()` and `.Concurrency()` coordination primitives are backed
by the `/api/v1/concurrency/{key}/*` routes (acquire, heartbeat,
release, state, resolve). See [caching.md](caching.md) for the model.
