# Controller HTTP API

The controller and the logs service expose HTTP APIs under the
`/api/v1` base path. The CLI, runners, the dashboard, and pipelines'
cross-run refs are all clients. Responses are JSON.

The complete route surface -- every method, path, and the scope each
requires, for both services -- is generated from the routing code in
[api-reference.md](api-reference.md). This page covers the cross-cutting
behavior that table doesn't.

## Authentication

Requests carry a bearer token, and each route declares the scope it
needs; `admin` satisfies any check. Token kinds, the scope set, the
unauthenticated endpoints, and first-visit admin bootstrap are in
[auth.md](auth.md).

## Data downloads

`POST /api/v1/data/download` signs a short-lived download for an object. Send
`{ "kind": "binary", "key": "bin/<input-hash>" }` for a committed binary,
`{ "kind": "artifact", "key": "artifacts/blobs/<sha256>" }` for a committed
artifact, or `bins/<hash>` for a legacy cache binary. Use a reader token or a
run-scoped cache grant minted while its runner holds an exact trigger or node
claim. The controller checks the grant's claim generation and holder against
the live claim on each signing request. A grant from an earlier claimant or
one minted without a claim cannot sign. The controller also checks the live
run, team-owned key, committed object or legacy cache object, and team's daily download allowance
before it returns `{ "url": "...", "sha256": "...", "size": 123,
"expires": "..." }`. It charges the recorded object size when it signs the
URL.

Log downloads require a token with `logs.read` or `admin`. A run cache grant
cannot sign log downloads, even for its own run. Revoking a claimant's token
stops its cache grant from signing new URLs.

A `source` download names the exact `sources/<sha256>/<submission-id>` key
bound to the claim's run. User and raw runner bearers cannot sign it. A user
token with `runs.write` may reserve and commit that source before triggering;
the controller binds it to one run during trigger admission. See
[Direct data uploads](data-uploads.md).

The URL expires after 60 seconds and names one exact object. A request that
reaches the controller through the in-cluster Service gets a regional S3
presigned URL; a request through the public ingress gets a CloudFront signed
URL. The controller selects the path from the presence of `X-Forwarded-For`
or `X-Forwarded-Host`. Configure the ingress to overwrite these headers. A
local filesystem store continues serving bytes directly through its existing
artifact route. Treat the returned URL as a temporary
bearer credential.

Binary clients discover this route through `GET /api/v1/services`. The controller
omits it on public ingress if CloudFront signing is unavailable. An S3-backed
controller also announces direct uploads through `direct_data` and
`GET /api/v1/data/capabilities`. Runners use the cache service when an older
controller has no direct route. Cloud runners read committed cloud binaries,
or local binaries when the team enables `trust_local_builds`. They do not read
legacy cache binaries when direct uploads are active. See
[Direct data uploads](data-uploads.md), [Tenant limits](limits.md), and
[Self-hosting](self-hosting.md).

## Webhooks

`POST /webhooks/github/{pipeline}` ingests GitHub deliveries. It is
verified by HMAC (`X-Hub-Signature-256`) rather than a bearer token,
since GitHub can't carry one; the handler acts on `push` and
`pull_request` (opened/synchronize/reopened) and answers `ping`. A
delivery naming a repository the pipeline is not bound to answers `404`,
re-sending a body the controller already accepted answers `409` with the
run the first delivery produced, and a delivery with no
`X-GitHub-Delivery` header answers `400`. See [security.md](security.md).

`POST /api/v1/webhooks/github/bindings` (scope `admin`) stores the secret one
repository's deliveries to one pipeline are signed with and allows that
repository for the pipeline; `DELETE` on the same path removes it. Stored
bindings add to the `GITHUB_WEBHOOK_BINDINGS` document rather than replacing
it. `sparkwing cluster webhooks connect` drives both sides; see
[hooks.md](hooks.md).

## Logs service

Logs live in a separate service keyed by run and node
(`/api/v1/logs/{runID}/{nodeID}`), with a whole-run read and an SSE
stream for live tail. The routes and their scopes are in
[api-reference.md](api-reference.md).
On a node read, `grep=TEXT&line_numbers=1` returns NDJSON objects with
`line_no` (the position in the full node log) and `line`. Matching is
case-sensitive. `max_matches=N` caps the response; omitting it returns all
matches. Without `line_numbers=1`, the read returns plain text as before.

A runner numbers its appends: each carries `X-Sparkwing-Log-Stream`, a
name for the writer, and `X-Sparkwing-Log-Seq`, the line's position in
that stream from 1. When the node finishes, the runner seals each stream
with `POST /api/v1/logs/{runID}/{nodeID}/seal`, a JSON body naming the
stream, its `final_seq`, the `lines` and `bytes` it numbered, the lines
it `dropped` without delivering, and the `sha256` of everything it
numbered. A seal carries the same claim headers as an append, so only
the node's current claim holder can send one, and a seal into another
team's run answers `403`. Sealing a stream twice is a no-op.
`GET` on the same path returns what the service holds for the node's
streams; the dashboard's `GET /api/v1/runs/{id}/logs/{node}/completeness`
turns that into one verdict. See
[Log completeness](observability.md#log-completeness).

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
