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

## Webhooks

`POST /webhooks/github/{pipeline}` ingests GitHub deliveries. It is
verified by HMAC (`X-Hub-Signature-256`) rather than a bearer token,
since GitHub can't carry one; the handler acts on `push` and
`pull_request` (opened/synchronize/reopened) and answers `ping`. A
delivery naming a repository the pipeline is not bound to answers `404`,
re-sending a body the controller already accepted answers `409` with the
run the first delivery produced, and a delivery with no
`X-GitHub-Delivery` header answers `400`. See [security.md](security.md).

## Logs service

Logs live in a separate service keyed by run and node
(`/api/v1/logs/{runID}/{nodeID}`), with a whole-run read and an SSE
stream for live tail. The routes and their scopes are in
[api-reference.md](api-reference.md).

## Run coordination

A pipeline binary needs more than node state from whatever holds its
runs: it dispatches its own child triggers, and it measures what the run
cost so the next run of the same pipeline is priced from evidence. Those
reach the controller as routes too -- `/api/v1/triggers/pending-for-parent`
and `/api/v1/triggers/{id}/claim` for the child-trigger loop,
`/api/v1/pipelines/{name}/profile/observations`, `/contention`, and
`/waits` for the capacity profile, `/api/v1/runs/{id}/nodes/{nodeID}/usage`
for a reaped process's accounting, and
`/api/v1/maintenance/reconcile-orphans` for the sweep that closes runs
whose orchestrator died. A run that talks to a controller therefore needs
no database handle of its own.

## Concurrency

The `.Memoize()` and `.Concurrency()` coordination primitives are backed
by the `/api/v1/concurrency/{key}/*` routes (acquire, heartbeat,
release, state, resolve). See [caching.md](caching.md) for the model.
