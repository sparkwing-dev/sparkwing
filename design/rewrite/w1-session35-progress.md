# W1 continuation 35

W1 remains incomplete. This session starts at 4dc30a9d5. The lead retains review,
committing, pipelines and landing. W1b implementation files are unchanged.

Removed four unused Tenant APIs: ReleaseGitCredential, GitCredentialReleases,
SetGitCredentialMachine and GitCredentialMachine. Removed GitCredentialRelease,
SourceSpend, MaxSourceMints, ErrSourceCredentialSpent, plannedSource and the
legacy source-spend/audit helpers. Removed the unused capabilities.source_mints
column from the final schema. ReleaseSourceCredential, SourceReleaseAudits and
SetSourceMachineConsent retain authority and audit ownership. No new production
interface, service, setting or environment variable is added.

The invariant is that source credential release requires current tenant and
claim authority, confirmed host identity and current owner consent, and commits
its audit before returning the credential. The six legacy git tests are ported
to the capability release, mutation and consent tests. Confirmation replay,
same-fingerprint replacement, changed-fingerprint refusal, HTTPS confirmation,
deletion, tenant isolation and nanosecond audit persistence pass. Existing
properties generate forged authority projections and machine-owner changes.
The two controller tests of the retired mint counter are deleted. Their retained
claim ancestry and liveness concerns run in the final store tests. GitHub App
provider integration is not established here.

All eight store recovery harness cells now run as ordinary pkg/store tests.
Six harness files are deleted. Cancellation and reconciliation move out of
store_recovery.go, and the SQLite recovery wrapper is removed. The native
scenario catalog is unchanged. New fixtures open through storetest and seed
sign-in, enrollment, admission, accepted retry policy and claims through APIs.
No moved test has a hand-written INSERT. Lease expiry uses scoped projection
updates. Competing capacity uses a second admitted host-owned run. Successor
owners use real enrollment and issuance. The checks retain reopening, free
unstarted lapses, disruption caps, retry ordinals and replay, cancellation,
racing reconciliation, local orphan policy, continuous capacity adoption,
no revival after expiry and single promotion. The remaining shared recovery
fixture still serves host-event and billing consumers.

Net production Go lines against this session's HEAD:

- Deleted 971 and added 2, net -969, excluding test files.
- Store production deletes 214 lines and adds none.
- The production acceptance harness loses 755 net lines.
- One additional schema column is deleted.
- The lead's store-budget measure is +3,959 against main. The W1+W1b target is
  -4,250, leaving an 8,209-line overage. The measure excludes the acceptance
  harness. The budget is not met. No overage waiver is requested.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR beneath /tmp/sw-astra-W1.

- Whole-repository go build -p 4 ./... passes after the final schema edit.
- Vet passes for pkg/store/..., pkg/controller/..., internal/acceptance/...
  and tests/acceptance/....
- The W1 batch passes 500 selected top-level tests and 1,554 test/subtest
  results without skips. w1-session35-tests.txt records the source files.
  Extract their Test names, exclude names containing Postgres and names starting
  TestPg, and pass their anchored alternation to go test -p 4 ./pkg/store -run
  with -count=1 -timeout=5m. This batch ran before the final schema-only edit.
- After that edit, 56 scoped authority, capability, source, recovery and schema
  tests pass, with 181 test/subtest results and no skips. The selector is
  ^Test(StoreRecovery|SourceGitCredential|SourceRelease|SourceConsent|RewriteSchema|OpenRefusesAStoreFromAnotherBaseline|TranslateSchema|Capability|Authority).
- The source controller, source HTTP, consent HTTP and selected credential HTTP
  checks pass. The remaining billing-start-award-versus-start/store-sqlite cell
  passes. No native cell is newly unskipped.
- commentcheck -base main passes across 2,165 files. The generated API snapshot
  matches .apidiff. Embedded docs/changelog, SDK symbol references, changelog
  checks and diff whitespace checks pass.
- A broader final-schema selector also reaches three legacy direct-upload tests:
  TestSourceBundleBindsOneRun, TestSourceReserveAcquiresOnlyOneFreeTeamSlotBeforeAnyRun
  and TestSourceCanBindForTwentyFourHoursAfterCommit. All fail because data_objects
  is absent. Restoring HEAD's original git_credentials.go, source_credential.go
  and schema.sql through a Go overlay reproduces all three failures. This is
  pre-existing W1b direct-upload debt. The broader selection also skips its
  PostgreSQL foreign-database subtest because no server is configured.

Contract changes and integrations left open:

- No foreign contract is changed. C1 records the removals and recovery ports.
- W1's persisted result selectors and materialization state handling remain
  implemented and tested with transaction-local fakes. W1b still must implement
  CopyMaterializedRecords in the existing transaction interface. No physical
  record-copy or byte-integrity coverage is claimed.
- W9 still must carry committed output and manifest selectors in ExecutionJob
  and its controller projection. W3 must consume them. W7 retains record
  authorization and byte verification after selection.
- C2 ProvisionSpec still lacks the approved seeded-bootstrap presentation.
  Store.Issue needs it for the one-use, no-existing-admin transaction.
- StateStoreOld, raw lifecycle/readiness, old claim/trigger/token paths and their
  callers remain. Console and verified-local authority persistence/composition,
  SetRunnerMetered, console-origin services, current host/source observations
  and production pool-rule mutation/composition remain W1 work.
- Rotation grace, the five Operator credit settings, the launcher store fence
  and six dead contract tables were completed before this session. Their
  regression selections pass. They were not reimplemented.
- PostgreSQL, native placements, real api.sock transport, Kubernetes, payment
  providers and repository QA remain unverified here.

Shared caller files are pkg/controller/claim_source_test.go,
pkg/controller/source_credential_test.go and tests/acceptance/recovery_test.go.
Other shared edits are C1, the port inventory, .apidiff/pkg_store.txt,
CHANGELOG.md, docs/migrations/_unreleased.md and its embedded mirrors.
The schema edit removes only the obsolete source counter. Lead-owned handoffs
and amendments are unchanged. New tests and the manifest are included in the
working tree for the lead to add when committing.

Friction is the legacy direct-upload fixture failure above. No Sparkwing binary,
provider, deployment or release runs. No Git metadata is written. Local Git
configuration has no identity or core.worktree override. The assigned scratch
is deleted after checks. The shared build cache is retained.

STATUS: CONTINUE
