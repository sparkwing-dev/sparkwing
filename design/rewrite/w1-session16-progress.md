# W1 continuation

Starts at 2a55c7828 with an integration merge in progress. W1 remains incomplete.
The lead owns review, Git writes, pipelines and landing. W1b files are unchanged.

## Merge

Resolved the content conflict in contracts/handoffs.md by preserving both sides.
The integration side adds W1b authorization/catalog/cutover instructions and W8's
pending-claim decision. The W1 side records completed concurrency maintenance and
the launcher-sync contract gap. No amended signature was replaced. Conflict
markers are gone. The index still reports UU because SWARM-BRIEF forbids Git
writes. The lead must stage the resolution and finish the merge.

## Implemented

Invariant: a holder report affects only the attempt it names, and replay cannot
consume another attempt. C1 now adds NodeTransition.SourceAttempt, a required
nonnegative ordinal pointer for holder start, retry, report and unstarted release.
An explicit zero names the state before first execution. A resumed start names
the ordinal retained through disruption. Start and retry return the next ordinal
in Applied.Attempt. Unstarted report/release consumes none. Other actors and
causes must omit the pointer.

The ordinal participates in the existing transition digest. The committing
transaction accepts an exact replay of its last transition, or checks the source
against the locked row's current ordinal before mutation. It never derives a
reported ordinal from current authentication state. Repeated identical failures
therefore advance distinct attempts, while delayed or future reports cannot
finish a newer attempt under the same claim generation. Internal unstarted-unit
release supplies the ordinal from the row it already locked.

This is a local C1 contract correction under SWARM-BRIEF's own-area rule. No
foreign Go contract, schema, billing policy, quota or customer limit changed.
C1, T1 and the generated public API snapshot describe the final local contract.

## Acceptance and test ports

The existing recovery-application-retry-ordinal/store-sqlite cell now executes two
identical failures followed by success. All three attempts and the exact four
events survive reopening. It also checks retry replay after reopening, delayed
failure and success refusal, unchanged claim/lease/generation, terminal replay
and zero disruptions. It remains store-boundary evidence. No new native cell is
claimed.

TestHolderReportsAffectOnlyTheirSourceAttemptProperty generates 32 sequences with
two through six identical retry failures, varied replay counts, and absent,
negative, stale and future source ordinals. Refused reports must leave the whole
node unchanged and add no events. Separate tests cover first and resumed starts,
unstarted reports/releases, exact replay and omission by non-holder actors.

The session-12 retry probe is now covered by the enabled property and acceptance
cell. Deleted its 15-line archived source. Deleted **0 legacy production lines**.
No new archived T1 source-function port was completed. Prior ports remain active.

## Required foreign contract and caller changes

W9 must add `SourceAttempt *int64` with JSON `source_attempt` to
controllerwire.JobTransitionRequest and copy it unchanged in
nodeTransitionForStore. Presence matters because initial start supplies zero.
W3 must capture the pre-edge ordinal, send it on holder execution transitions,
and consume Applied.Attempt after start/retry. Re-reading the current ordinal at
report time would defeat the fence.

The unchanged controller tests TestEngineJobLifecycleRenewsAndReplaysUnderStoreFences
and TestEngineOwnerReconcilesExecutesAndFinishes now fail at their first start
because the existing wire cannot carry the required ordinal. They are neither
deleted nor skipped. W1b must add `SourceAttempt: &a.Attempt` to blobResultFixture
in internal/store/blob_result_records_test.go. Its unchanged
TestBlobResultRecordsCommitAndReplayWithTerminalState fails for the same missing
input. W1b files were not edited.

A temporary Go overlay supplied exactly those foreign caller changes, plus
explicit start zero and report one in the two controller fixtures. With that
overlay, all TestEngine and TestBlobResult tests pass. This probes the proposed
seam, not the unchanged production transport. Reproduce by copying these four
files beneath /tmp/sw-astra-W1, applying the following substitutions, and mapping
each original absolute path to its copy in a Go overlay's Replace object:

| File | Probe substitution |
| --- | --- |
| internal/controllerwire/contracts.go | Add `SourceAttempt *int64` with JSON `source_attempt,omitempty` to JobTransitionRequest. |
| internal/controller/engine_store.go | Copy `body.SourceAttempt` into NodeTransition.SourceAttempt. |
| internal/controller/engine_store_runtime_test.go | Supply pointer-to-zero for the two starts and pointer-to-one for the report bodies. |
| internal/store/blob_result_records_test.go | Add `SourceAttempt: &a.Attempt` to blobResultFixture's transition. |

Run the environment below with `go test -p 4
-overlay=/tmp/sw-astra-W1/contract-overlay.json ./internal/controller ./internal/store
-run '^TestEngine|^TestBlobResult' -count=1`.

## Amendment and handoff audit

Earlier finished work is retained. The uncompleted groups remain explicit.

| Entries | State and remaining integration |
| --- | --- |
| A1-07, A1-44, A1-45 | Store acceptance and expansion rerun shared resolution and preserve secret classification. Trusted checkout/intake providers still need production wiring. |
| A1-08, A1-18, A1-55 | Mixed-kind matching, push ancestry, zero initial ordinal and here claim/reclaim rules remain implemented. This continuation finishes C1's source-attempt enforcement. W9/W3 transport is the pending handoff above. |
| A1-11, A1-20, A1-25, A1-26 | Store source-location seals, provisioning, plan-secret and enrollment interfaces remain implemented with contract-fake evidence. W2 owns trusted providers and installation. |
| A1-13 through A1-17 | Reconciliation, recorded failure, cancellation and persisted expansion projection remain implemented. Materialized-record copying still has no installed implementation. W1b supplies SetResultBlobRecords for result verification. Failure-stage transport, GuardRefusal, materialized-result identity and generation-zero generator transport remain shared gaps. |
| A1-21 through A1-24 | Cancellation intent, digest sessions, CSRF sealing, ancestry/binding and audited source release remain implemented. C2 BindingState still lacks cancellation reason. Result digest/operation transport, concurrency-key selectors, teamless sessions and trusted source/origin adapters remain integrations. |
| A1-27, A1-28 | Committed-checkout settlement remains implemented. W1b owns accounting and the lead's lifecycle-wiring assignment. C2 SettleBilling remains a stub. Metered claims still refuse rather than inventing pricing or customer-wait policy. |
| A1-29, A1-31 through A1-35 | Blob/upload/record/log persistence and accounting stay with W1b. Its result-fixture caller update is listed above. No W1b implementation changed. |
| A1-38 | SyncLauncher still needs immutable award-generation and Kubernetes Job-instance correlation from W8/W9. Stable UnitID plus NodeUID cannot identify a replaced award's Job. |
| A1-40, A1-53 | W8/T1/lead retain LocalRuntime readiness, real HTTP/api.sock, provider, PostgreSQL, native platform and real-repository evidence. |
| A1-51 | Legacy Store.AcceptPlan/validator, StateStoreOld, claim/trigger writers and readiness paths remain. The old planning caller is planLaunchedRun through Client.SubmitPlan. Its final engine client is a stub. This continuation does not remove that caller or claim its retirement. |

The new lead handoffs for W1b's mutation owner, catalog edits and billing cutover
belong to W1b. The W8 pending-claim handoff belongs to W8. W1's cancellation,
session, unit-reaper and concurrency-maintenance handoffs remain finished.
No new human decision is requested. The source-attempt wire amendment is needed
before this change can land with the controller suite green.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W1/cache, GOTMPDIR=/tmp/sw-astra-W1/tmp and
TMPDIR=/tmp/sw-astra-W1/tmp. Package parallelism is at most four.

- Whole-repository go build ./... passes before and after implementation.
- Scoped go vet passes for internal/store/..., internal/controller/..., internal/acceptance/...
  and tests/acceptance/....
- The W1 selection passes **335 top-level tests / 1,007 cases**, without skips.
  Extract Test function names from w1-session16-tests.txt, omit names beginning
  TestPostgres, and pass their anchored alternation to go test -json -p 4 ./internal/store.
- tests/acceptance with `-skip '^TestProtocol$'` passes. Recovery and placement also
  pass separately. Required skeleton and platform skips remain.
- commentcheck -base main is clean across 1,025 Go files. sleepcheck -base HEAD
  and git diff --check pass. Changed Go files are formatted.
- The two controller tests and W1b result test named above fail without the
  foreign caller amendment. All TestEngine and TestBlobResult tests pass under
  the four-file contract overlay. No full store/controller suite pass is claimed.
- cmd/apidiff generated the store snapshot into scratch. Only pkg_store.txt was
  copied back, with the SourceAttempt field as its sole generated change.

No PostgreSQL, race, lint, pipeline, provider, native placement or independent
review result is claimed. No Sparkwing binary, Git write, release, deployment,
service or harness-memory write ran. Writes stayed inside the worktree and its
designated scratch directory. Local Git identity and core.worktree have no
overrides. Scratch is removed before reporting.

## Shared files and friction

The only production caller migration is internal/store/unit_release.go. Direct W1
store tests supply explicit source ordinals. Shared acceptance callers are
internal/acceptance/store_capacity.go, store_recovery.go, store_retry.go,
store_started.go and store_unstarted.go. Shared documentation is c1-data.md,
handoffs.md, t1-acceptance.md, t1-port-inventory.md and .apidiff/pkg_store.txt.

The first property run failed compilation before the C1 field existed. The first
acceptance run caught a resumed-start caller incorrectly reusing zero. It now
supplies the retained pre-start ordinal. Foreign request/fixture omissions are
the confirmed integration failures above. No current-ordinal inference or
compatibility fallback was added to bypass them.

STATUS: CONTINUE
