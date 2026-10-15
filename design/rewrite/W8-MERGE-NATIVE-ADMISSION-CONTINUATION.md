# W8 merge and native admission continuation

Continues `8821de13a` and `W8-NODE-AWARD-ADOPTION-CONTINUATION.md`.
The local-controller priority remains incomplete. The shared bindings below
still block production schedules and foreground hosting.

## Implemented

Invariant: only the freshly authenticated owner award can renew its own run
fence, and renewal reports current compute activity.

The local native binder now supplies W3's required owner-renewal callback. It
checks the actor against the refreshed award and calls `Store.RenewUnit`.
The native owner owns the keeper. A property test varies the owner, authority,
prefix, holder, generation and unit kind. A runner test reaches `NewNativeHost`
and checks one controller confirmation, owner renewals during preparation and
idle waiting, cancellation, and service closure after the owner joins.

The binder also supplies W3's `LocalAdmission` and `StoreAdmission`. W6 resolves
`machine.box_id` and its hostname default. Both admission paths use that same
identity. Local jobs enter machine admission before execution. Each machine
lease owns its connection to the elected home, with takeover and spawning
disabled. Release closes the connection. Watch failures reach the host.

`Lease.WatchControl` now returns its existing recovery error. The native lease
adapter uses it. The public watch test verifies serving errors are returned.
Existing in-memory recovery and release tests now exercise this public method.
The older `Watch` caller retains its existing error logging.

Runners now use W3's `NewRunInput` for captured seals and persisted acceptance
checks. W8's shared retry-origin validator remains on hosting, launcher claim
and bootstrap exchange. The duplicate `RunInputForAward` and accepted-plan
decoder are removed. There is no forwarding alias.

## Merge resolution and deletions

All ten initial conflict paths have resolved file contents. The two contract
files and four nodehost test files use the integration versions. The integration
scheduler deletion stays deleted. Runner behavior retains W8's native hosting
and retry validation, adapted to W3's shared input constructor.

The brief prohibits git writes. Nothing was staged or committed, so Git still
lists these paths as unmerged until the lead stages the resolved files and the
scheduler deletion. No conflict markers remain in those contents.

The merged tree retained `internal/nodehost/plan_document.go`, whose session
implementation the integration branch deleted. It had no caller. Removing its
89 production lines restored the whole-repository build without restoring the
retired session. The remaining legacy resume-drift test is ported to the shared
owner/store fixture. It refuses changed acceptance before acquisition and keeps
held and terminal rows. Shared executor drift tests cover reconstructed SDK
plan mismatch before a body starts.

## Remaining contracts and real integration

| Owner | Required binding or contract |
| --- | --- |
| W2/W1 | Persisted `CredentialPeer` authentication. `authenticationAuthorityTx` still supports bearer and session only, so W8's peer claimant cannot obtain queue authority. |
| W2/W9/W1 | Local schedule registration with checkout, captured environment and proof choice, plus pin/unpin and tick operations and durable environment association. `ScheduleRepoRequest` carries a clone URL. The declared schedule manager has no local pin or tick operations. W8's lifecycle and timer factory remains implemented. |
| W3/W9 | The full HTTP-backed `NativeHostStore` for foreground runs. `RemoteOwnerStore` supplies scheduling, reconciliation and expansion, while native hosting also needs persisted seals, transitions, completion and execution state. |
| W1/W2 | Attempt-zero claim lifetime across application attempts. `TestLocalNodeAuthorityW8SurvivesStart` still fails with credential attempt 0, live attempt 1, and `credential is not live`. |
| W3/W7/W1/W2 | Remaining owner policy, authorized bytes/records/output, controller log projection and ephemeral process relation bindings. |
| W1/W2 | Persisted launcher synchronization. W8's immutable award and Job-generation correlation remains complete. |
| Lead/T1 | Native Cloud isolation, enforced egress, aggregate scratch, DinD teardown and Windows/Darwin execution evidence. |

No foreign contract was changed to invent these missing operations. Those pieces
remain stopped at their declared boundaries, with component fakes retained.

The final W9 `rewrite_integration` probe passes the local controller constructor.
Local run reaches socket dialing, refused by the sandbox. Local schedules still
refuse missing checkout/environment/pin registration. Constructor success does
not establish sustained worker readiness. No native acceptance cell is newly
closed. The seven `TestNativeStoreOwnerW8` modes pass with declared service fakes
and real shared hosts/store transactions.

The 12:35 retry work, A1-38 and the four dead contract tables remain complete.
A1-39/40/48 advance through owner renewal and machine admission. The remaining
per-row dispositions in the preceding W8 reports still apply, including native
A1-42/53 and foreign enrollment, storage and transport obligations.

## Validation

All Go checks use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and GOTMPDIR=/tmp/sw-astra-W8/go. Tests use at most -p 4.

- Whole-repository `go build ./...` passes.
- Full short runners, launcher, runner service/unitfile, localcontroller and
  nodehost suites pass. Engine/sdkinstall and cluster suites also pass.
- `tests/acceptance -run 'W8|LauncherCloud'` passes after the final changes.
- Focused CLI/local-runtime/lifecycle/schedule composition tests pass.
- Focused race checks pass for native owner completion, renewal, admission,
  public lease watching, recovery and release.
- The bounded owner-renewal property executes 9,972 fuzz cases. The shared-input
  retry property executes 1,733. Both pass.
- Scoped vet and `commentcheck -base main .` pass. Whitespace checks pass.
- A broader acceptance-package run was interrupted after the W8 and nodehost
  package results. It is not reported as a passing acceptance suite.
- The production probe and claim-attempt failure above remain failing evidence.

Reproduce the focused acceptance check with the stated environment and:

```
go -C /home/bombuntu/.xwing/envs/1086/sparkwing test -p 4 ./tests/acceptance -run 'W8|LauncherCloud' -count=1
```

No pipeline, deployment, candidate installation or release ran. Worker scratch
is deleted after checks join. `/tmp/sw-gocache` is retained.

## Scope and volume

Worker edits outside W8 are limited to:

- `cmd/sparkwing/local_host.go`: one composition line passes the installed
  version to the local native binder. This is the only worker CLI production edit.
- `internal/nodehost/plan_document.go`: delete the unused 89-line legacy planner
  required to compile after the integration session cut.
- `internal/nodehost/resume_drift_test.go`: port the leftover legacy test.
- `tests/acceptance/native_store_w8_test.go`: migrate the renewal fixture to the
  shared owner's callback and remove its retired store-renewal wrapper.
- The initial contract/nodehost conflict paths described above.

There is no worker edit in `internal/orchestrator`. Other staged CLI and W3
changes arrived from the integration merge.

W8 production Go lines this session: **109 added, 68 removed, net +41**.
Against `main`: **10,217 added, 7,063 removed, net +3,154**.
That is **5,454 above the -2,300 budget**. Counts exclude tests, testdata,
acceptance and contract declarations, and include the two new untracked
production files. Worker production outside W8 is **net -89**. The CLI line
replacement is +1/-1.

The budget remains unmet. Existing engine/version, shared runner, transport and
service overage justifications remain in the preceding reports. This session's
admission adapter is needed because W3's port returns an abstract lease while
the admission client returns its concrete lease. The adapter carries the token,
release and error-returning watch without adding another scheduler, policy,
timer, setting or store owner. Removing it would leave machine admission
unbound. The renewal callback is required by the amended shared-host constructor.
These are additions for the lead's budget decision, not a waiver.

Friction remains the missing foreign contracts, socket restrictions and the
reproduced claim-attempt defect. Git configuration has no injected
`core.worktree` or test identity. No harness memory was written.

STATUS: CONTINUE
