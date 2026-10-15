# W3 unused executor cut

<!-- design:begin -->

Priorities 1 and 2 remain green. The full nodehost suite passes against the
Tenant/Operator store, and the concurrency mismatch-refusal checks pass.
NativeHost still installs real tenant admission. This session adds no admission
fake or alternate persistence path. Priority 3 remains incomplete.

## Deleted and retained

Removed the unused `RunNodeOnce` API, its `RunNodeOption` surface, the write-only
claim-holder context, and its duplicate remote acquisition and binary builder.
No production caller referenced `RunNodeOnce`. The removed builder included the
retired remote binary download/upload path. The native source and claimed-host
paths already own acquisition for the shared runner.

The live trigger/pod state helper moved unchanged into `trigger_host.go`.
`AdmitTriggerSource`, `TriggerSourceURL` and their refusal sentinel moved into
`trigger_source.go` because the trigger consumer still calls them.

Production deletion is **349 net lines**: 442 removed and 93 added, including
those moves. W3's measured production delta against `main` is **+5,489 lines**,
excluding tests, testdata, acceptance and contract files. That is **7,789 lines
over** the -2,300 budget. This is an incomplete cut, not a justified overage.
Session, Server, the old scheduler/layers and the live local/trigger/pod callers
still exist. This session deletes none of those live implementations.

## Retained test ports

- `TestRunNodeRemoteCancelAbortsSourceFetch` becomes
  `source.TestPrepareCheckoutCancellationStopsCredentialRelease`. It blocks the
  source-access credential callback, cancels acquisition, waits for its return,
  and requires no entry or workspace. The source implementation is real. The
  credential controller is a fake, and this does not establish HTTP or git-child
  cancellation coverage.
- Repository refusals use the existing direct admission table. The uncheckable
  source matrix now calls `TriggerSourceURL` and `AdmitTriggerSource`, retaining
  missing-repository and working-tree consent cases. The duplicate unlisted-repo
  assertion is removed. There is no remaining removed executor to enter fetch.
- The `shouldRunRemote` test retires with that deleted selector. The old
  sourceless `RunNodeOnce` fixture retires with its API. The native
  `TestSourceProjectionCannotInventLocationFromDisplay` and
  `TestPrepareCheckoutRequiresSealedSHAAndAuthorizedCredential` retain the
  source-location and remote-commit refusals.

No acceptance cell is added or activated. No native placement is newly proved.

## Remaining contracts and implementation

The remaining live cut still needs W2/W9's authorized persisted execution
snapshot. `Controller.ReadSnapshot` returns only `ScheduleSnapshot`. Native
OwnerStore/ClaimStore need the sealed request, accepted spec/plan, owner,
complete execution rows and accepted expansions. Display reads are insufficient.
The remote run-start operation corresponding to
`Tenant.TransitionRun(RunStart)` is also absent. Capacity/adoption and
owner/process delegation still need authenticated remote adapters.

Local RunHosted still receives legacy backends and invocation fields. It needs
an admitted RunInput and native composition. No authority was inferred from
those legacy fields. The previous W1 admission/approval, debug-persistence and
interrupted-snapshot findings remain open. W3 still owns concrete service/record
wiring, memo/retry selection, retained remote local semaphores and placement
composition. The earlier amendment audit remains in force. This deletion closes
no complete amendment or live production placement.

No foreign contract changed. Lead-owned handoffs and amendments are unchanged.
The only shared edit is `cmd/sparkwing/profile_guard_test.go`, which drops the
removed API from its secret-source exception table. No foreign production caller
changed. No mechanism, setting, limit or compatibility path was added.
There is no product docs impact because the removed internal API has no production caller.

## Verification and friction

Checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, and scratch below /tmp/sw-astra-W3.

- Whole-repository `go build -p 4 ./...` passes.
- Vet passes for nodehost, lease, source, orchestrator and cmd/sparkwing.
- Full nodehost, nodeproto, lease and source suites pass. Nodehost takes 22.2 s.
- Source-cancellation and repository/source-admission ports pass under race.
- Both CLI profile guards affected by the removed exception pass.
- Commentcheck against main and `git diff --check` pass.

The live legacy trigger-host regression still fails before execution with
`no such table: tokens`. This confirms the remaining migration gap. A broader
CLI profile-test selection also finds unrelated W9 fixtures whose responses
omit `team`: `TestProfilesRemoveRevokesOnlyMemberCredential` and
`TestProfilesRemoveRetainsConnectionChangedDuringRevocation`. They are unchanged.
The focused guards pass. Full orchestrator and CLI suites are not claimed green.

Bootstrap discovery invoked installed `sparkwing info --for-agent` once.
No candidate, stateful Sparkwing command, pipeline, install, release or Git write
ran. Scratch is removed after checks join. The shared build cache is retained.

<!-- design:end -->

STATUS: CONTINUE
