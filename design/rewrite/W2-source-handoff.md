# W2 source and plan-secret continuation

<!-- design:begin -->

## Implemented

The merge conflict in `internal/controller/engine_contract.go` has resolved contents.
W2's split implementations remain. The incoming signed version-policy field and
compatibility implementation remain, with calls adapted to invalidEngineBody.
The stored run-detail reader adds verified version notices before projecting its
safe public response. The incoming version/detail tests pass.

Git still lists the file as unmerged because the swarm brief prohibits Git
writes. The lead must stage the resolved file and finish the merge. No conflict
markers remain. Incoming W1 work is retained.

Source release uses the actual PolicyRouter registration at
`POST /api/v1/runs/{run}/source-credential`. It passes Authorized unchanged to
C1's ReleaseSourceCredential transaction and opens the returned bound envelope
only after a committed audit. The adapter compares audit scope with the
credential and authenticated binding before decryption. Responses carry only
kind, host, secret, optional username and optional known_hosts. Checkout facts
remain in the sealed execution input.

Machine consent uses `PUT /api/v1/tokens/{token}/git-credentials`. It requires
an explicit enabled boolean and passes the account AuthorityRef to C1's
SetSourceMachineConsent. Policy requires an account session and machine-owner
binding. The web caller uses this path.

WithSecretsCipher installs C1's existing PlanSecretOpener. It uses the same
stored-row envelope binding as ordinary controller secret reads. Clearing the
cipher removes the opener. Failed opening prevents plan persistence.

SourceController, SourceTransactions, SourceCredentialResponse and
WithSourceController are local C2 contract additions. SourceTransactions keeps
C1's existing method signatures. The resolver supports embedded controllers and
fakes because concrete Tenant construction belongs to C1. No new SQL, issuer,
credential store, environment variable, service or customer limit was added.

## Deleted and retained

Deleted 401 legacy implementation lines: claim_source.go (264), git_credential.go
(107) and the former consent request/handler in git_credentials_team.go (30).
Two old registrations and the source limiter field remove three more lines.
The legacy claim grant-kind slice moved to claim_route.go because remaining
cache/heartbeat/execution-start routes still use it.

The deleted code includes legacy source claim lookup, token-table eligibility,
unaudited App minting and source-specific per-process limiting. The final source
transaction owns eligibility and auditing. The former App implementation cannot
satisfy that contract and is not retained as a fallback.

Legacy App/source tests and the shared launchedRun helper remain because their
App, extra-repository and acquisition findings still need final contract ports.
Deleting them would discard uncovered requirements. The broader old credential
subsystem remains for later hard cuts.

## Acceptance and test ports

The source invariant is that only the credential covered by a committed audit
may be decrypted for the authenticated claim. FuzzSourceReleaseHTTPAuditCannotChangeCredentialScope
generates foreign team, credential, host, repository, run, job, prefix and
replacement-generation facts. It passed 1,694 executions in five seconds.

The secret-opening invariant is that a stored envelope opens only for its exact
tenant, name, pipeline and access flags. FuzzPlanSecretOpenerCannotOpenRelocatedEnvelope
passed 15,292 executions in five seconds against the real bound cipher.

TestSourceControllerUsesStoreAuditAndBoundCipher exercises the real HTTP router,
SQLite authentication and ancestry, controller-to-controller delegation, source
binding, enrollment, consent, audit transaction and bound decryption. Its cipher
checks the audit row before opening. Revoking the root through RevokeAuthority
then returns HTTP 401 without opening the credential. No network listener is
needed.

The HTTP acceptance tests cover absent/revoked/ended/replaced authority, binding
and grant refusal, cancellation, transaction failure, malformed input, audit
mismatch, private decryption errors and account-authorized consent. They port
component coverage for own-claim source access, root/ancestry revocation,
team/host scope and audit-before-release. W1's existing source-release tests
also pass, including consent, host, enrollment, audit/commit failures and races.

Plan-secret tests verify the store-selected binding through the real acceptance
transaction with a failing cipher probe. A successful open reaches W4's
ValidatePlan panic stub. The final tests therefore use the declared opener
interface and a cipher fake for this boundary. They do not claim encrypted
identifier validation through the complete acceptance transaction.

No full native acceptance cell is newly unskipped. The complete
credentials-source-release-opt-in and credentials-source-release-races-and-hosts
cells still need native acquisition, App, submodule and process-leak evidence.
The existing executable acceptance suite passes with its native pending cells.

## Amendment dispositions

No handoffs.md entry names W2. Every W2 amendment group is accounted for below;
earlier implementation details remain in W2-amendments-1-handoff.md and
W2-data-http-handoff.md.

| Rows | Disposition |
| --- | --- |
| A1-01/03/05 | Shared error decoder and mask/report policy remain implemented. W3's real process-service router remains open. |
| A1-08/09/19/26/55 | Claim authorization, named here acquisition and enrollment/version boundaries remain. W3/W8/W9 still own production runner identity and here transport. |
| A1-11/16/17 | Private source/acquisition and execution-state projections remain. Source response now leaves checkout identity in those seals. Consumer cutovers remain. |
| A1-14/15 | Failure reads and holder-preserving cancellation remain. Cancellation intent reason still needs C1's separate field; see below. |
| A1-20 | C1's merged root-origin policy is retained. W2 still needs final root/member/machine/service/Actions provisioning and credential endpoints. |
| A1-21/22/23 | Fresh snapshot verification and route evidence remain. Stored source and consent routes now use the actual policy router. Session/CSRF, console/peer and remaining route cutovers stay open. |
| A1-24 | Stored source release is connected to C1's real audited transaction. Data authority/metadata/charging projections and audited App release remain open. |
| A1-25 | PlanSecretOpener wiring is implemented and tested. Shared validation still depends on W4; trusted intake production adapters remain. |
| A1-27/28 | Billing provenance and verified committed-checkout callback remain. Production payment configuration and other verified billing-event contracts remain open. |
| A1-29/30/31/32/33/34/35 | Generic authenticated data signing/records remain implemented against fakes. Public persisted authority/read/charge projections, physical backend integration and specialized memo/depcache transport remain. |
| A1-38 | Launcher sync still uses its declared fake-backed snapshot interface. C1's final production snapshot remains open. |
| A1-39/40/49/53 | Runner/local-controller/file startup and native topology coverage remain with W3/W8/W9/T1/lead. |

## Contract changes needed and real integration

- C1 must provide an audited App release permit carrying the verified
  installation/repository IDs and approved extras before W2 can mint the
  external token. ReleaseSourceCredential only returns stored SSH/HTTPS
  credentials and rejects other kinds. W2 must not reconstruct this authority
  through token-table or installation lookups outside the transaction.
- C1's AuthenticationReader still omits the session presentation digest.
  Legacy session CSRF lookup still reads removed sparkwing_meta and token
  tables, with the old time representation. Final session, console and CSRF
  persistence must precede W2's default browser adapter. No SQL copy or digest
  bypass is added here.
- C1 must give cancellation intent its own reason field. W2 keeps Extra.Result
  empty when cancelling held work because C1 currently writes supplied result
  fields into the holder's row. TestEngineCancelJobKeepsLiveHolderAndRechecksOwner
  still fails on its reason assertion. Passing a fake cancelled result to make
  that test green would violate holder-result ownership.
- Earlier C1 data-authority/live-blob/charging projections, logical lookup and
  upload-ticket selectors, launcher snapshot, authority-bearing console writes
  and sizing mutation requests remain open.
- W3/W8/W9 must connect source.Access to the new response and use sealed source
  location/SHA/checkout policy. The remaining cluster fetch-source client still
  expects the removed App/checkout response fields and fails loudly when its
  repository list is absent. It is retained with W8's old runner path.
- Account-session consent has fake HTTP coverage and C1 transaction coverage.
  Full browser identity/CSRF and dashboard ownership controls remain open.
- Default stored run-detail reads require a configured signed version policy
  after planning. Production configuration remains with the engine integration.

There is no new product-policy question. W2 is incomplete. Remaining W2 work is
the credential and route hard cuts, trusted browser/console/peer and provisioning
adapters, audited App minting after its C1 contract, and specialized data/cache
transport. Native QA remains a launch requirement.

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W2/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp.
Tests used at most -p 4. Scratch is deleted after verification.

| Check | Result |
| --- | --- |
| go build -p 4 ./... | Pass. |
| Scoped go vet on capability, apierr, controller, acceptance, apiroutes, apispec and apiref | Pass. |
| Complete capability/API-error/executable acceptance/API-route/schema/reference tests | Pass. Native pending cells still skip. |
| Source/plan-secret focused controller and acceptance tests | Pass. |
| Broader Engine/PolicyRouter/ServerPolicy/handler-lint/signer selection | One existing cancellation-reason failure, described above. Other selected tests passed. |
| Existing W1 TestSourceRelease and TestSourceMachineConsent selection | Pass. |
| Exact controller-route classification and duplicate inventory tests | Pass. |
| Source audit fuzz and secret-envelope fuzz, five seconds each, two workers | Pass, 1,694 and 15,292 executions. |
| Web gitCredentials caller test | Pass. |
| API/schema/reference generation and check-api-spec.sh | Pass. |
| Docs symbol and embedded-mirror checks | Pass. |
| go run ./internal/commentcheck -base main . | Pass, clean across 785 files. |
| git diff --check | Pass. |

The controller selection is reproducible with:

```sh
go test -p 4 ./internal/controller -run '^(TestEngine|TestPolicyRouter|TestServerPolicy|TestAuthorizedHandlers|TestFilesystemSignedObjectBoundary|TestS3SignedObjectBoundary|TestSourceController|TestPlanSecretOpener|FuzzPlanSecretOpener)' -count=1
```

The passing shared suite is:

```sh
go test -p 4 ./internal/capability ./internal/apierr ./internal/acceptance ./tests/acceptance ./internal/apiroutes ./internal/apispec ./internal/apiref
```

Use the environment and worktree stated above. The only failing controller
case is the cancellation-reason assertion, rather than the new source or opener
checks.

The initial source tests failed because the adapter types and routes were absent.
The opener test failed because WithSecretsCipher did not configure the store.
A full successful plan-acceptance probe reached the W4 stub and was replaced by
boundary tests, preserving that real-integration gap. A broad orchestrator
selector included socket tests, so it was cancelled and replaced by the exact
route-inventory selection. No Sparkwing CLI or pipeline was invoked.

The lead owns independent review, native/database/backend integration and
Sparkwing gates. No Git metadata was written. Git still requires the lead to
stage the merge resolution. The local Git identity and core.worktree settings
were not changed.

Shared implementation/caller edits outside W2 are
internal/apiroutes/apiroutes.go, internal/apiroutes/apiroutes_test.go,
internal/orchestrator/wingd_api_routes_test.go,
tests/acceptance/source_release_http_test.go,
web/src/lib/gitCredentials.ts and web/src/lib/gitCredentials.test.ts.
Other shared artifacts are api/openapi.yaml, docs/api-reference.md, docs/auth.md,
docs/git-credentials.md, docs/security.md, docs/migrations/_unreleased.md,
CHANGELOG.md, their generated mirrors and .apidiff/pkg_controller.txt.
No production Go caller outside W2 needed a compile migration.

Friction was the C1 session/CSRF gap, W4 validation stub, absent audited App
permit, cancellation-intent/result conflation and the remaining legacy clients.

<!-- design:end -->
