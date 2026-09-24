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
`{ "kind": "binary", "key": "bins/<hash>" }` with a reader bearer token or a
run-scoped cache grant minted while its runner holds an exact trigger or node
claim. The controller checks the grant's claim generation and holder against
the live claim on each signing request. A grant from an earlier claimant or
one minted without a claim cannot sign. The controller also checks the live
run, team-owned key, stored object, and team's daily download allowance
before it returns `{ "url": "...", "sha256": "...", "size": 123,
"expires": "..." }`. It charges the recorded object size when it signs the
URL.

The URL expires after 60 seconds and names one exact object. A request that
reaches the controller through the in-cluster Service gets a regional S3
presigned URL; a request through the public ingress gets a CloudFront signed
URL. The controller selects the path from the presence of `X-Forwarded-For`
or `X-Forwarded-Host`. Configure the ingress to overwrite these headers. A
local filesystem store continues serving bytes directly through its existing
artifact route. Treat the returned URL as a temporary
bearer credential.

Binary clients discover this route through `GET /api/v1/services` and use it when the
controller announces it. The controller omits it on public ingress if CloudFront
signing is unavailable. Older controllers also omit it, so those clients
continue through the cache download route when it is absent. Uploads still go
to the cache. See [Tenant limits](limits.md) for the daily team allowance and
[Self-hosting](self-hosting.md) for CloudFront configuration.

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
