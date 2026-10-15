# W2 amendment integration handoff

<!-- design:begin -->

The merge's three conflicted file contents are resolved. Amended shapes are
retained in `c2-credentials.md`, `internal/apierr/codes.go` and
`pkg/controller/engine_contract.go`. The lead must stage them because the swarm
brief prohibits git writes. No git command changed the index or history.

## Implemented in this continuation

- Authentication and PolicyRouter keep one set of contract declarations and
  their existing implementations. The amended inner-error tests pass, including
  numeric details, duplicate keys, nulls, trailing input and additive fields.
- Claim selection refuses empty, duplicate and unknown kinds. Runner scans
  require each requested kind's grant. Launcher selection accepts plan/eval/node
  under its own service authority. Named trigger selection cannot select a job.
  Sync has no selection. Here authority reaches only its reserved run. Reclaim
  retains its first-claim window, expiry and requester ancestry.
- Job cancellation and failure reads register through PolicyRouter. Cancellation
  passes member or owner authority to TransitionNode without a terminal result.
  ReadFailure passes its recovery source, source generation and reader actor to
  C1's existing transaction. Invalid selectors are reported after authentication.
  An absent recorded failure returns null without synthesizing an error.
- Snapshot replies retain accepted expansion bodies, hashes and source fences
  from the same read. Replayed expansion replies retain each generated job's
  current state, attempt, claim, retry and disruption facts.

Removed 155 lines of duplicate declarations and contract panic stubs across the
authentication and policy files. Two additional engine panic bodies are replaced
by the job-control adapter. No legacy credential subsystem is claimed deleted.

## Amendment actions

| Rows | W2 result and remaining integration |
| --- | --- |
| A1-01, A1-05 | Node mask/annotation/summary policy rows and request contracts survive the merge. W3 still owns process service registration and all sink acknowledgements. Its current server has not adopted the shared PolicyRouter. |
| A1-03 | Complete shared codec. Both prior W2 tests and amendment tests pass. |
| A1-08, A1-09 | Complete C2 selection checks, including each kind's grant and separate launcher authority. Actual queued/named claim adapter and enrollment/version-policy wiring remain. |
| A1-11 | C2 preserves typed source location in the existing sealed intake/plan wire. W1/W3/W6 still own source authorization and execution. No caller-provided App trust is accepted by IntakeForStore. |
| A1-14, A1-15 | Complete C2 HTTP and transaction adapters with fake-backed proofs. C1 ReadFailure/TransitionNode bodies remain stubs in this integration. The cancellation-reason gap is below. |
| A1-16, A1-17 | Current execution projection and accepted-body transport are implemented for expansion/snapshot replies. Full reconciliation and private award delivery remain. |
| A1-19 | Server-granted runner identity still needs enrolled-profile and award delivery wiring. No labels or environment values are promoted to identity. |
| A1-20 | Existing root-only ProvisionSpec validation remains. Its authorizer is checked without becoming the new root's revocation parent. Ordinary and here ancestry are unchanged. |
| A1-21, A1-22 | Ratified authentication snapshot, selectors and exact-body evidence are retained with one implementation. Store.ReadAuthentication is still a stub. |
| A1-23 | Existing common router and registered engine tests pass. Complete controller/node-host route cutover and independent full registration comparison remain. |
| A1-24, A1-25, A1-26 | Transactional authority/binding/data adapters, plan-secret opening and enrolled claim profiles need W1 implementation and production wiring. C2 must consume these seams without its own SQL or issuer. |
| A1-27 | Existing billing policy/calculation tests pass. W1 must connect reservations, starts, finish/reap settlement and immutable payer/rate/executor facts. |
| A1-28 | Existing verified Stripe callback and removed grant-route proof pass in the acceptance suite. Production verifier/settlement wiring remains. No grant path was reintroduced. |
| A1-29, A1-30 | Bound record reads force VerifiedDefaultOnly for remote-cache authority and reject returned records without verified default-branch facts. Remote-cache bindings reject writes, binary and source-bundle record reads. W1 must enforce the same restriction in memo/depcache transactions and every digest read edge. W9's remote transport remains a stub. |
| A1-31, A1-32, A1-33, A1-34, A1-35 | DataController.BindData/DataAuthority and all bound signer/record operations are implemented with fake-backed acceptance proofs. Exact job/owner/publication identity is checked, reservation precedes signing, physical verification precedes receipt settlement, and staging deletion follows settlement. W1 must supply live authority/metadata reads and transaction bodies. W7 must supply ObjectBackend. W2/W9 must agree upload idempotency and finish the HTTP/client cutover before deleting the old handlers. |
| A1-38 | Sync/exchange entry points survive the merge. Their implementations and immutable private execution delivery remain. |
| A1-39, A1-40 | W8's unified runner/local-controller wiring still needs C2's remaining claim/authentication routes. No legacy loop was added here. |
| A1-49 | W6/W8/W9 own credential-file delivery and cleanup. W2's future enrollment/issuance cutover must use those contracts. |
| A1-53 | Native HTTP/api.sock, SQL race, placement and backend cells remain blocked. Fake-backed component proofs do not satisfy them. |
| A1-55 | Initial/reclaim liveness and named-only here authority have passing verifier/policy proofs. Claim endpoint and client response-loss behavior still need real integration. |

## Contract changes needed outside W2

- C1's NodeTransition/TransitionExtra has no cancellation-reason field. The
  adapter sends no NodeResult because that would confuse cancellation intent
  with a holder's result. The current job adapter cannot persist CancelRequest's
  reason. Add a C1 cancellation-intent field if reason persistence is required.
- ClaimAward supplies prefix, holder, generation and expiry. UnitClaim and
  ReconciledNode now carry the one-time bearer, but there is no complete atomic
  execution-delivery projection for the unit ID, granted runner, parent expiry,
  pinned verified engine and immutable source/deadlines. W1/W8/W9 must agree that
  read/projection boundary. Do not issue another bearer or invent these values
  in C2 after the claim transaction.
- Launcher sync and plan/eval renewal still need their store operations. Existing
  owner/job renewal methods cannot represent those cases. Result-body replay
  digests and annotation/summary attempt attribution retain the earlier handoff's
  transaction-contract questions.

No local contract signature was changed. RunTransactions now includes the
already-declared Tenant.ReadFailure method. Existing fake tenants were migrated.

## Verification and ports

The repository build, scoped vet, capability/API-error tests, executable
acceptance tests, controller policy/signing tests, daemon route inventories and
API generator tests pass. Focused docs symbol and mirror checks are run separately.
The full docs suite cannot bind its web-client test socket in this sandbox.
The lead retains independent review, Sparkwing pipelines, native integration and
the full legacy controller suite.

New proofs cover 14 claim-selection cases, eight named here cases, 44 job-control
HTTP cases, accepted expansion bodies and current execution state on expansion
replay. The cancellation body race changes the generation at the request read
boundary and checks that the original fence reaches the refusing transaction.
No additional full T1 scenario is unskipped and no complete legacy test port is
claimed in this continuation.

Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4,
GOCACHE=/tmp/sw-astra-W2/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp:

```sh
go build -p 4 ./...
go vet -p 4 ./internal/capability ./internal/apierr ./pkg/controller ./tests/acceptance ./internal/apiroutes ./internal/apispec
go test -p 4 ./internal/capability ./internal/apierr ./tests/acceptance
go test -p 4 ./pkg/controller -run '^TestPolicyRouter|^TestAuthorizedHandlersDoNotAuthorize$|^TestFilesystemSignedObjectBoundary$|^TestS3SignedObjectBoundary$'
go test -p 4 ./internal/orchestrator -run '^TestEveryControllerRouteIsClassified$|^TestReadRouteListCarriesNoDuplicates$'
go test -p 4 ./internal/apiroutes ./internal/apispec ./internal/apiref
go test -p 4 ./pkg/docs -run '^TestDocsNameOnlySymbolsThatExist$|^TestPkgDocsContentMatchesDocsRoot$|^TestEmbeddedChangelogMatchesRoot$'
```

Shared edits are `internal/orchestrator/wingd_api_routes_test.go` (two route
classifications), `internal/controllerwire/contracts.go` (attach the amended
intake-response GoDoc), `.apidiff/pkg_controller.txt`, `api/openapi.yaml`, API/auth docs,
changelog and generated mirrors. No other package caller changed. The source
schema now documents the amended snapshot field and the two new endpoints.

Friction was the merge's duplicate Go declarations, amended numeric error
details differing from the prior decoder, stale OpenAPI snapshot fields and the
sandbox's socket restriction. No new setting, environment variable, issuer,
service, cache or compatibility path was added. Temporary scratch is removed
after final checks. W2 remains incomplete.

## Data binding continuation

`DataAuthority` and `BindData` now implement the controller binding boundary.
Each operation resolves persisted authority again and checks C2 data policy.
The caller's authority and upload identity must match that resolved binding.
Job receipts retain the exact attempt/generation. Trigger and plan uploads use
their unit identity without inventing a job. Publication remains separate,
including imported job outputs. Owner and publication record-kind restrictions
follow the amended UploadRules.

Upload reserves quota and an immutable ticket before signing size and SHA-256.
A signing failure aborts the reservation and removes staging only after abort
succeeds. Commit rechecks authority before ticket lookup and physical publication.
ObjectBackend verifies bytes before CommitBlobUpload settles existence and the
receipt. A failed verification or settlement returns no receipt and preserves
staging for retry/retention. Cleanup errors after a committed receipt are reported,
so an exact commit replay remains necessary. Downloads sign only authorized live
metadata. HasMany omits missing and unreadable digests, deduplicates requests,
honors explicit budgets and propagates revocation and private failures.

Record creation checks the primary, manifest and member digests before the one
store commit. Memo/depcache lookups and memo copying preserve their existing
transaction inputs. Memo copies check source-ref scope and require the destination
to match the bound attempt. Remote-cache
bindings force verified-default RecordLookup, reject writes and reject binary
and source-bundle records. The shared policy's typed unreadable reason is handled
the same as its decoded JSON string in batched existence checks.

### Local contract additions and real integration

The W2-owned DataController contract adds ResolveTenant, ResolveAuthority,
ReadBlob and Access. DataTransactions is a subset of the existing Tenant upload
and RecordStore methods, with a compile-time Tenant assertion. No W1 or W7
signature changed. The two read callbacks are required because the ratified
contracts do not expose a controller-readable authority/metadata projection.
No SQL, store, issuer, service, credential cache or configuration option was added.

W1 must implement and supply these callbacks with committed facts. ResolveAuthority
must resolve the opaque reference again, including ancestry, membership, owner
or attempt liveness, sealed repo/ref scope, publication mode and imported results.
ReadBlob must return live blob size/digest without granting a read edge. Access
must enforce the live read set, remote-cache restrictions and applicable accounting.
The existing upload/record transactions retain their final authority rechecks.
W7 must provide its physical ObjectBackend implementation for both storage modes.

The local signer creates a fresh opaque upload ticket per Upload call and performs
no automatic retry. UploadIntent has no idempotency-key field, while the declared
HTTP upload route requires idempotency-key replay. W2/W9 must settle how that
route carries its key into reservation before wiring HTTP. No competing wire
body or implicit replay scheme was added here.

The legacy HTTP data paths remain because the contract readers, upload retry
mapping and W9's NewDataClient are incomplete. Their setup also serves the old
log/output paths through WithDirectUploads and WithSignedDownloads. Removing
that setup before migrating those consumers would break log transport. No old
credential subsystem or legacy test port is claimed complete in this continuation.
Only the two panic-stub bodies were replaced, removing two source lines.

The new acceptance file exercises the public DataController/DataBinding interfaces
with W1/W7 fakes. It proves adapter behavior, not SQLite/Postgres transactions,
HTTP/api.sock, filesystem/S3 bytes or any native TestStorage placement cell.
Those cells stay pending. Source documentation is this contract and handoff.
The controller API snapshot includes the local contract additions. No caller
outside W2 changed in this continuation.

The twelve new data-binding test functions pass. They include opaque session
references and a deterministic generation change after physical verification,
where settlement refuses and the adapter returns no receipt or staging deletion.
No complete legacy test port or native acceptance cell was activated.

Final verification passed with the environment and cache paths above:

- Whole-repository `go build -p 4 ./...`.
- Scoped `go vet -p 4` on capability, apierr, controller and acceptance.
- Full capability, apierr and executable acceptance tests.
- Focused controller policy-router, handler-policy, filesystem signer and S3 signer tests.
- API route/spec/reference tests, focused docs symbol/mirror/changelog checks,
  commentcheck against HEAD and `git diff --check`.

The generated controller API snapshot and changelog mirror are updated. Shared
changes in this continuation are `.apidiff/pkg_controller.txt`, `CHANGELOG.md`
and `pkg/docs/changelog.md`. The full legacy controller suite, independent review,
Sparkwing pipelines and native/backend integration remain with the lead.

## Job status continuation

`GET /api/v1/runs/{run}/jobs` and `GET /api/v1/runs/{run}/jobs/{job}` now
use the common PolicyRouter and C1's declared ReadSnapshot/ReadNode boundary.
The list uses one committed snapshot. The detail checks the exact returned
job reference. Empty lists return an array. Invalid or duplicate persisted
references fail before a partial response. Both serialize the existing safe
JobDetail type, excluding declarations, environment, claim holders/prefixes
and execution settings. Snapshot and list share the same run/team validation.

Members read status with runs.read. The live trigger owner reads its own run.
A job's data.read authority reaches its own detail only. Cancellation refuses
bound sensitive reads while ordinary member status reads remain available.
No grant, route-policy signature, clock, issuer or persistence implementation
was added. This advances A1-23's production route cutover. A1-53's native cells
and the other amendment rows retain the integration status recorded above.

Deleted 50 lines across the two legacy read handlers, route registrations and
tenant classifications. Their old GET paths no longer serve status. The other
legacy node operations remain for their own cutovers. The Go client status
methods now request the final paths and the collection's array response.
They unwrap canonical errors with the shared inner decoder. Missing jobs
return apierr.NotFound. Retry advice retains the existing behavior. Sizing
uses that same decoder rather than retaining another envelope reader.

The existing client methods retain the store.Node return type because their
backend and legacy runtime consumers still use it. Only the safe status
subset is returned. No private execution data is reconstructed, and no old
route/error fallback was added. W9 still owns replacing that client with the
shared controllerwire client. W3/W8's old runtime consumers must use private
awards and snapshots wherever they need execution or placement facts.

New tests exercise the actual controller Handler with C1 authentication/read
fakes. They cover member/owner/job positives, sibling-list refusal, audience
and ancestry/grant/fence failures, foreign persisted references, duplicate
rows, terminal status, empty arrays, HEAD refusal and removal of old reads.
Socket-free client tests cover the final paths, escaped selectors, array
decoding, canonical errors, malformed errors without input disclosure and
retry advice. No full T1 scenario or complete legacy test port is claimed.

The remaining RunDetail stub also needs a complete read projection: the
declared RunSnapshot/RunRow carry no attempt history, log subjects or accepted
engine identity. C2 cannot manufacture those values. W1/W9 must supply that
read contract before the full local/remote status view can be implemented.
The claim-delivery, sync, exchange, plan/eval renewal and upload-idempotency
contract questions above remain open. No cross-area contract was changed.

Shared implementation edits in this continuation are
`pkg/controller/client/client.go` and `internal/orchestrator/wingd_api.go`.
The new client test is `pkg/controller/client/jobs_read_test.go`.
Source docs, migration/changelog, OpenAPI and generated mirrors are aligned.
`perf/budgets.json` records the missing authorized performance corpus without
claiming a measurement. W1's read methods remain stubs and real HTTP/api.sock,
backend/SQL integration, full legacy controller tests, independent review and
Sparkwing pipelines remain for the lead.

Verification uses the same isolated environment described above. The initial
HTTP and client cases failed on the absent routes and old wire. Final checks
passed:

- Whole-repository `go build -p 4 ./...`.
- `go vet -p 4` on capability, apierr, controller, controller/client,
  acceptance, apiroutes and apispec.
- Full capability, apierr and executable acceptance tests. The added job
  status cases also pass after the audience/bootstrap/terminal additions.
- Client `TestJobStatus`, `TestSizing` and `TestParseRetryAfter` cases.
- Controller policy-router, authorized-handler and filesystem/S3 signer tests.
- Daemon route inventory, API route/spec/reference and docs symbol/mirror checks.
- Changed-line commentcheck and `git diff --check`.

Reproduce the new boundary cases with `go test -p 4 ./tests/acceptance -run
'^TestEngineJobs'` and `go test -p 4 ./pkg/controller/client -run
'^TestJobStatus|^TestSizing|^TestParseRetryAfter'`, using the Go environment
and scratch paths above. Native T1 cells and complete legacy ports remain
pending. No performance measurement was taken.

No Git writes or Sparkwing binaries were used. The three merge conflict file
contents were already resolved on entry and remain resolved. The lead must
stage them to clear the index's unmerged entries.

## Credit ledger continuation

Balance and history reads now register through PolicyRouter. Both require
member or verified local runs.read authority. The active team comes from the
committed credential. A selected CreditReader must report that same team.
Machine roots, work/trigger claims, bootstrap delivery, publication credentials
and deployment authority cannot borrow runs.read to read the team ledger.
The successful response fields and existing history cap remain unchanged.

The license check for these two paths runs after authentication and authorization.
A live member receives canonical not_found when billing is unavailable. Missing
or dead credentials receive unauthenticated. History validates its limit before
resolving persistence and writes only after both ledger reads succeed. A charge
read failure cannot disclose the grants fetched before it.

The local C2 addition is CreditReader and Server.WithCreditReader. The reader is
a subset of existing Tenant methods, including Team. The default uses ForTeam.
The resolver supplies the same interface to tests and embedded controllers.
This is an injection seam because C1's authentication implementation is absent.
It adds no SQL, issuer, credential cache, setting, limit or service. Removing
the resolver and its fixtures undoes the seam. No other area's contract changes.

Removed 17 lines of retired scaffolding: the eight-line legacy balance wrapper,
two old route registrations, two old credit classifications and five stale
/profile classifications. Both existing ledger handlers are rewritten in place.
The stale classifications were found by the actual mux inventory test after
the earlier sizing cutover. The new registrations are included in API generation.

The new acceptance tests cover the actual Server.Handler with authentication and
ledger fakes. They prove positive bearer/session/verified-peer reads, team-hint
refusal, unknown/dead and unrelated authority refusals, availability ordering,
selector validation, explicit empty arrays, no partial responses, unavailable or
foreign readers, canonical private-error handling and exact-method refusal.
All eight new test functions and their 62 subtests pass without skips.
No full T1 scenario or parked test port is claimed complete. A1-23 advances by
two production registrations. All other amendment rows retain the status above.

### Real store integration still open

A separate current-schema SQLite probe failed before returning a balance.
Tenant.CreditState returns `SQL logic error: no such table: sparkwing_meta (1)`.
Tenant.ListCreditCharges returns `SQL logic error: no such column: token_prefix (1)`.
The final schema calls that column capability_prefix. W1 owns migrating both
read implementations to the final settings/schema. The probe created two teams,
seeded distinct paid grants through their tenants, then called these existing
read methods and the actual controller. Both methods failed with the errors
above and the controller returned canonical internal without private SQL text.
The probe was removed from the fake-backed component suite. This is failed real
integration evidence, not a passing native acceptance cell or a reason to add an
old-schema fallback. Grant reads alone do not establish a working ledger view.

The existing authentication, binding, claim delivery, sync/exchange, plan/eval
renewal and data-upload idempotency integrations remain open as recorded above.
The whole old credential subsystem is not deleted. Credit settings and purchase
routes still need their own authorization cutovers.

### Verification and shared files

Commands use the brief's GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
-p 4 and /tmp/sw-astra-W2 cache/temp isolation. These final component checks passed:

```sh
go build -p 4 ./...
go vet -p 4 ./internal/capability ./internal/apierr ./pkg/controller ./tests/acceptance ./internal/apiroutes ./internal/apispec
go test -p 4 ./internal/capability ./internal/apierr ./tests/acceptance ./internal/apiroutes ./internal/apispec ./internal/apiref
go test -p 4 ./pkg/controller -run '^TestPolicyRouter|^TestAuthorizedHandlersDoNotAuthorize$|^TestFilesystemSignedObjectBoundary$|^TestS3SignedObjectBoundary$|^TestTenantRoutes_'
go test -p 4 ./internal/orchestrator -run '^TestEveryControllerRouteIsClassified$|^TestReadRouteListCarriesNoDuplicates$'
go test -p 4 ./pkg/docs -run '^TestDocsNameOnlySymbolsThatExist$|^TestPkgDocsContentMatchesDocsRoot$|^TestEmbeddedChangelogMatchesRoot$'
go run ./internal/commentcheck -base HEAD .
```

The broader TestRoute selection could not finish because its content-type test
binds a TCP socket, which this sandbox refuses. Native HTTP/api.sock, live SQL
integration, full legacy controller tests, performance measurements, independent
review and Sparkwing pipelines remain with the lead. Performance entries state
that authorized ledger timings and response bytes are unmeasured. The API snapshot
is generated for the controller package only. Other package snapshots stay with
their owners.

Shared edits in this continuation are internal/apiroutes/apiroutes.go and its
regression test, .apidiff/pkg_controller.txt, perf/budgets.json, auth/migration
and API-reference documentation, the changelog and generated mirrors. No caller
outside W2 changed. The contract text records the local reader interface.

No Git writes or Sparkwing binaries were used. Merge file contents already had
no conflict markers on entry. The lead must stage the three unmerged index entries.
Scratch is removed after verification. W2 remains incomplete.

## Queue read continuation

`GET /api/v1/queue` uses PolicyRouter and the existing member `runs.read`
row. Tenant selection comes from the committed credential. The reader must
report that same team. Group, holder, waiter and runner-headroom projections
retain the existing response fields. Private read failures and malformed
persisted groups return the common error before any partial queue response.
Session and verified-peer requests use the same verifier.

The local C2 addition is QueueReader and Server.WithQueueReader. Its two
methods are a subset of Tenant's existing API. Store.ForTeam remains the
default. This seam supplies fake-backed HTTP evidence while the real C1
authentication adapter is a stub. Removing the resolver and fixtures reverses
the addition. No C1 signature, store logic, issuer, configuration key or
credential cache changed.

The old queue registration and tenant classification are deleted. The
56-line socket fixture is replaced by the acceptance projection test. The
runner-isolation test now uses the same tenant contract without SQL setup.
The frontend route exception is deleted because the controller serves the
final path. These cuts remove 94 lines of handler/test scaffolding and old
registrations. No legacy credential subsystem is claimed deleted.

Five queue HTTP test functions and 29 subtests pass. They cover effective
capacity, superseded holders, waiter positions, member/session/verified-peer
reads, ignored team selectors, missing/dead/foreign authority, reader team
mismatch, absent dependencies, canonical errors, private/partial read failures,
method refusal and removal of the old path. The separate runner test preserves
the two-team headroom proof. These are component proofs. No full T1 cell or
parked credential test port is completed.

Shared implementation changes are cmd/sparkwing/queue.go and
web/src/lib/api.ts (final route callers), internal/apiroutes/apiroutes.go and
its test (registration discovery), and internal/orchestrator/wingd_api.go and
its inventory test (read classification). Documentation, changelog, OpenAPI,
the controller API snapshot, performance manifest and generated mirrors follow
the cut. Other API snapshots are untouched. Authorized queue latency, cold
start and response bytes remain unmeasured because the required tenant corpus
is absent from the dashboard performance fixture.

The repository build, scoped vet, full capability/API-error/executable
acceptance suites, policy-router/signing checks, queue runner-isolation proof,
frontend route inventory, daemon route inventory, API route/spec/reference
checks, docs symbols/mirrors, CLI queue rendering, changed-line commentcheck
and git diff --check pass. Use the isolation variables already listed above:

```sh
go build -p 4 ./...
go vet -p 4 ./internal/capability ./internal/apierr ./pkg/controller ./tests/acceptance ./internal/apiroutes ./cmd/sparkwing
go test -p 4 ./internal/capability ./internal/apierr ./tests/acceptance ./internal/apiroutes
go test -p 4 ./tests/acceptance -run '^TestQueueRead' -count=1
go test -p 4 ./pkg/controller -run '^TestPolicyRouter|^TestAuthorizedHandlersDoNotAuthorize$|^TestFilesystemSignedObjectBoundary$|^TestS3SignedObjectBoundary$|^TestQueueStateShowsOnlyTheCallersTeamsRunners$|^TestEveryFrontendAPICallIsAControllerRoute$'
go test -p 4 ./internal/orchestrator -run '^TestEveryControllerRouteIsClassified$|^TestReadRouteListCarriesNoDuplicates$'
go test -p 4 ./internal/apispec ./internal/apiref ./pkg/docs -run '^TestGenerator|^TestEveryRegistered|^TestCommittedSpec|^TestSchema|^TestRewrite|^TestWriteRoutes|^TestDocsNameOnlySymbolsThatExist$|^TestPkgDocsContentMatchesDocsRoot$|^TestEmbeddedChangelogMatchesRoot$'
go test -p 4 ./cmd/sparkwing -run '^TestRenderQueue_'
```

The initial queue case failed on the absent route. API generation rejected a
duplicated grant in schema prose, which was removed because the generator
supplies the route's scope. Native HTTP/api.sock, real SQL/backend races, full
legacy controller tests, performance measurements, independent review and
Sparkwing pipelines remain with the lead. The claim delivery, reconciliation,
sync/exchange, plan/eval renewal, run-detail and upload-idempotency contract
gaps above remain open. The other A1 row dispositions are unchanged.

All three merge-file contents already lacked conflict markers on entry and
retain the amended declarations. The index still lists them unmerged because
the brief prohibits Git writes. The lead must stage them. Installed Sparkwing
discovery ran once during bootstrap before the brief's binary prohibition was
read. All subsequent verification uses Go tools. No candidate installation or
Git write ran. Scratch is deleted after verification. W2 remains incomplete.

## Run detail continuation

The worktree was clean on entry. No merge remains unresolved. The earlier
queue cutover and prior W2 work are committed. This continuation makes no Git
writes and runs no Sparkwing binary.

`GET /api/v1/runs/{run}` now uses PolicyRouter and returns
controllerwire.RunDetail. DisplayRequest/DisplaySpec own argument redaction.
Members, live bound claims, sessions and verified local peers share that
projection. The include query cannot reveal private execution values. The
response preserves safe job status, complete attempt history, tagged log
subjects, the accepted portable engine and verified policy notices.

The invariant is that an authorized status read contains only display-safe
inputs and status facts from its resolved run. It contains no execution seal,
private hash, holder, capability prefix or secret argument value. Missing or
invalid classification hides every argument value, including on terminal runs.
The second invariant confines nested jobs, attempts and log subjects to the
resolved team/run and recorded attempt identities. Both have generative fuzz
tests through the actual registered HTTP route.

Deleted 377 lines of legacy detail helpers and tests: 122 in handlers.go,
85 in run_detail_decorations_test.go and 170 in secret_args_display_test.go.
The old registration, tenant classification and panic stub are also removed.
The retired helpers include the unused plaintext/admin/loopback gate.

Six new test functions cover 45 HTTP subtests plus missing-reader and empty
status cases. Two fuzz targets cover private projection and nested scope.
They replace the old detail-redaction assertion, reverse the unclassified
plaintext assertion, drop grandfathering, and remove the execution-view and
auth-disabled plaintext expectations. The old embedded decoration-wrapper
test is removed. Approval/step/child resources remain separate reads. These
are component ports, not completion of the native display or route-matrix
scenarios. No native T1 scenario is newly unskipped.

### Contract additions and remaining integration

W2 adds RunStatusFacts and EngineController.ReadRunStatus using existing
store, blob and engine types. Missing wiring returns policy_missing.
RunSnapshot alone has neither complete attempt history nor log subjects, and
separate reads would lose consistency. The chosen seam avoids inventing an
attempt from each current job or silently returning empty history. Replace the
callback with the complete C1 read when that contract exists.

No C1 or C3 declaration changed. C1 still needs a consistent status-read
contract returning the snapshot, complete NodeAttempt rows and validated
LogSubject rows. Its adapter must apply LogSubject.Validate and return only
the tenant's persisted facts. W8 must supply notices from verified engine
policy. W9 must migrate the old Client.GetRun consumers and the dashboard's
former raw/wrapped response to RunDetail. GetRunForExecution remains in the
legacy client and is no longer an execution-input path. Its consumers in
internal/orchestrator/run_node.go, run_for_execution.go and
secret_args_execution_test.go must move to the private award wire. W2 does
not return redacted status arguments as execution inputs or add a forwarding
compatibility endpoint.

The inherited claim-delivery/reconciliation, launcher sync/exchange,
plan/eval renewal, source/secret/log/data cutovers and upload-idempotency gaps
remain. The complete controller/node-host registration matrix, production
authentication/binding adapters and native transport/SQL/backend cells remain
open. No other area's contract was changed.

### Shared files and verification

Shared implementation edits are internal/orchestrator/wingd_api.go (the final
read-route pattern), internal/apispec/rewrite.go, spec_test.go and
path_parameters_test.go. The API generator now recognizes equivalent path
parameter names across methods, seeds missing methods on the existing path
and refuses duplicate equivalent paths. This keeps the new GET and legacy
DELETE on one valid OpenAPI resource without changing runtime routing.
No other package's Go caller changed because no consumed Go signature was
removed. The outstanding wire-consumer migrations are listed above.

The remaining shared edits are api/openapi.yaml, docs/api-reference.md,
docs/api.md, docs/auth.md, docs/migrations/_unreleased.md, CHANGELOG.md,
perf/budgets.json, .apidiff/pkg_controller.txt and generated docs mirrors.
Other API snapshots are unchanged. Authorized latency, cold start and response
bytes remain unmeasured because the committed status corpus and adapter are
absent from the dashboard performance fixture.

The initial regression failed at the RunDetail panic. Whole-repository build,
scoped vet, full executable acceptance/capability/API-error suites, API generator
and schema suites, focused controller policy/signer/tenant checks, daemon route
inventories, docs symbols/mirrors and changed-line comment policy pass. Both
fuzz targets pass generated-input runs with two workers. Native SQL/transport,
the legacy controller suite, independent review and Sparkwing pipelines remain
with the lead. The scratch directory is removed after verification.

Use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4,
GOCACHE=/tmp/sw-astra-W2/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp:

```sh
go build -p 4 ./...
go vet -p 4 ./internal/capability ./internal/apierr ./pkg/controller ./tests/acceptance ./internal/apispec ./internal/apiroutes
go test -p 4 ./tests/acceptance ./internal/capability ./internal/apierr ./internal/apispec ./internal/apiroutes ./internal/apiref
go test -p 4 ./tests/acceptance -run '^TestEngineRunDetail|^FuzzEngineRunDetail' -count=1
go test -p 4 ./tests/acceptance -run '^$' -fuzz '^FuzzEngineRunDetailPrivateProjection$' -fuzztime=5s -parallel=2
go test -p 4 ./tests/acceptance -run '^$' -fuzz '^FuzzEngineRunDetailScope$' -fuzztime=5s -parallel=2
go test -p 4 ./pkg/controller -run '^TestPolicyRouter|^TestAuthorizedHandlersDoNotAuthorize$|^TestTenantRoutes|^TestFilesystemSignedObjectBoundary$|^TestS3SignedObjectBoundary$|^TestEveryFrontendAPICallIsAControllerRoute$'
go test -p 4 ./internal/orchestrator -run '^TestEveryControllerRouteIsClassified$|^TestReadRouteListCarriesNoDuplicates$'
go test -p 4 ./pkg/docs -run '^TestDocsNameOnlySymbolsThatExist$|^TestPkgDocsContentMatchesDocsRoot$|^TestEmbeddedChangelogMatchesRoot$'
go run ./internal/commentcheck -base HEAD /home/bombuntu/.xwing/envs/1080/sparkwing
```

Friction was the absent consistent status-read contract, legacy clients that
still expect execution values from the display endpoint, and the API generator's
assumption that equivalent path parameters have identical names. No new issuer,
service, configuration setting, environment variable or credential cache was
added. W2 remains incomplete.

<!-- design:end -->
