# W1 continuation 26

W1 remains incomplete. This continuation implements the credential mutation
and secret-read handoffs for persisted account authority. HEAD is 5b1153476.
No Git write ran. W1b implementation files and the lead-owned ledgers are unchanged.

## Merge resolution

The only conflicted path was internal/orchestrator/wingd_api_routes_test.go.
The incoming W8 commit deletes its API implementation and test harness. The
resolution deletes this 334-line test too. Keeping it would reference removed
symbols. The whole-repository build passes with that deletion.

The index still lists the path as unmerged. The brief prohibits Git writes
and the metadata is read-only. The lead must stage the deletion and finish
the merge. There is no remaining conflicted file body to resolve.

## Implemented and deleted

Tenant implements C2 CredentialWriter through WriteSecret, RemoveSecret,
WriteGitCredential, ConfirmGitCredentialFor and RemoveGitCredential. Every
transaction checks the selected member/team.admin alternative, current
ancestry and membership, exact tenant and persisted actor. It checks authority
again before commit. Store time supplies timestamps. The team lock serializes
the existing caps. Confirmation reads the current pinned fingerprint in its
transaction. Replacing an SSH credential preserves confirmation only for the
same fingerprint. A failed transaction returns no credential.

Tenant.ReadSecret implements C2 SecretReader. It repeats the selected route's
authorization against current persisted authority. A member can read only an
unmasked row within its tenant. A claim's bound run supplies its pipeline even
when the request omits run. Declaration, row precedence, shared scope, pools,
lease, generation, cancellation and deadline are checked in the transaction.
Selection and secret_released commit together. A failed audit or final
authority check returns no row. Claim-wide attempt zero survives execution
start. A delegated nonzero attempt cannot read after that attempt changes.

Removed the 75-line ReleaseClaimSecret/error path and its exported
ErrSecretUndeclared. Undeclared reads use canonical forbidden with reason
secret_undeclared. Removed the 90-line superseded controller secret regression
and 11 lines of obsolete generated-fixture commentary/assertions. The new
store and HTTP tests retain the release, declaration, binding and audit
requirements under the final contracts. The old missing-run refusal is replaced
by the required implicit bound-run behavior. The old OIDC gap assertion and
legacy flat error assertions do not describe the final protocol.

The existing git insert logic is shared by the new transaction. Its raw API
and the remaining raw credential fixture APIs still need retirement. There is
no compatibility alias or second issuer.

## Invariants and evidence

Invariant: a credential write or secret release commits only while current
ancestry, membership, tenancy and the selected route alternative authorize it;
a claim release also retains its declared secret and live claim fence.

Generated cases vary tenant, grants, route alternative, membership, revocation,
lease, generation, cancellation, pipeline, declaration and pool. Tests also
cover same-key and changed-key confirmation, failed replacements, missing rows,
concurrent cap admission, and authorization loss caused by transaction-local
triggers. Failed operations roll back values and audits. No test sleeps.

TestCredentialTransactionsHTTPUseDefaultStoreAdapters uses real C2 session
verification, default Tenant adapters, encryption and SQLite through the real
handler. It checks tenant-selector independence, stored actor, unmasked reads,
masked refusal, deletions and demotion. It opens no listener. Existing fake
adapter tests now expect unauthenticated when their invented authority reaches
the real store. Explicitly missing adapters still return policy_missing.

The existing credentials-session-digest-csrf/store-sqlite cell also exercises
credential mutations and reads with the persisted owner session after reopening.

Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4,
and GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W1.

- Whole-repository go build -p 4 ./... passes.
- Scoped go vet passes for pkg/store/..., pkg/controller/..., internal/acceptance/...
  and tests/acceptance/....
- W1's selection passes 427 top-level tests / 1,326 cases with no skips.
  Extract Test names from w1-session26-tests.txt, exclude Postgres/Pg names,
  and pass their anchored alternation to ./pkg/store -count=1 -timeout=180s.
- The final attempt-specific read extension also passes separately after that
  selection, including current-attempt access, stale-attempt refusal and
  retained claim-wide access.
- Acceptance passes 26 top-level tests / 292 cases with no skips. The selection
  is the 17 SQLite cells under TestLifecycle, TestRecovery, TestPlacement,
  TestCredentials, TestBilling and TestSurface, plus TestCredentialTransactionsHTTP,
  TestCredentialMutationsHTTP, TestSecretReadHTTP and TestCredentialMaintenanceHTTP.
- commentcheck -base main . passes across 1,659 files. git diff --check,
  sync-docs.sh --check and check-changelog.sh pass. The store API snapshot is
  regenerated. Git has no local user.name, user.email or core.worktree override.

The new API tests first failed compilation on the absent methods. Secret-read
fixture failures required the existing scoped opener and complete run rows.
The real HTTP fixture was corrected to use the declared token field and 200
response for git writes. None of those fixture failures is counted as a
production regression. The running-job read failed at the attempt boundary
before its claim-wide fence correction.

## Contract and real-integration points

New W2 finding: C2 authentication rejects a valid job award after execution
start. A temporary TestW1C2RunningSecretProbe used secretReadStoreFixture,
real ReconcileNode, real TransitionNode(CauseStart, SourceAttempt=0), and
capability.NewAuthenticator(store).Authenticate against GET /api/v1/secrets/{name}.
The returned real job bearer authenticates before start and returns
"credential is not live" after it. internal/capability/authentication.go
sameFence compares the award's attempt zero with the node's current attempt
one for equality. It must distinguish claim-wide zero from a concrete delegated
attempt without weakening generation/holder fencing. The temporary probe is
removed. The retained store test covers both forms, but no successful real
running-job HTTP release is claimed. W2 owns its authentication mechanism.

The earlier W9/W3 SourceAttempt transport gap is still present in
JobTransitionRequest. LauncherObservation still lacks claim generation and
Kubernetes Job-instance identity. W2/W8/W9 must amend those foreign contracts
before safe real integration. Verified payment events remain W2/W1b work.
Verified local-peer credential transactions still need C2's transport-to-store
authority integration. No foreign contract signature changed here.

## Amendment audit and remaining W1 work

The previously completed A1-07/08/11, 13-18, 20-27, 40, 44/45 and 55 store
work remains covered by the inherited selection. This pass advances A1-24's
credential adapters and A1-51's old-consumer cut. A1-28/29/31-35 stay with W1b's
billing/blob/log split. A1-53 native placement evidence stays with the lead and
owning areas. No native listener, api.sock, PostgreSQL, provider, pod, process
execution, full legacy suite, race suite or pipeline pass is claimed.

Remaining W1 work is GitHubRunnerRegistry and atomic Actions revocation;
console session persistence/authentication and RunnerMeteringWriter;
production machine/service enrollment, registration uniqueness and
ClaimProfileResolver; CapabilityManager inventories and rotation through
Store.Issue; verified local-peer credential transactions; and the remaining
StateStoreOld, raw lifecycle/readiness, bootstrap/plan snapshot, claim/token,
trigger and credential fixture API cuts. Startup bootstrap raw-credential
delivery still needs agreement with W2/C4. Existing-award billing must remain
immutable when root metering changes. The previous account-route integration
gap remains with W2.

## Shared files and friction

Shared Go files are pkg/controller/claim_run_test.go (superseded test removal),
internal/acceptance/store_sessions.go, new store_credential_mutations.go,
tests/acceptance/credential_mutations_http_test.go, secret_reads_http_test.go,
and new credential_transactions_http_test.go. The merge-only deletion is
internal/orchestrator/wingd_api_routes_test.go. No foreign production caller
needed migration for ReleaseClaimSecret. Its only remaining direct fixture
caller was removed from pkg/store/generated_node_test.go.

Shared prose is c1-data.md, docs/auth.md, docs/migrations/_unreleased.md and
CHANGELOG.md. Their embedded mirrors and .apidiff/pkg_store.txt are regenerated.
The only new production mechanism is the already-declared tenant transaction
adapter. There is no new setting, service, environment variable, schema,
customer restriction or billing policy.

Friction is the read-only merge index, C2's concrete attempt comparison and
the previously recorded foreign wire/enrollment gaps. No service or Sparkwing
binary ran. Scratch is deleted at the end. The lead owns independent review,
gates, staging and landing.

STATUS: CONTINUE
