# Controller HTTP API

The CLI, runners, fetched engines and dashboard use `/api/v1`. The local
controller exposes the same messages and authorization policy over its private API
socket. The [HTTP reference](api-reference.md) is generated from the shared
capability table. It includes node-host and external-proof routes with their
separate audiences.

The repository's `api/openapi.yaml` is generated from the same policy and the
shared wire types. Each operation retains its complete authorization alternatives
under `x-sparkwing-policy`. JSON schemas preserve omitted fields, nulls and byte
encoding. Shared validators enforce constraints beyond those serialization shapes.
Operations without a documented success body carry `x-sparkwing-wire: undocumented`.
They do not inherit a body from a removed endpoint.

This page describes the frozen wire contract. Native controller registration,
transactional authorization and signed storage still require integration. Client
transport fixtures establish message handling, not those server guarantees.

<!-- design:begin -->

## Authentication

A route requires one complete access alternative: credential class, every listed
grant, resource binding and liveness. Alternatives cannot supply one another's
missing checks. Ownership comes from a server lookup. A request cannot choose its
actor, granted runner identity, billing provenance or claim fence.

Capabilities, browser sessions and local peers pass the same applicable resource
checks. Operator-console sessions and deployment-admin capabilities have separate
powers. See [Authentication](auth.md) and [Security](security.md).

External routes name their own proof: GitHub webhook HMAC, payment signature,
GitHub Actions OIDC, browser CSRF/nonce or exact signed-object URL. A missing or
invalid credential returns 401. A live credential without the route's authority
returns 403. Resource conflicts use their canonical code, including `lock_held`.

## Errors and retries

HTTP errors use one envelope:

```json
{"error":{"code":"lock_held","message":"another holder owns this claim","hint":"wait for its lease"}}
```

The inner object requires a nonempty code and a string message. Optional `hint`
and `details` retain their declared string and object types. Clients accept
unknown additive fields and future codes. They reject duplicate keys, null
required fields, malformed bodies and nested envelopes without echoing the body.

The route's retry policy governs the shared HTTP/socket transport. Exact-body
retries preserve the body. Keyed intake keeps its idempotency key and body.
Claims, named claims and reconciliation can return a fresh bearer once, so a lost
response is never automatically replayed. Here intake and retry also suppress
replay. Lease recovery resolves an uncertain claim. A lost initial here bearer
requires a fresh trigger request. See [Here runners](hotfix-runner.md).

## Intake, execution and display

| Operation | Request | Response |
| --- | --- | --- |
| `POST /api/v1/runs` | `CreateRunRequest` | `CreateRunResponse` |
| `POST /api/v1/runners/claim` | `ClaimRequest` | `ClaimResponse` |
| `POST /api/v1/launcher/claim` | `ClaimRequest` | `ClaimResponse` |
| `POST /api/v1/runs/{run}/claim` | Named trigger `ClaimRequest` | `ClaimResponse` |
| `POST /api/v1/runs/{run}/plan` | `PlanSubmission` | `PlanAccepted` |
| `GET /api/v1/runs/{run}/snapshot` | Run selector | `ScheduleSnapshot` |
| `POST /api/v1/runs/{run}/jobs/{job}/reconcile` | `ReconcileRequest` | `ReconcileResponse` |
| `POST /api/v1/runs/{run}/jobs/{job}/transition` | `JobTransitionRequest` | `Applied` |
| `POST /api/v1/runs/{run}/jobs/{job}/expansion` | `ExpansionSubmission` | `ExpansionAccepted` |
| `POST /api/v1/runs/{run}/finish` | `FinishRequest` | `Applied` |
| `POST /api/v1/runs/{run}/retry` | `RetryRequest` | `CreateRunResponse` |
| `GET /api/v1/runs/{run}` | Run ID or team number | `RunDetail` |

The wire types live in `internal/controllerwire`. Intake seals requested inputs
once. Acceptance seals checkout declarations, resolved settings and the validated
plan together. Retry retains the original request, source authority, SHA and
accepted seals, with the retrier recorded separately. It never rewrites the
original seal's identity to match the new run ID.

Private claim and reconciliation responses carry execution settings, fences,
accepted inputs and fresh capabilities. Public `RunDetail` and `JobDetail` carry
safe display projections. Status clients never substitute a raw execution seal
for a display record.

A snapshot is one consistent scheduling read. Generator replay uses the exact
accepted body and source fence, including empty expansions. The persisted topology
stays authoritative. Held jobs receive a cancellation request while their current
holder retains result rights. A result replay compares the actor, operation,
fence and body digest. Source-attempt fencing remains an integration requirement.

A reported job result carries `failure: {job, stage, error}` when it has a
structured failure. The inner error retains its code, message, hint and details.
Recovery reads that same persisted failure. An outcome or reason cannot replace
its stage. The error-only result field is removed.

## Logs and stored data

Run logs use `GET /api/v1/runs/{run}/logs`. Job reads, streams and completeness
use `/api/v1/runs/{run}/jobs/{job}/logs`, `/logs/stream` and
`/logs/completeness`. Job segment and manifest writes use the matching
`/log-segments` and `/log-manifest` routes. Log subjects distinguish job attempts
from trigger, plan and eval units. A final log-tail exception cannot reopen work
or write another attempt's logs.

An interrupted or incomplete observation remains visible independently of the
recorded run verdict. The CLI returns exit 3 when it cannot establish a complete
observation. See [Observability](observability.md).

Blob transfers and logical memo/depcache records use the authenticated `/data`
routes. Storage authorization rechecks the live capability, bound uploader and
persisted read edges. Object requests receive only signed transfer headers.
See [Direct data uploads](data-uploads.md).

## Version policy

A repository's exact SDK pin selects its verified engine. From launch freeze,
the controller supports requests emitted by each engine for twelve calendar
months after its release. Older engines continue until a signed security
revocation or minimum-version policy takes effect. Additive fields and codes
remain readable. Each selected engine owns its matching node protocol.
See [Versioning](versioning.md).

<!-- design:end -->
