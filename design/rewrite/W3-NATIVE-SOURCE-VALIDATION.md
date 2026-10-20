# W3 native source validation

The five source-selection review fixes are committed in 0d8121455. This session
fixes the two failures reported by W3-native-latest.md after that commit. Both
failed locally before their fixture changes and pass afterward.

## Changes

- The queue-cache fixture now captures its environment into the host's private
  snapshot directory and stores its seal on the request. Trigger and node
  claims can open the profile cache using the captured environment. The existing
  assertion still verifies cache binding, credential issuance/revocation and
  joined host cleanup for both claim kinds.
- The local schedule transport fixture uses an isolated HOME and XDG config
  directory. Its fake remote has a numeric documentation address, so source
  validation needs no public DNS. The Git shim maps that address to a local
  repository. The HTTP test deadline is 30 seconds because selecting configuration
  for all 21 declarations takes more than five seconds. Its separate five-second
  concurrency-operation deadline is unchanged. The response must contain all
  21 schedules.

The invariant remains that preparation uses its supplied environment. Production
code and timeouts are unchanged. No ambient environment fallback was added.

## Verification

All Go commands use this worktree through `go -C` and these environment values:

```text
GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache
GOTMPDIR=/tmp/sw-astra-W3/gotmp TMPDIR=/tmp/sw-astra-W3 GOMAXPROCS=4
```

Recreate the scratch directory before reproducing these commands:

```text
go build -p 4 ./...
go vet -p 4 ./cmd/sparkwing ./internal/controller
go test -p 4 ./cmd/sparkwing -run '^(TestLocalQueueCacheDoesNotDependOnOwnerWinningJobClaim|TestProfileCache|TestWorkingTreeTriggerPreservesNamedReferenceIdentity|TestLocalEntryPointsWithholdSigningMaterial|FuzzPreparationGitNeverRestoresSigningMaterial)' -count=1
go test -p 4 ./internal/controller -run '^(TestLocalScheduleTransportRegistersPinsTicksAndDeletes|TestLocalControllerScheduleFiresOneSealedRunAtDueInstant|FuzzLocalSchedulesRequireVerifiedTransportPeer)$' -count=1 -timeout=1m
go test -p 4 ./internal/source ./internal/doccheck -count=1
go run ./internal/commentcheck -base main .
```

All passed. The CLI group took 12.698 seconds, the controller group 6.063 seconds,
the source suite 16.315 seconds and doccheck 1.180 seconds. Commentcheck was clean
across 2,902 files. The passing regression group covers the global fsmonitor
callback, withheld signing material, named working-tree selectors and the
persisted queue environment. Source tests cover selection/acquisition parity for
contained symlinked configuration. Doccheck covers standalone command lines.
Go formatting and diff whitespace checks pass.

No acceptance cell or skip changed, and no archived test needed porting. Native
socket tests and the whole-repository suite remain with the lead. The current
native report still contains FAIL for cmd/sparkwing and internal/controller. Those
entries cannot be cleared by this sandbox's focused runs. The lead must commit
this diff and rerun native validation before W3 can report COMPLETE.

## Scope and handoff

Shared files touched are cmd/sparkwing/local_profile_queue_test.go and
internal/controller/schedules_local_test.go. The earlier source preparation report
now points here. No API removal, caller migration, contract change, new mechanism
or production deletion was needed. Lead-owned amendments and handoffs are
unchanged. Their W3 implementation assignments remain covered by the existing
continuations. No source implementation stub blocks this slice.

Session production delta is zero. W3 production against main remains
+12,475/-26,120, net -13,645 lines with the earlier budget exclusions. This is
below the -2,300 target. Production deletions this session: zero lines.

Friction: a fake Git remote does not bypass the real source URL's DNS validation,
and configuration materialization for 21 pipelines exceeded the fixture's old
deadline. No production limit changed. Scratch is removed at session end and
/tmp/sw-gocache is retained. No Git writes ran.

STATUS: CONTINUE
