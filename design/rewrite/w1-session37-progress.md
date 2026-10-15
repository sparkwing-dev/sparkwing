# W1 continuation 37

W1 remains incomplete. This session starts at 353b63667. The lead owns review,
committing, pipelines and landing. W1b persistence source is unchanged.

Removed eleven Store APIs. CreateOrReplaceSecret, GetSecret, GetSecretRow,
GetSecretForPipeline, GetSecretForRun, ListSecrets and DeleteSecret no longer
select the default team implicitly. Their existing Tenant methods remain.
The local secret reader selects DefaultTeam through ForTeam and returns a
selection error. Test callers use the existing teststore.ForTeam helper, which
fails the fixture when tenant selection fails. Controller requests retain
their authenticated tenant.

Removed NodeClaimFenceNodeForRun, NodeExecutionAttemptIsLive,
TriggerExecutionAttemptIsLive and TriggerExecutionAttemptBelongsToLiveClaim.
None has a production caller. Removed their SQL-review exceptions and tests
of those readers. The still-used NodeExecutionAttemptBelongsToLiveClaim reader
and its legacy execution-reporting test remain. Current authentication and
tagged log checks retain owner, attempt, ancestry and tail-grace requirements.
No authority implementation, storage schema or W1b log writer changes.

Net production Go lines against this session's HEAD, excluding tests, testdata,
the acceptance harness and contract declarations:

- Deleted 133 and added 6, net -127.
- Store deletes 132 and adds 1, net -131.
- The lead's store-budget calculation gives +3,573 against main. The W1+W1b
  target is -4,250, leaving a 7,823-line overage. No waiver is requested.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR beneath /tmp/sw-astra-W1.

- Whole-repository go build -p 4 ./... passes on the final Go tree.
- Vet passes for pkg/store/..., pkg/controller/..., internal/localsecrets,
  cmd/sparkwing-controller and pkg/localws.
- The store selections pass 57 top-level tests / 211 test and subtest results
  without skips. They cover the secret CRUD, scope, limit, sampling and tenant
  checks, current authority properties, plan seals, transitions, matching,
  queue order, launcher synchronization, reconciliation and materialization.
  The manifest w1-session37-tests.txt records anchored selectors for every
  passing top-level test executed in these batches.
- All ten internal/localsecrets tests pass. Credential HTTP selections pass
  22 top-level tests / 189 test and subtest results. The real controller/store
  lifecycle selection passes eight top-level tests and fuzz targets / 14
  results. The two controller key-refusal tests pass. These batches have no skips.
- Three tagged-log checks pass 11 results. Their files are unchanged.
- commentcheck -base main passes across 2,179 files. The regenerated API
  snapshots match the complete snapshot directory. The documentation symbol
  check, embedded-doc drift check, changelog check and diff whitespace check pass.
- Both changed controller and store test packages compile, as do localsecrets,
  the controller command and localws. Native localws startup and the backup
  drill are not claimed as execution evidence for their fixture-only edits.

The broader legacy selection has three failing top-level tests. Both
TestNodeExecutionAttemptBelongsToLiveClaimOutlivesTheAttempt and
TestClaimNamedNodeAwardsAnUnqueuedNode fail while inserting runs.args_json.
TestAMissingLookupNamesWhatItLookedFor fails its GetRun and GetTrigger subtests
on removed args_json and pipeline columns. Its secret and cancellation subtests
pass. Restoring every changed Go file from 353b63667 through a Go overlay
reproduces the same failures. These old paths still need the hard cut. They
are not passing acceptance evidence and no schema backfill is added.

Test ports and retirements are recorded in t1-port-inventory.md. Existing
secret assertions now run through explicit tenant handles. Tests specific to
the four removed readers retire with those APIs. Current authentication,
launcher and tagged-log tests provide the retained final-schema evidence.
No native cell is newly unskipped and no acceptance registry is enlarged.

Remaining work and real integration points:

- W1b still has no CopyMaterializedRecords implementation. W1's selector and
  materialization checks use transaction-local fakes. Physical copies and byte
  integrity are not proven here.
- W9's ExecutionJob still omits committed output and manifest selectors.
  W9 must transport them and W3 must consume them. W7 retains record-edge
  authorization and byte verification after selection.
- C2 ProvisionSpec still has no seeded bootstrap presentation. W2 must amend
  that contract before Store.Issue can implement the approved one-use seeded
  bootstrap flow while no admin exists. No foreign contract changes here.
- W1 still owns console and verified-local authority persistence/composition,
  SetRunnerMetered, console-origin service issuance, current host/source
  observations and production pool-rule mutation/composition.
- StateStoreOld, raw lifecycle/readiness methods, old claim/trigger/token paths
  and their callers remain. The queue scanner and named-claim path still use
  the removed schema. Retiring them with their fixture and controller callers
  is the next substantial deletion opportunity.
- Rotation grace, the five Operator credit settings, launcher store fencing,
  the moved W1 acceptance checks and six dead contract tables were completed
  before this session. They are not reimplemented. Their relevant current
  properties and store checks pass.
- PostgreSQL, native placements, api.sock transport, Kubernetes, provider
  billing and repository QA remain unverified. No new product question or
  foreign contract request is introduced beyond the open interfaces above.

The only shared production caller edit is internal/localsecrets/source.go.
Shared test callers are internal/localsecrets/key_test.go,
cmd/sparkwing-controller/secrets_key_test.go,
pkg/localws/secrets_sealed_test.go and the runner_claim, runner_scopes,
secrets_dashboard, secrets_encryption and secrets_team tests in pkg/controller.
The backup_restore_drill store fixture changes only its secret method owner.
Other shared edits are C1, the test-port inventory, the store API snapshot,
CHANGELOG.md, the migration guide and their embedded mirrors. Lead-owned
handoffs and amendments are unchanged.

Friction is the reproduced legacy-schema failures above. No new mechanism,
environment setting, service, customer restriction or compatibility API is
added. No Git write, Sparkwing binary, candidate service, pipeline, external
message, deployment, release or harness-memory write runs. Local Git config
has no user identity or core.worktree override. The assigned scratch is deleted
after final checks. The shared build cache is retained. The lead must include
this report and its test manifest when committing.

STATUS: CONTINUE
