# W2 data HTTP continuation

<!-- design:begin -->

## Implemented

`POST /api/v1/data/upload`, `POST /api/v1/data/commit` and both methods on
`/api/v1/data/records` use PolicyRouter and the existing DataController bindings.
Upload takes a digest and explicit nonnegative size. The controller derives
uploader identity and scope. Commit accepts only the ticket and returns public
receipt fields after physical verification and transactional settlement.

Record writes derive team, repository, ref and uploader from the authenticated
binding. Member team and manifest identity come from that same projection.
Every primary, manifest and member digest passes the edge checker before the
record transaction. Record lookup returns public record/member metadata and
omits private authority, uploader, source and read-set snapshots. Remote-cache
lookups force verified-default-only even when the query requests false.

The invariant is that caller-supplied selectors cannot change the authenticated
team, uploader, attempt or reference authority. A fuzz test generates arbitrary
foreign primary, manifest and member digests and proves that none reaches the
record committing transaction. Its five-second run completed 896 executions.
HTTP tests also cover missing/revoked/ended/stale authority, cancellation, bare
machine roots, malformed requests, quota failure, signing cleanup, integrity
failure and revocation after physical verification. Private backend errors do
not reach responses. Empty uploads are allowed by the nonnegative-size contract.

Discovery checks the final authenticated adapters. Configuring the older S3
output/log backend alone does not advertise data uploads. API schemas,
references, source docs, mirrors and the controller API snapshot are updated.
The performance manifest records unmeasured authorized data routes.

The local C2 wire declarations are in `c2-credentials.md` and OpenAPI. No other
area's contract changed. No issuer, credential store, environment variable,
customer limit or service was added.

## Deleted and retained code

Deleted 356 legacy production/type/registration lines across `direct_upload.go`
(302), `data_download.go` (52) and the two raw route registrations. These include the swcg upload authenticator,
pretrigger user-token authenticator, caller-selected object-key requests,
handlers and DirectUploadResponse. No production Go caller used the removed
response type. The unused cacheScope and verifyLiveDataGrant methods and their
test-only exports are removed. The remaining direct-upload backend serves legacy output/log
callers and still needs their later cutover.

Removed 1,214 obsolete test/helper lines across `direct_upload_test.go`,
`direct_source_test.go`, `direct_upload_s3_integration_test.go` and the first
two tests of `data_download_test.go`, `cache_ref_internal_test.go`,
`claim_source_test.go` and `export_test.go`. The build-trust test is retained in
`build_trust_test.go`. The older cache-grant tests are retained with the
credential subsystem that still calls them.

The final HTTP tests port the authorization cases behind live-claim upload,
raw-runner refusal, stale same-root reclaim, server-selected ref scope,
failed-signing cleanup and corrupt-transfer refusal. The physical interrupted
S3 commit and existing-object checksum tests are not claimed as fully ported.
Their final native storage acceptance remains open with W7/T1. The pretrigger
source upload and empty-object-refusal behavior are removed by the final
capability/uploader and nonnegative-size contracts.

## Amendment dispositions

The earlier row details remain in `W2-amendments-1-handoff.md`. No entry in the
current `contracts/handoffs.md` names W2. These are the remaining W2 groups.

| Rows | State after this continuation |
| --- | --- |
| A1-01/03/05 | Existing mask/report policy and inner error decoder remain. W3's production process service registration is still required. |
| A1-08/09/19/26/55 | Claim routing remains implemented. Production runner/version/identity and here transports remain with W3/W8/W9. |
| A1-11/16/17 | Existing private award and reconciliation projections remain. Consumer cutovers remain with W3/W8/W9. |
| A1-14/15 | Failure reads and holder-preserving cancellation remain implemented. C1 cancellation-reason persistence remains open. |
| A1-20/21/22/23 | Existing issuance, store snapshots and policy router remain. Session/CSRF, peer/console, root provisioning and the remaining controller routes still need their cutovers. |
| A1-24/25/27/28 | Existing verified payment path remains. Source audit/consent, PlanSecretOpener wiring, transactional console rechecks and production payment configuration remain. |
| A1-29/30/31/32/33/34/35 | Generic upload, commit and record HTTP cutover is implemented against the declared interfaces and tested with fakes. Authority/metadata/charging adapters, specialized cache transport and physical backend integration remain below. |
| A1-38 | Existing fake-backed launcher sync remains. C1's transactional final snapshot is still needed. |
| A1-39/40/49/53 | Runner/local-controller/file startup and native topologies remain with their owners and T1/lead. No complete native cell is unskipped. |

## Contract changes needed and real integration

- C1 must expose the persisted data-authority projection needed by
  DataController.ResolveAuthority, plus live blob metadata and charging adapters.
  Its private resolveBlobAuthorityTx already owns these facts. W2 added no SQL
  copy or caller-vouched trust. W1 must keep transaction ownership.
- C1's DataAccessChecker must handle declared selectors without requiring an
  already-known digest. W2 sends DataCommit with UploadID before ticket lookup,
  and DataLookup with logical record kind/scope/name. In this tree,
  checkBlobDataWriteTx rejects the first because Digest is empty, and the
  DataLookup branch calls readableBlobsTx with an empty digest. This is source
  inspection, not a real-store HTTP pass. Initial and committing transaction
  checks must continue to enforce authority.
- W7 must implement the physical ObjectBackend and supply the filesystem byte
  transfer seam. Sign/CommitObject/DeleteObject alone do not expose filesystem
  upload/download streaming. The final signed-proof routes still need binding
  to W7's byte owner. No W2 filesystem implementation was duplicated.
- W9's NewDataClient remains a stub. Generic record HTTP is connected, but
  specialized LookupMemo/LookupDepcache/CopyMemo transport still needs a shared
  wire choice with C2. Preserve authorized members and server-assigned trust
  facts while keeping private authority snapshots out of responses.
- `cmd/sparkwing-controller/main.go` still configures WithDirectUploads rather
  than WithDataController. `internal/directdata/client.go` still sends the
  retired key/run/cache-grant bodies, and bincache/orchestrator consumers still
  use it. W3/W7/W9 must replace those consumers through the declared byte and
  record interfaces. Their Go callers still compile. No compatibility route
  or request decoder was retained.
- Earlier C1 launcher snapshot, cancellation-intent reason and authority-bearing
  operator mutation requests remain open. Earlier source-release and
  session/CSRF/console integration work remains open.

There is no new product-policy question. W2 is incomplete. The next W2 work is
the remaining trusted adapters and credential/controller hard cut, followed by
specialized cache HTTP integration and native acceptance.

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W2/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp.
Tests used at most `-p 4`. The scratch directory is removed at the end.

| Check | Result |
| --- | --- |
| `go build -p 4 ./...` | Pass on the final production tree. |
| `go vet -p 4` on capability, apierr, controller, acceptance, apiroutes, apispec and apiref packages | Pass. |
| Full capability/API-error/executable acceptance/API-route/schema/reference tests | Pass. Native pending scenarios still skip. |
| New data HTTP tests, including the final remote-cache and size cases | Pass. |
| Focused engine/policy/operator/storage/signer tests | Pass with the known C1 cancellation-reason test excluded. |
| Orchestrator route inventory and docs symbol/mirror tests | Pass. |
| Data-edge fuzz, five seconds, two workers | Pass, 896 executions. |
| API/reference/snapshot regeneration and `git diff --check` | Pass. |
| `go run ./internal/commentcheck -base main .` | Fails only on incoming W1 comments at state_materialize.go:15 and state_result_records.go:9. No W2 comment is reported. |

The new HTTP tests ran red against the old direct handler and absent record
routes, then green after the cutover. A broad acceptance run exposed discovery
fixtures still configuring the old backend. Those fixtures were migrated and
the full executable suite passed. No native S3/filesystem transfer, Postgres,
topology, Sparkwing pipeline, daemon or product binary ran. The lead owns the
independent review and gates. Broad legacy credential tests remain outside the
passing selection because their token-table fixtures still need retirement.

Shared edits outside the controller implementation are
`tests/acceptance/data_writes_http_test.go`,
`tests/acceptance/data_reads_http_test.go`,
`tests/acceptance/discovery_http_test.go`,
`internal/orchestrator/wingd_api_routes_test.go`, `perf/budgets.json`,
`api/openapi.yaml`, `docs/data-uploads.md`, `docs/api-reference.md`,
`docs/migrations/_unreleased.md`, `CHANGELOG.md`, their generated mirrors and
`.apidiff/pkg_controller.txt`. No production caller in another package needed
a compile migration in this continuation.

Friction was the unimplemented production data projections, the selector/checker
mismatch, the missing physical backend/transfer seam and stale discovery
fixtures. No Git write ran. Git identity remains Korey's default and no
core.worktree override was introduced.

<!-- design:end -->
