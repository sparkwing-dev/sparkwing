# W9 placement validation

The local request originates in `cmd/sparkwing/run_prepare.go` with local origin
and host dispatch. `internal/wingd/localcontroller/native.go` reads that sealed
request but left `NativeHostConfig.Validation` empty. `NewNativeHost` carried
the empty placement into initial planning, execution replay and evaluation.
The shared validator then returned `invalid plan at plan: unknown placement`.

Native composition now sets placement through `store.RequestPlacement`, also
used by controller intake, store acceptance and expansion acceptance. That
classifier now returns `PlacementLocalTriggered` for triggered requests with
local execution location or a local checkout. Existing precedence for publish,
controller dispatch, here, direct local runs and Actions stays intact. The
capability table already supports all six placements. Unknown placement remains
an error in the shared validator.

`TestNativeServicesValidatesPlansForEveryLaunchPlacement` exercises native
composition, `NewNativeHost`, `PlanInitial` and the real validator for A, B, C,
D, F and G. It uses generators, approval conditions and box concurrency. The
listener is supplied without opening a socket. All six cases first failed with
the reported unknown-placement error and pass with the fix.
`TestAcceptPlanUsesEveryRequestPlacement` also passes all six requests through
store admission and acceptance with the real shared plan validator. Its
authority setup and intake sealing use existing store fixtures.

Verification used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache` and `GOTMPDIR=/tmp/sw-astra-W9/gotmp`.

- Root and `.sparkwing`: `go build -p 4 ./...` and `go vet -p 4 ./...` pass.
- Focused store, localcontroller and nodeproto tests pass with `-p 4`, selecting
  `TestAcceptPlanUsesEveryRequestPlacement`, `TestNativeServices`,
  `TestNativeOwner`, `TestPlanCapabilityLaunchParity`, `TestValidatePlan`,
  `TestExpansion` and `TestAcceptance`.
- Commentcheck against main, embedded documentation drift and whitespace checks
  pass.

The rebuilt CLI ran `sparkwing run example -o plain` from this checkout with
isolated HOME, state and config under task scratch. It stopped before intake:
`cannot start local controller: ... dial unix /tmp/sparkwing-1000-0a84ded087fd/d.sock: connect: operation not permitted`.
This is the next observed execution failure in the sandbox. No later product
failure is established. Xwing candidate inspection also failed because its
session-index lock is outside the writable roots, so the CLI was built directly
under task scratch. The lead needs a socket-capable run to continue native QA.

Shared files touched are `internal/wingd/localcontroller/native.go`, its
`native_config_test.go`, `internal/store/plan_contracts.go` and the new
`internal/store/request_placement_test.go`. W9 documentation and its embedded mirror
describe the shared placement validation. No acceptance cell was newly
unskipped and no archived test was ported. No foreign contract change or new
mechanism is needed. Open product questions for this fix: none.

Production changes are +6/-2 lines, net +4, including formatting. No legacy
implementation was replaced or deleted. W9-owned production Go versus main is
+16,916/-23,812, net -6,896, within the -2,900 budget. Repository Git metadata
was not written. No task-owned process remains. Task scratch is removed after
verification, and the shared Go cache is retained.

STATUS: COMPLETE
