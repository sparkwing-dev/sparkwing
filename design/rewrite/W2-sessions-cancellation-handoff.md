# W2 session and cancellation continuation

<!-- design:begin -->

The 13 conflicted paths have resolved file contents. W2 implementations win
where the integration branch retained replaced behavior. Both sides' migration
notes and handoffs survive. WithSecretsCipher configures both the plan opener
and session-key cipher. The controller API snapshot was regenerated. Four
already-deleted legacy test files remain deleted (952 incoming lines). Other
incoming legacy test hunks remain removed. Git still reports unmerged index
entries because the swarm brief forbids git writes. The lead must stage the
resolutions and finish the merge. HEAD remains 6de2c75d0.

## Implemented

Session authentication compares the presentation digest with its committed
AuthorityRef.ID, the sessions.hash key. Capability.Digest remains the bearer
identity. This local C2 correction removes the ten-line sessionDigestFixture
that patched store snapshots. Fake session/console identities now use the
same hashed reference. Bearer matching and ancestry checks are unchanged.

The default shared-policy CSRF callback reads the live session through C1,
checks its identity/team/account and validates the same-origin header/cookie
proof against its persisted-key-derived token. Generated cases reject planted
matching header/cookie tokens, cross-session tokens, foreign origins and
mismatched cookies. Real encrypted sessions pass without an authorization
fixture. Demotion and membership removal take effect on the next request.
Missing or wrong session keys return a private unavailable response.

Invariant: a browser mutation requires a live session and same-origin proof
bound to that exact session. No second key, store, cache, setting or service
was added. Browser renewal and absolute-lifetime integration, roleless OAuth
sessions, license gating and console/peer identities still need their final
shared-policy adapters.

CancelJob now sends the reason through C1's existing request-only Result
payload. C1 stores it in cancellation intent without closing the held row or
its attempt. The previously failing event/reason test passes. Generated reasons
preserve the holder, claim fence, attempt, status and result rights. Exact replay
keeps the same state sequence. W2 added BindingState.CancelReason as requested
by W1. W1 still needs to populate that authentication field from committed
intent. No W1 implementation or foreign contract signature was changed.

## Amendment dispositions

Prior completed work is recorded in W2-amendments-1-handoff.md and the focused
W2 handoffs. This table carries forward every amendment naming W2.

| Rows | W2 result and remaining integration |
| --- | --- |
| A1-01/05 | Shared node mask/annotation/summary policies remain implemented. W3 owns production process-service registration and sink acknowledgements. |
| A1-03 | Inner error decoder and amendment tests pass. |
| A1-08/09/19/26/55 | Final runner/launcher/named claim policy and HTTP adapters remain implemented. Enrollment/software-policy production wiring, here delivery and W9 clients remain open. |
| A1-11 | Typed sealed source location is retained. Source authorization/execution remain with W1/W3/W6/W8/W9. |
| A1-14/15 | Real failure reads and cancellation adapters pass. Cancellation intent now reaches C1 and its event. Authentication reason projection remains W1 work. |
| A1-16 | Current execution/expansion projection remains implemented. Full native private award delivery still needs the runner/client owners. |
| A1-20/21/22/23 | Root issuance, fresh authentication, evidence, shared router and default session CSRF are implemented. Session matching now uses the committed row identity. Other root origins, roleless/console/peer identities, full controller cutover and node-host registration comparison remain. |
| A1-24/25 | Bound data/source adapters and plan-secret opener remain implemented. W1 owns live transactional reads. Source release uses its audited transaction. No controller SQL is added. |
| A1-27/28 | Billing provenance and verified payment boundary remain implemented. Production payment configuration and full placement settlement evidence remain integration work. |
| A1-29/30/31/32/33/34/35 | Signed-data, upload, record and cache transports remain implemented against their interfaces and fakes. Live authority/metadata/charging, physical backend wiring, logical selection authorization and W9 clients remain open. |
| A1-38 | Bootstrap exchange/renewal and fake-backed launcher sync remain implemented. C1's real launcher-sync snapshot is absent. |
| A1-39/40/49 | Unified runner, local controller and credential-file delivery remain W8/W9 integration work. W2's existing final routes remain available through their contracts. |
| A1-53 | Native placement/API socket/backend evidence remains T1/lead work. No native cell was unskipped. |

## Cross-area work still open

- W1: populate BindingState.CancelReason. Preserve the first job request over
  a later run request. C2's field is now available.
- W1: implement the existing CredentialWriter, CredentialRotator and
  SourceAuditReader contracts. Inspection found none of those methods on
  Tenant. Missing adapters still fail closed.
- W1: logical DataLookup still sends an empty digest to readableBlobsTx.
  Complete record-selection authorization and transaction-level edge checks.
- W1/W7/W8: connect persisted data authority, blob metadata, charging,
  object backends, enrollment/version policy and launcher-sync reads.
- W9: NewEngineClient and NewDataClient remain panic stubs. Full native
  client/runner transport is not established by the fake-backed HTTP tests.
- W2: secret-by-name release, remaining legacy route/authentication cuts,
  nonmember provisioning, roleless browser flows, console and local-peer
  adapters, and remaining legacy credential fixtures still require work.
- W9: documentation tests still reference removed SDK symbols and have two
  stale shell-command search expectations. The exact failures are
  TestDocsNameOnlySymbolsThatExist and TestShellCommandQueriesReachTheRightSection.
- W5/T1: broad acceptance reaches native TypeScript tests, but this worktree
  has no sdk/typescript/node_modules/typescript/bin/tsc. Native evidence is
  unavailable here. Dependencies were not installed by W2.

There is no new foreign contract change request. The existing cross-area
job-transition source-attempt handoff remains open. 
## Verification and ports

Passed:

- Whole-repository go build -p 4 ./....
- Scoped go vet for capability, apierr, controller, internal/acceptance and
  tests/acceptance.
- Complete capability/apierr unit tests and API route/schema/reference tests.
- Focused credential, authority, data, engine, operator, deployment, source,
  payment, credit, sizing, discovery, identity, queue and reporting acceptance
  tests, including their selected fuzz seeds.
- TestCredentials/credentials-session-digest-csrf/store-sqlite and
  TestCredentials/credentials-operator-console-split/store-sqlite.
- Focused controller engine/policy/issuance/storage/signing tests, including
  TestServerPolicySessionsCheckCommittedIdentityAndCSRF,
  TestAuthenticationSessionMatchesOnlyItsCommittedRowProperty,
  TestEngineCancelJobKeepsLiveHolderAndRechecksOwner,
  TestEngineCancellationIntentPreservesHolderProperty and the retained session
  capability-scope property.
- Commentcheck against main, mirror/changelog drift and git diff --check.

The broad acceptance run failed on native TypeScript setup. The broader docs
run failed on the unrelated SDK-symbol/search expectations above. No full
legacy controller pass is claimed. No parked T1 port or new native cell was
completed. Existing SQLite cells were rerun, not newly unskipped.

Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and -p 4.
GOCACHE and GOTMPDIR/TMPDIR were under /tmp/sw-astra-W2. That directory is
removed after checking all owned processes have completed. The lead retains
independent review, product pipelines and committing/landing.

## Shared edits and friction

Shared edits are contracts/c2-credentials.md and handoffs.md, docs/auth.md,
docs/migrations/_unreleased.md, their generated mirrors and
.apidiff/pkg_controller.txt. Acceptance edits are tests/acceptance/{authority_mutations_http,
credential_authentication,credential_inventory_http,credential_issuance_http,
credential_maintenance_http,credential_mutations_http,credit_reads_http,
data_binding,data_cache_http,data_reads_http,deployment_authorization,
discovery_http,engine_job_control_http,engine_run_detail_http,identity_http,
operator_authorization,queue_reads_http,source_release_http}_test.go.
credential_authentication_test.go adds the session-reference constructor.
engine_job_control_http_test.go follows C1's intent payload. The others migrate
session/console references.
The resolved controller tests and server.go remain W2-owned.

Minimal removed-API caller migrations are console_fixture_test.go (three
LookupSession clock arguments), build_trust_test.go (tenant trust reader) and
credential_issuance_test.go (remove the digest-patching reader). No caller was
migrated outside controller or acceptance tests in this continuation.

Friction was the read-only merge index, persisted status versus derived claim
state in the new property test, retired store callers, missing native TypeScript
dependencies and stale unrelated docs. The bootstrap sparkwing info discovery
ran once before the swarm brief's binary prohibition was read. No candidate,
daemon, pipeline, release or Git write ran. The ordinary git identity remains
Korey's and there is no core.worktree override.

STATUS: CONTINUE

<!-- design:end -->
