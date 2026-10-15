# W8 embedded store queue continuation

## Implemented

`runners.NewStoreRunner` constructs the embedded queue over the existing
`EngineController` and its store. C2 authenticates and authorizes each claim.
The shared controller owns enrollment, signed software admission, matching and
issuance. The shared loop passes returned awards to `StoreHosting`, which builds
W3's native hosts over that same store. The host retains acquisition, renewal,
execution and cleanup ownership.

`localcontroller.Binding.NewQueue` gives the elected local service binder that
construction. It refuses a controller backed by another store. The caller
supplies a provisioned claimant bearer provider, loop observations and the native
service binder. The controller requires its runner policy, and C1 requires its
enrollment resolver. This is constructor composition. The production
`Config.Bind` and CLI startup are still incomplete.

Invariant: every requested unit kind is authorized by the current credential
before the embedded queue receives an award. Named trigger claims use their
run's ownership and policy. Ordered claims require all selected grants.
`FuzzStoreQueueClaimAuthorityW8` varies named/queued selection, missing grants,
duplicate/unsupported/omitted kinds, changed bearer bytes and revocation.
Refusals leave the queue unclaimed. Successful claims return the persisted run
and a separate execution award.

The existing `TestNativeStoreOwnerW8` now includes the real embedded queue:
local binding, C2 claim authorization, shared controller issuance, native host,
C1 completion and joined service cleanup. Its direct-award case retains the
runner-label mutation regression. Enrollment, software policy and source entry
remain fixtures. The empty accepted plan executes no SDK body.

Mechanism added: one embedded claimant adapter and its queue constructor. It
calls the shared authorization and controller owners because a second HTTP
client around the same process would still depend on W9's unimplemented client.
It adds no issuer, keeper, schema, environment variable, background service or
software policy. Removing this constructor would return embedded claim setup to
the service binder.

## Merge and deletions

All three conflict paths have resolved working-file contents. The lead must
stage them and finish the merge because the swarm brief forbids Git writes.
The index still lists its original three unmerged paths.

- `internal/depcache/depcache.go` retains the shared npmcmd implementation and
  incoming `Key`. The unused incoming npm command builder stays removed.
- `internal/depcache/depcache_npm_test.go` retains the local cache-directory
  tests. Windows argv/missing-tool coverage remains in the shared npmcmd suite.
- `internal/runners/service/leak_test.go` stays with the implemented runner
  service package. Its content is unchanged.

The depcache resolution prevents restoration of 86 incoming lines: 18 production
and 68 test/import lines. These repeat earlier removals. The fake claimant type
and method in the owner component are deleted, totaling nine lines. No further
legacy production host is deleted in this continuation. The warm-pool dispatcher,
run-host Kubernetes dispatcher, cluster worker and old Cloud pod builder remain
absent. The final Cloud builder remains `runners/launcher/job.go`.

## Amendments and handoffs

Every W8 row in amendments-1.md and handoffs.md was checked against the previous
native-host report. The lead-owned ledgers are unchanged.

| Rows | Disposition |
| --- | --- |
| A1-08,09,20,26,49,55 | Existing unified selection, launcher routing, issuance and private credential loading remain. The embedded queue adds real C2 enforcement for queued and named claims. Production enrollment/local claimant provisioning and Actions evidence remain integration work. |
| A1-10,11,12,43,45,46,48,50,51,52 | Existing engine, source, environment, snapshot and hard-cut work remains. No alternate implementation was added. W9's startup/client/notices/upgrade composition and native Windows packaging evidence remain open. |
| A1-18,27 | Ordinal zero and immutable award billing remain. W3/W9 still omit SourceAttempt on holder transitions. C1 refuses the omission. |
| A1-19,36,39 | Store-backed native host construction now has a real embedded claimant. Native services still supply source/process/log/storage bindings and fresh attempt delegation. Production runner Labels still need W2/W9 projection. |
| A1-38 | Existing immutable Cloud award transport remains. Wire observations still lack an award fence and Job UID. Plan/eval awards still clear Execution. No foreign signature was changed. |
| A1-40,41 | The elected local owner now exposes concrete queue construction. State/activity/peer bindings and the final service factory remain open. Readiness, four holds, drain, managed argv and maintenance remain implemented. |
| A1-42,53 | Deployment isolation/scratch/teardown and native placement/platform evidence remain unproven. No native cell was enabled. |

Approved pending-claim outcomes and cancellation classification remain unchanged.
The startup bootstrap input/delivery mismatch remains with W1/W2/W6. W8 does not
seed an alternate issuer.

## Foreign integration points

- W1/W2: local admission run-store, initial/live activity, orphan reconciliation,
  owned secret read and kernel-peer authorization adapters. W8's State and
  LocalHTTP interfaces remain the binding contracts.
- W2/W3/W9: complete native source/process/policy/log/storage services and remote
  NativeHostStore transport. W3's NewNativeHost/NewNodeInput constructors are
  implemented and consumed. Source acquisition is still a fixture in this proof.
- W9/W3: SourceAttempt transport and captured ordinal on every holder start,
  retry, report and unstarted release. W8 has not changed the foreign wire type.
- W2/W9: complete server-granted runner Labels in real award projection.
- W8/W9/W1: immutable award fence and Kubernetes Job instance in launcher sync.
  UnitID survives re-awards and NodeUID identifies the node. W1 cannot safely
  reconstruct old-versus-current awards from those fields alone.
- W2/W9: policy-owned execution resources for plan/eval awards. W8 cannot infer
  them from operator ceilings or fabricate them after award.
- W9: NewEngineClient still panics. Final runner/launcher/CLI composition keeps
  its declared transport interface rather than invoking that stub.
- T1/lead: native Cloud/Linux/Darwin/Windows and real-repository QA, plus deployed
  network enforcement and complete teardown. No new product decision is proposed.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, private cache/temp paths
and at most four package workers. Final results follow.

| Check | Result |
| --- | --- |
| Whole repository go build -p 4 ./... | Passed on final production source. |
| Focused vet across runners, engine, cluster, wingd and acceptance | Passed. Final constructor/test vet passed again after the final edits. |
| All runners/... and engine/... tests; localcontroller | Passed. |
| Full cluster suite | One metrics listener test fails because the sandbox refuses sockets. Focused CLI/bootstrap tests pass. |
| Native-store owner, partial-bind and award seeds; local composition/fence tests; pending-claim denial with race | Passed. |
| Final owner direct/embedded cases and queue-authority seeds with race | Passed. |
| FuzzStoreQueueClaimAuthorityW8, 100x, parallel=2 | Passed, 100 executions. |
| Full tests/acceptance | Failed at the existing SourceAttempt boundary, detailed below. |
| internal/acceptance | Passed. |
| commentcheck -base main . | Passed across 1,640 Go files. |
| git diff --check | Passed. |

Full acceptance fails CloudStoreBootstrapW8Components, the runner-store node
component and the lifecycle event/step cases with the missing source ordinal.
FuzzExecutorFailureMask also fails because execution returns that transition
error before reaching its expected SDK error adapter. This result establishes
neither a native pass nor an independent observed secret leak.

No new historical test port or native acceptance cell is claimed. The existing
owner component now exercises the real embedded claim path. Source docs are
updated in internal/runners/HOSTING.md and internal/wingd/LOCAL-CONTROLLER.md.

Shared files touched are the two depcache merge resolutions. Acceptance changes
are W8-owned native_store_w8_test.go and store_queue_w8_test.go. No other area's
production caller changed, and no lead-owned contract ledger was edited.

Friction: the metrics test requires a socket the sandbox forbids. Overwing
reports its installed executable behind source. No Sparkwing binary, pipeline,
Git write, deployment or release ran. The repository config has no user identity
or core.worktree override. The worker scratch directory is deleted after the
last check process joins. The lead retains merge staging/commit, independent
review, pipelines and native QA.

STATUS: CONTINUE
