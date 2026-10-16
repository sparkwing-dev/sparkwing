# W1 session 66

Store suite: **15 failures / 1,206 passing → 15 failures / 1,207 passing**.
The starting run took 215.791 seconds. The ending run took 225.209 seconds.
Both runs skipped the same 17 top-level tests. The failing set is unchanged
and remains in W1b's billing/storage split. No failing test was deleted,
skipped or weakened. The original 214-failure inventory is stale.

## Implementation and deletion

Removed `Store.AppendEvent` and its tenant-inference helper. Event appends now
require `Tenant.AppendEvent`, use the store clock and return `ErrNotFound` for
missing or foreign runs. Sequence, byte counters and the event insert still
commit together through `appendEventTx`. Profile drift retains its tenant.
Local session cleanup records through DefaultTeam and reports foreign or pruned
run outcomes as unrecorded.

Moved `ComputeLimits`, `SetComputeLimits` and `ComputeUsage` from Store to
Operator. Removed the unused `ComputeAlarmState` and single-setting
`SetComputeLimit`. Existing batch validation, zero-valued removal and rollback
remain. Removed `Operator.CountPendingNodes`. Both queue gauges now read the
ready count and partition from one `CountNodesByQueueState` result.

Production delta against starting HEAD: **-50 lines**, with **23 added and 73
deleted** under the lead's filter. Store alone is **-49 this session** and
**-3,194 against main**, still **1,056 over** the combined -4,250 budget.
No budget waiver or completion is claimed. No runtime option, schema, interface,
retry, credential or compatibility path was added.

## Tests and acceptance

The generated event property covers foreign selectors, payloads, order and
store-clock timestamps. Insert refusal rolls back counters and preserves
sequence one for the next append. Concurrent allocation passes, including the
race run. The local-doctor HTTP/client proof covers local, foreign and pruned
runs, successful and failed cleanup, and dry runs over the verified peer pipe.
Foreign event rows remain absent.

Concurrent event, reporting, backup and filter fixtures select their tenant.
Compute-limit and plan-limit fixtures select Operator. The queue partition and
StoreQueueOrder assertions use the retained shared projection. The unused alarm
helper test retires with its API. The retained cron interval test supplies a
resolved numeric GitHub identity and passes after the operator guard changes.

Whole-repository build and scoped store/controller/crons/sessionledger/
acceptance vet pass. Focused race, compute transactions, queue partition,
local-doctor, direct profile folding and operator HTTP fake-adapter tests pass.
StoreSessions, StoreTransitions, StorePlanSeals, StorePlanInsertionLimitUsesCanonicalRefusal,
StoreQueueOrder, StoreClaimMatching, StoreEnrolledClaims and StoreLauncherSync
pass. No new native acceptance cell is unskipped.

Commentcheck against main, sleepcheck against session HEAD, API snapshot,
embedded-doc drift and whitespace checks pass. The API snapshot contains only
the store surface changes. Native PostgreSQL contention, sockets, Windows and
product pipelines remain lead checks.

A broader controller probe retains one inherited failing test with three cases:
TestFinishRunFoldsPartialSamplesRaiseOnly sends an unauthenticated finish request
and receives 401 before folding. Direct folding tests pass. W2's final
EngineController.Finish has no call to foldRunProfiles. Preserve that completion
integration when porting the HTTP fixture. No authentication bypass was added.
Exact store failures and focused evidence are in
[w1-session66-store-failures.txt](w1-session66-store-failures.txt).

## Remaining work and contracts

Ordered placement, rotation grace, Cloud metering and the SyncLauncher store
fence remain implemented. Schedule/App intake still needs C4's tagged committing
origin in IntakeFacts/SealedRequest and W2's transaction-readable verified
delivery contract. AuthorityRef cannot represent an arming account or verified
delivery. Existing policy seams remain tested with fakes. No credential was
synthesized and no foreign contract was extended. Standalone/combined trigger
insertion and triggerIntake/triggerRows remain until that intake cutover.

Raw token retirement still reaches W1b's legacy credit fixtures and charging
helpers. W1b owns the 15 unchanged failures. Publisher metric binding, additive
usage report identity, maximum-concurrency/queue-depth inputs and sealed GitHub
completion facts remain open. QueuePriorityWriter still needs C1 persisted
ranking and its committing adapter.

C2's aggregate OperatorSettingsWriter remains an integration gap. This session
moves the privileged compute APIs, but does not implement its authority-bearing
transactions. Its WriteSignUpSettings argument is controller.SignUpSettingsChange,
which Store cannot import without a cycle. W2/lead must move or alias that DTO
into a neutral or store-owned contract before Operator can implement the exact
aggregate interface. Storage/quota/allowance methods still need their authority-bearing adapters.
No cross-area contract change was made.

## Shared files and friction

Production callers outside store: internal/crons/tick.go and
pkg/controller/{compute_limits,local_doctor,profiles,server}.go. Controller and
cron fixture caller changes are listed in
[w1-session66-files.txt](w1-session66-files.txt). The only W1b-file edit is the
AppendEvent caller substitution in pkg/store/storage_test.go, covered by the
lead's raw-API caller-migration exception. Its storage/accounting behavior and
other W1b files are unchanged.

C1, the port inventory, changelog, migration guide, embedded copies and API
snapshot are updated. Lead-owned handoffs and amendments are unchanged.
HEAD remains 1dd450825. No Git write ran, and local Git config contains no user
identity or core.worktree override. Scratch is removed after verification.
The shared build cache is retained.

STATUS: CONTINUE
