# Controller HTTP API

The controller exposes its HTTP API under the
`/api/v1` base path. The CLI, runners, the dashboard, and pipelines'
cross-run refs are all clients. Responses are JSON.

The complete route surface -- every method, path, and the scope each
requires -- is generated from the routing code in
[api-reference.md](api-reference.md). This page covers the cross-cutting
behavior that table doesn't.

## Authentication

Requests carry a bearer token, browser session or verified local identity.
The route reference lists accepted authority. Capability routes require
their declared grants, bindings and liveness; `admin` cannot bypass that
policy. Token kinds, the scope set, unauthenticated endpoints and bootstrap
are described in [auth.md](auth.md).

## Run detail

`GET /api/v1/runs/{run}` returns the shared run status object with `inputs`,
`jobs`, `attempts`, `logs` and `notices`. Job and attempt status excludes raw
declarations, holder identities and capability prefixes. Log subjects preserve
the distinction between job attempts and trigger, plan or eval work.

Inputs always use the display projection. Secret argument values are redacted.
Missing or invalid classification redacts every value, including on finished
runs. The `include` query cannot reveal secrets or select an execution view.
Members need `runs.read`. Bound claims need `source.read` or `trigger.host`
for that run. Missing or dead credentials receive `401`; live credentials
without the required authority receive `403`.

The controller requires its shared verifier and a complete status-read adapter.
An absent adapter returns `policy_missing`. It never fills missing history with
invented attempts or returns a partial status response after a read error.

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

`POST /api/v1/data/download` takes `{"digest":"<sha256>"}`. It requires a
live data capability and a digest in that caller's read set. The controller
resolves the team, repository and ref from authenticated authority. A digest,
team query or object path grants no access by itself. Ordinary member and
machine tokens cannot download bytes without the required data authority.

The response contains `url`, `headers`, `sha256`, `size` and `expires`.
Send only the returned headers to the storage URL. The controller charges the
stored object size against the team's daily download cap before signing.
The URL names one immutable object and expires after 60 seconds. Revocation
stops new signing requests. Issued URLs remain valid until their expiry.
S3 and filesystem backends use the same authorization boundary.

`POST /api/v1/data/has-many` takes `{"digests":["<sha256>"]}` and returns
`{"present":["<sha256>"]}`. Missing and unreadable digests are omitted.
An empty array returns an empty array. Duplicate digests appear once. An
optional `deadline` is an RFC 3339 timestamp that bounds this query. Presence
checks transfer no object bytes and consume no download allowance.

Both routes use the common error envelope. Missing or dead authority returns
`401`. Live authority without the required grant or read edge returns `403`.
An authorized missing blob returns `404` with `blob_missing`. A spent download
allowance returns `429` with `Retry-After`. Private backend errors return
`internal` without object keys or diagnostic text.

`GET /api/v1/services` announces the download route only when its authority,
read-set, metadata, signing and charging adapters are configured. Signed
URLs work through the backend selected by the controller. Cache grants and
`{kind,key}` download requests are removed. Logs use the controller's log routes.

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
`/api/v1/pipelines/{name}/sizing/observations`, `/contention`, and
`/waits` for the capacity profile, `/api/v1/runs/{id}/nodes/{nodeID}/usage`
for a reaped process's accounting, and
`/api/v1/maintenance/reconcile-orphans` for the sweep that closes runs
whose orchestrator died.

A capacity write names a pipeline rather than a run, so it is bound to a
live claim on a run of that pipeline: a node claim for a runner executing
one node, or the run's trigger claim for the orchestrator, which records
the queue wait before the first node exists and the run's measurement
after the last one is gone. Sizing writes require `node.report` and an
authority-bearing transaction adapter that rechecks the sealed repository and
pipeline. Without that adapter they return `policy_missing`.

## Concurrency

The `.Memoize()` and `.Concurrency()` coordination primitives are backed
by the `/api/v1/concurrency/{key}/*` routes (acquire, heartbeat,
release, state, resolve). See [caching.md](caching.md) for the model.
