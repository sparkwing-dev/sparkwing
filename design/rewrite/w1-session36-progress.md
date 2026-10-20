# W1 continuation 36

W1 remains incomplete. This session starts at 2a65f8eed. The lead retains
review, committing, pipelines and landing. W1b files are unchanged.

Removed six legacy APIs: Store.StartClaimExecution, Store.HeartbeatClaim,
Tenant.PutGitCredential, Tenant.ConfirmGitCredential, Tenant.DeleteGitCredential
and Store.ResealSecrets. Removed ClaimBeat, SecretResealCounts, the reseal batch
reader and the private teamSecret tuple. Removed the old claim-token HTTP start
and heartbeat branches and their unused client helpers. The other raw runner
start and heartbeat paths remain with their existing consumers.

Unit renewal uses RenewUnit. Job starts use TransitionNode with CauseStart and
SourceAttempt. Git mutation fixtures use WriteGitCredential,
ConfirmGitCredentialFor and RemoveGitCredential. Internal rotation fixtures use
the package-private transaction writer. External HTTP fakes seed a persisted
administrator session separately from the synthetic authentication boundary.
They still record the incoming authenticated actor. No production authority
substitution or compatibility API is added.

The renewal invariant is that only a current issued award may renew or start its
unit. TestRenewUnitRequiresCurrentAwardProperty generates 384 root, unknown-prefix,
ancestor-revocation, lease-expiry, generation and holder cases across trigger,
plan, eval and job units. Refusals must be typed authority errors, preserve the
lease, leave execution unstarted and create no attempt. Revocation uses the real
transaction writer. Infrastructure errors do not count as successful refusals.

The historical resealing API and controller startup plaintext scan are removed
under the pre-launch no-backfill rule. The retained missing-key sampler returns
TeamSecret directly. Its existing test moves unchanged into
secrets_sample_test.go. Keyless reads and writes still return ErrKeyRefused.
The cipher refusal test and HTTP encryption-failure properties retain the
key-name diagnostic, canonical refusal and no-persistence requirements.

Net production Go lines against this session's HEAD:

- Deleted 374 and added 7, net -367, excluding tests, contract declarations,
  testdata and the acceptance harness.
- Store production deletes 260 and adds 5, net -255.
- The lead's store-budget measure is +3,704 against main. The W1+W1b target is
  -4,250, leaving a 7,954-line overage. The budget is not met. No waiver is requested.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR beneath /tmp/sw-astra-W1.

- Final whole-repository go build -p 4 ./... passes.
- Vet passes for internal/store/..., internal/controller/..., cmd/sparkwing-controller
  and tests/acceptance/....
- The store batch passes 106 top-level tests and 346 test/subtest results.
  The PostgreSQL subtest of TestOpenRefusesANonEmptyForeignDatabase skips
  because no server is configured. All SQLite checks in this batch pass.
  It includes result selectors, materialization, authority, credential mutation,
  recovery, reconciliation, schema, session and tenant-scope checks.
- The final strengthened renewal property separately passes all four unit kinds.
- The credential/session HTTP batch passes 30 top-level tests and 213 results
  with no skips. The real controller/store lifecycle batch passes eight
  top-level tests/fuzz targets and 14 results with no skips. The manifest
  w1-session36-tests.txt records each package and anchored selector.
- TestConfigureSecrets_MultiTeamRefusesToStartWithoutAKey and
  TestKeylessCipherRefusesCredentialAccess pass in cmd/sparkwing-controller.
- commentcheck -base main passes across 2,169 files. The regenerated API
  snapshots, embedded docs/changelog, documentation symbol references,
  changelog check and diff whitespace check pass.
- A broader HTTP batch finds three existing TestEngineLifecycleHTTP/transition
  failures: success, store_race and private_error. Each returns HTTP 400
  bad_request, invalid engine request, before its fake transaction is called.
  Restoring every changed Go file from 2a65f8eed through a Go overlay reproduces
  all three failures with the same messages. No fix to that foreign fixture or
  request contract is claimed. The narrower real controller/store lifecycle
  checks pass, including source-attempt projection and retry ordinal fencing.

Acceptance ports and retirements are recorded in t1-port-inventory.md. The old
pod claim-token route test retires with its start and heartbeat APIs. Its retained
liveness, cancellation, holder and current-source authority requirements run in
the final unit/state/source tests. The spent-source-counter assertion retires
with that removed counter. The two resealing tests and obsolete startup
plaintext-conversion test retire with historical backfill. No native cell is
newly unskipped, and no acceptance registry is enlarged.

Remaining work and integration points:

- W1's persisted result selectors and materialization state handling pass with
  the existing transaction-local fakes. W1b still must implement
  CopyMaterializedRecords. Physical copying and byte integrity are not proven.
- W9's ExecutionJob still omits committed output and manifest selectors.
  W9 must carry those selectors in its controller projection, and W3 must consume
  them. W7 retains record authorization and byte verification after selection.
- C2 ProvisionSpec still has no seeded bootstrap presentation. W2 must amend
  that foreign contract before Store.Issue can implement the approved one-use,
  no-existing-admin transaction. No foreign contract is changed here.
- W1 still owns console and verified-local authority persistence/composition,
  SetRunnerMetered, console-origin service issuance, current host/source
  observations and production pool-rule mutation/composition. Metering cannot
  trust a caller-supplied console principal in place of committed authority.
- StateStoreOld, raw lifecycle/readiness and old claim/trigger/token paths and
  their remaining callers still require deletion and migration.
- Rotation grace, the five Operator credit settings, launcher store fencing and
  the six dead contract tables were completed before this session. They are not
  reimplemented. Current inspection also finds SourceAttempt transport and the
  controller SyncLauncher body implemented. Their earlier missing-body reports
  are stale. Native launcher integration is not established here.
- PostgreSQL, native placements, real api.sock transport, Kubernetes, provider
  billing and repository QA remain unverified.

Shared production caller files are internal/controller/claim_run.go,
internal/controller/client/claim.go, internal/controller/server.go,
cmd/sparkwing-controller/keyless.go and cmd/sparkwing-controller/main.go.
Shared tests are internal/controller/claim_run_test.go,
internal/controller/secrets_encryption_test.go,
cmd/sparkwing-controller/secrets_key_test.go and the credential_inventory,
credential_maintenance and credential_mutations HTTP files under tests/acceptance.
Other shared edits are C1, the port inventory, two .apidiff snapshots,
CHANGELOG.md, docs/migrations/_unreleased.md and their embedded mirrors.
Lead-owned handoffs and amendments are unchanged. The lead must include the two
new store test files, this report and the test manifest when committing.

Friction is the three baseline HTTP transition-fixture failures above and the
unavailable PostgreSQL lane. Initial repository orientation called the installed
sparkwing info --for-agent before the swarm no-binary rule was loaded. All later
verification used Go. No stateful Sparkwing command, candidate service, Git write,
external message, deployment or release runs. Local Git configuration has no
identity or core.worktree override. The assigned scratch is deleted after final
checks. The shared build cache is retained.

STATUS: CONTINUE
