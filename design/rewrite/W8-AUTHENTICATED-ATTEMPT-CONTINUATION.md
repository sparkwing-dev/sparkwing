# W8 authenticated attempt continuation

Continues `31c95e546` and `W8-MERGE-NATIVE-ADMISSION-CONTINUATION.md`.
The local-controller priority remains incomplete.

## Implemented and deleted

Invariant: an attempt-zero node award retains its claim identity across execution
attempts, while every other award field and every fixed attempt stays fenced.

C2 now projects the current attempt during authentication. W8's award comparison
accepts that projection for an attempt-zero node claim. It still rejects changed
holders, generations, runs, jobs, capability prefixes, units and fixed attempts.
The new property failed on the previous W8 comparison and passes after the
three-line change. It ran 1,000 generated cases.

The local node tests now use the shared authenticator directly. The custom
claim-spanning test authenticator and reader are deleted. Their replacement
retains the real C2 attempt projection with fake snapshot and relation readers.
The node-fence property also ran 1,000 generated cases.

`TestLocalNodeAuthorityW8SurvivesStart` is removed from the
`rewrite_integration` build tag and passes in the ordinary acceptance selection.
It checks both the immutable persisted attempt zero and the authenticated live
attempt. `TestStoreHostingW8RefreshesNodeAwardAfterStart` starts a real stored
claim inside W8's service binder, refreshes its authority through C2, and checks
partial-binding cleanup.

`TestStoreLauncherW8` uses the production persisted sync reader. Its old reader
fixture is deleted. The test reaches the typed HTTP client, C2 handlers, store
claim, bootstrap exchange, node start, live synchronization, committed completion
and Job retirement. Kubernetes and the Cloud isolation boundary remain fakes.
This closes the previously open W1/W2 persisted-sync integration point for A1-38.
The fake sync reader remains only in the independent transport-refusal property.

The two fixture reductions remove 65 test lines and add 30, net -35. No whole
production file or legacy production API is deleted in this session. The prior
dispatcher, trigger-consumer and duplicate source/host cuts remain deleted.
No additional T1 inventory test was ported. These are component acceptance
advances, not new native-platform placement claims.

## Priority blockers and contract changes needed

| Owner | Concrete remaining boundary |
| --- | --- |
| W2/W1 | Verified local peers authenticate for reads, but their snapshot has no persisted authority reference. `Claimant.Bearer` reaches shared delegation and fails with `invalid authority reference`. Supply the transactional local authority/issuance binding through C2/C1. W8 does not mint a root or insert a credential outside that policy. |
| W2/W9/W1 | Local registration needs an authorized checkout, captured environment, proof choice and pin association. `ScheduleRepoRequest` carries a clone URL, and `ScheduleManager` has no pin or tick operation. Supply the local operations and their durable state contract. W8's lifecycle and native timer factory are already implemented. |
| W3/W2/W9 | Foreground hosting needs authenticated `ReadSnapshot`, `ReadNode`, `TransitionRun` and `TransitionNode`. The CLI adapter and `NativeHostStore` interfaces exist, but their private remote transport is absent. `RemoteOwnerStore` supplies scheduling/reconciliation/expansion only. |
| W3/W7/W2 | Native owner policy, authorized bytes/records/output, controller log projection and ephemeral process relations still need shared service bindings. The runner/Cloud executable also needs its production factories supplied to `MainWithRuntime`. |
| Lead/T1 | Native API sockets, Windows/Darwin, Cloud egress/isolation, aggregate scratch accounting and privileged sidecar teardown still need execution evidence. |

`TestLocalClaimantW8UsesPersistedPeerAuthority` is a new socket-free
`rewrite_integration` probe in the owned controller package. It reproduces the
first blocker against a fresh real store. It remains red under that tag.

The unmodified W9 `TestReviewProductionExecutionBindings` probe reports:

- Local controller construction passes.
- Local run reaches admission socket dialing, refused by this sandbox.
- Local schedules fail with `local schedules require checkout, environment and pin registration`.

Constructor success does not establish a functioning queue. The local-run probe
uses detached mode and does not establish the foreground binding. No foreign
contract shape was changed to bypass these missing operations.

## Remaining amendment dispositions

All 26 W8 participant rows in `amendments-1.md` retain the previous report's
disposition, with these updated integrations:

| Rows | W8 disposition |
| --- | --- |
| A1-08, A1-09 | Shared mixed claim and launcher route selection remain implemented and tested. |
| A1-10, A1-11, A1-12 | Selected-engine installation, sealed source and shared process environment consumers remain implemented. Production source authorization and platform execution remain shared integrations. |
| A1-18, A1-19 | Current-attempt authentication now reaches real stored start and W8 host refresh. Granted runner/engine construction and its existing tests remain intact. |
| A1-20, A1-26 | Shared issuance/enrollment is used. Local peer delegation remains blocked as above. |
| A1-27 | W8 uses shared claim/transition billing. No runner-side billing implementation is added. |
| A1-36 | Native byte/record bindings remain open. |
| A1-38 | W8 award, bootstrap and immutable Job/generation correlation are implemented. Persisted controller sync now passes through the real reader. Native Kubernetes proof remains open. |
| A1-39, A1-40 | Shared loop, native hosts and elected controller lifecycle exist. Production local execution still needs the bindings listed above. |
| A1-41 | Captured-home foreground managed services and native timers remain implemented. |
| A1-42 | Pod construction is implemented. Deployment enforcement/teardown proof remains open. |
| A1-43 | Signed engine policy, verified resolution/fallback and historical asset support remain implemented. Packaging/native evidence stays separate. |
| A1-45, A1-46, A1-48 | W8 consumes shared settings/environment builders and captured snapshots. Foreground and scheduled execution still need production bindings. |
| A1-49 | Credential-file startup consumers remain implemented. Enrollment and executable factories remain shared composition work. |
| A1-50 | CLI host/cache/publication adapters remain W9/shared integration work. |
| A1-51 | Prior runtime fixture/consumer cuts remain removed. |
| A1-52, A1-53 | Native platform/release and complete placement evidence remain open. |
| A1-55 | Named here claim/reclaim consumers and the no-replay behavior remain implemented. |

The 12:35 retry-origin rule, the approved pending-claim outcome/cancellation
designs and the four dead contract-table deletions were already completed.
Their tests pass without rebuilding those mechanisms. Lead-owned handoffs and
amendments are unchanged.

## Verification

All Go commands use `GOWORK=off GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, `GOTMPDIR=/tmp/sw-astra-W8/go`,
`TMPDIR=/tmp/sw-astra-W8/tmp`, `GOMAXPROCS=4` and at most `-p 4`.

- `go build -p 4 ./...` passes for the whole repository.
- Full short `internal/runners/...` and `internal/wingd/localcontroller` suites pass.
- `go test -p 4 ./tests/acceptance -run 'W8|LauncherCloud' -count=1` passes.
- Focused CLI local-service, native owner composition and schedule-factory tests pass.
- Scoped vet on runners, localcontroller, acceptance and CLI passes.
- Both 1,000-case fuzz runs above pass.
- Focused race checks pass for runners, localcontroller and W8 acceptance.
- `commentcheck -base main .` passes across 2,258 files.
- The tagged claimant and W9 production probes retain the failures listed above.

The focused race selection covers the claim identity property, local node
adoption, concurrent adoption, persisted node start, W8 refresh and persisted
launcher synchronization.

No pipeline, candidate installation, Sparkwing executable, deployment, release or
Git write ran. Internal source docs changed with these boundaries. Public CLI
grammar, wire fields and configuration are unchanged. No generated docs include
the three edited internal source documents. Scratch is removed after all checks
join. The shared build cache is retained.

## Scope and volume

Production edits are entirely in W8's `internal/wingd/localcontroller/store_host.go`.
There are **no `cmd/sparkwing` composition edits this session**, no other
production caller migrations, and no edits in `internal/orchestrator`.
Shared test edits are limited to:

- `tests/acceptance/local_node_authority_w8_test.go`
- `tests/acceptance/store_launcher_w8_test.go`

W8 production Go lines this session are **+3/-0, net +3**.
Against `main`, W8 is **10,234 added, 6,799 removed, net +3,435**.
The starting merged HEAD was +3,432. These counts exclude tests, testdata,
acceptance and contract declarations. Production outside W8 is **net 0**.

| W8 path | Net production lines versus main |
| --- | ---: |
| internal/cluster | -1,967 |
| internal/runners | +1,397 |
| internal/wingd | +1,748 |
| internal/engine | +2,257 |
| Total | +3,435 |

W8 remains **5,735 lines above its -2,300 budget**. No budget waiver is claimed.
The exact-engine distribution/version-policy work added by decisions 102-103 is
2,257 net lines and has no separate row in frozen section 0.1. Removing it would
drop required signature verification, native pin handling, SDK installation or
staged upgrades. Controller lifecycle/HTTP, Windows peer transport and managed
service composition account for further additions described in previous reports.
The retained admission model is explicitly unchanged by decision 91. None of
those capabilities can be removed to meet the budget. Further simplification or
an explicit lead budget decision remains required.

Mechanisms added this session: none. The change adapts the existing identity
comparison to C2's current-attempt projection. No new environment variable,
option, timer, root issuer, state owner or compatibility surface is added.

Friction remains the missing shared local mutation/schedule/private-state
contracts and the sandbox's socket restriction.

STATUS: CONTINUE
