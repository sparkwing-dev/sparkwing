# W9 child intake, output publication and diagnostics

Base: `63c94f010`. Environment: `1087`. No Git writes.

- Child intake now resolves a captured default owner timeout against the current
  operator default. A local parent's default zero no longer becomes an explicit
  remote zero. Explicit flag values still retain their meaning and validation.
  The real children route reproduced HTTP 500 with `owner_execution (default):
  remote owner duration must be positive`; it now creates and persists the child
  with the six-hour default. Settings tests cover captured zero/nonzero defaults,
  operator overrides and explicit zero refusal.
- Build's recorded failure was `job output requires a record publisher`.
  Native services now bind the existing `OutputRecords.PublishOutput` to the
  current attempt's authenticated storage/upload identity. The two-job native
  integration test now emits SDK result outputs and verifies committed record
  selectors under independent node awards. It reproduced that exact failure
  before the fix and succeeds afterward through the real controller and store.
- Cause capture moved from the controller into `apierr`; controller error
  writers and signed filesystem uploads share the same first-cause recorder.
  Signed PUT failures previously logged only `Internal Server Error`. A real
  signed-route property test now verifies the masked storage cause appears once
  with the request ID and does not leak into the response.

The retained native evidence corrects the snapshot diagnosis. In
`/var/tmp/sw-lead-tmp/e2e-home.r4no/.sparkwing/wingd/d.log`, request
`adcc645e9dd9cb22eda6a7e3` already logs HTTP 500 with `context canceled`;
`f26babd511f21663a28cc489` logs HTTP 503 with `authentication service
unavailable` and `context canceled`. The build failure event in that home's
store records the missing output publisher after all eight build steps passed.
Copies of the native databases were inspected; the originals were untouched.

The real local-host snapshot route now has regressions for a store decoding
failure, cancellation after authorization (500) and cancellation during
authentication (503). Each records its cause once with the request ID. These
cancelled polling requests are separate from build's output-publication failure;
their status handling remains unchanged.

Passed: `go build ./...` and `go vet ./...` in both the root and `.sparkwing`
modules; full settings, apierr, blob, localcontroller and controller/client
suites; focused controller local-host, audit and masking properties;
commentcheck against main; diff whitespace check. The native integration uses
real HTTP over `net.Pipe`, SQLite, SDK subprocesses and signed storage routes.
No acceptance cells were newly unskipped and no archived tests were ported.

Next observed failure: the public production-composition probes reach local
controller startup but Unix socket listen/connect returns `operation not
permitted`. The on-demand run probe skips at its socket preflight. A native
`sparkwing run example` remains necessary to identify the next product failure.
No operational Sparkwing command beyond the required discovery ran. The process
sweep found no surviving task-owned Sparkwing or wingd process.

Production changes: **+37/-22, net +15**. The 14-line controller-local cause
helper was removed and moved into the shared error package. No legacy subsystem
was removed. W9-owned production Go versus main remains **+16,922/-23,812,
net -6,890**, within the -2,900 budget.

Shared files touched: `internal/settings/resolvers.go` and its intake policy
tests; `internal/wingd/localcontroller/native.go` and its two-job integration
test; `internal/apierr/codes.go`; `internal/blob/filesystem.go`;
`internal/controller/{handlers,policy_router,server,browser_flow,browser_github_app}.go`
and its private-host/error-log tests. No lead-owned handoff or amendment changed.
Contract changes needed: none. Open design questions: none. Existing adapters
were connected and the cause recorder moved; no new setting or runtime service
was introduced.

All checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, scratch below `/tmp/sw-astra-W9`, and at most four Go
package workers. Scratch was removed after checks joined; the shared cache was
retained. One initial root build hit a missing shared-cache archive; its rerun
passed. Xwing context discovery could not acquire its read-only session-index
lock. The supplied worktree was reused. Git identity/core.worktree overrides
remain absent.

STATUS: COMPLETE
