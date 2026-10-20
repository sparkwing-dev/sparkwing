# W2 production serving handlers

This slice implements 18 of the 23 missing registrations. The separate resume worker supplies resume, leaving four missing routes in both metrics configurations. No policies or features were removed to make the inventory pass.

## Implemented

- Native job attempts, steps, activity and debug-pause handlers. Attempts omit holder and capability-prefix fields. Activity rechecks the current writer at commit.
- Named job claims use the shared claim operation. Decoding refuses path/body disagreement and any unit kind other than node.
- Six job/owner log submission routes derive subjects from authorized unit/attempt state. The request must match exactly. Signing reads sealed retention under the store's writer/fence predicate. S3 signing checks W7's actual data-bucket lifecycle. Commits verify compressed bytes, digest and decoded size before RecordSegments rechecks authority. Incremental commits verify their supplied byte range. Live job pushes reuse the existing ring attempt identity. Owner subjects have distinct generated keys.
- Pools expose server-granted reservations and canonical repository rules. Writes enumerate repositories from the tenant's bound GitHub App installation, then recheck the live installation and current administration at commit. An idempotency key derives the rule identity. Changed selectors under that key fail. Rule deletion and runner root revocation recheck administration in their transactions.
- source=local selects local-origin runs of independently verified canonical GitHub repositories. The frozen pool table forbids local: repository keys.
- Child intake uses native CreateRun. Child reads use RunDetail. Authorization reads sealed child lineage and the original issuer's immutable run/job/holder/generation instead of the legacy invocation table.

## W2 request shapes completed locally

Log signing accepts blob.LogSigningRequest. Log commits accept subject, segments, final and optional incomplete_reason. Segments use store.Segment. An omitted object key is derived by the server. Live pushes accept subject, offset and base64 JSON data. No actor or retention class is accepted. C1 completeness reads use committed metadata and verified segment bytes. The physical signed manifest slot remains part of W7's batch contract.

Child intake accepts controllerwire.CreateRunRequest. Its lineage must name the path parent and a parent job. Reads return controllerwire.RunDetail. Pool operations use existing controllerwire shapes and return canonical repository keys.

## Remaining four routes

1. GET /api/v1/runs/{run}/jobs/{job}/output needs a native committed result/record-selector read projection. NodeRow has none. Member reads need a C1 authorized read of that record before signing. The generic data authority resolver accepts capability authority, not member sessions. Do not restore OutputReadGrant or legacy claim tokens.
2. GET /api/v1/runs/{run}/children/{child}/jobs/{job}/output needs that projection and the child read edge through the blob authority path. W1b owns committed-result/materialization integration. Current blob read relations cover dependencies and declared pipeline references but no child edge.
3. POST /api/v1/runs/{run}/jobs/{job}/claim/input needs the chosen committed memo/coalescing/reference input wire request and native read projection. Retained ClaimInputRequest/ClaimInput accepts legacy ClaimToken and returns legacy output grants.
4. POST /api/v1/operator/teams/{team}/refunds needs a human-refund request/queue contract joining current console authority to the provider and ledger owners. Provider verification and SettlePayment come from other work. The old automatic card-refund worker does not define this human operation.

PUT /api/v1/queue/groups/{group}/priority now has a handler against C2's
QueuePriorityWriter. It validates the existing wire request and preserves exact
zero, tenant authority and the idempotency key. The default refuses a missing
transaction. C1 stores arrival order and still needs a persisted integer-priority
contract and ordering mutation. Changing arrivals would alter continuity and
would not implement integer priority. The HTTP property uses a fake transaction.

Real integration points remain. W9 has no native log HTTP client for these completed shapes. Tests fake only W7 object I/O. Dashboard log readers still select legacy NodeLog records; new submissions persist native log_streams/log_segments. That reader cut belongs to the storage owner. Child replay identity currently scopes by subject/pipeline without parent generation. A replacement parent reusing a key may replay an earlier child's intake, while its new generation correctly cannot read that child. C1 must include the immutable parent fence in child replay identity. Pool branch patterns are accepted by the existing wire contract and new handler, but intakePoolsTx still compares branch values exactly. C1 needs to consume those patterns against verified App ref evidence.

## Verification

Commands used GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache GOTMPDIR=/tmp/sw-astra-W2/routes. Tests used at most -p 4.

Passed:

- go build -p 4 ./...
- go vet -p 4 ./internal/store ./internal/controller
- Full go test -p 4 ./internal/apiroutes -count=1
- go run ./internal/commentcheck -base main . and git diff --check
- Store tests for exact-writer log signing, activity, pool selectors/replay, foreign installation authority, runner revocation/current roles and native child generation binding. Generated cases cover subject/writer changes, foreign installations and child binding/target substitutions.
- TestLogSubmissionRoutesCommitOnlyTheirExactAuthorizedSubject drives actual HTTP claim, execution start, signing, two incremental verified commits, final commit and exact replay, then reads native metadata. Generated substitutions obtain no signed URL. It exposed the controller attempt-zero authentication defect, which the parent fixed through the existing committed snapshot.
- TestControllerRegistersDeclaredLogSubmissions.

TestFinalCredentialServingRegistrations now reports exactly the four routes above in both metrics configurations. No native acceptance matrix cell is claimed. The existing child-binding test now uses real AdmitRun. No working test was deleted.

## Files and budget

New production files:

- internal/controller/child_routes.go
- internal/controller/log_submission_routes.go
- internal/controller/missing_operation_routes.go
- internal/controller/pool_routes.go
- internal/store/controller_pools.go
- internal/store/controller_reporting.go
- internal/store/controller_runner_admin.go

Shared production edits: internal/controller/engine_claim_routes.go and the child relation in internal/store/authority_binding.go. The parent owns the server.go hookup and authentication ordinal projection. Shared test migration: internal/store/binding_runtime_test.go. New tests: internal/controller/log_submission_routes_test.go, internal/store/controller_pools_test.go and internal/store/controller_reporting_test.go.

The new production files add 853 lines. This slice removes two legacy child-invocation reader lines and adds native binding and named-claim decoding. The lead's complete W2 measurement against main, including untracked production Go and the standard exclusions, is +14,110/-16,998, net -2,888. W2 is within its -250 budget. The lead reports its shared-store additions separately. No schema, configuration option or dependency was added by this slice.

Friction: concurrent edits temporarily caused build errors in binding_execution.go and the intake source resolver. A Go test hit a missing shared-cache file and standard-library package errors. Its identical retry passed. The cache was never deleted. No Git writes or Sparkwing binaries ran. The worker scratch directory was removed after its checks finished.

STATUS: CONTINUE
