# W9 detached profile-cache diagnostics

Base: `a267a087c`. No Git writes. Retry selection and the lead-owned amendment
and handoff files are unchanged.

## Native evidence

The lead's latest native file passes attached profile-cache execution, attached
and detached publication, edited-source execution, personal execution, local
fallback, overflow, cancellation and SDK HTTP execution in all three placements.
Its remaining failure is detached profile-cache execution. The subsequent
`runs logs --follow` process is killed at the fixture deadline with empty output.
This session has not established or fixed the cause of that hang.

The probe retains CombinedOutput, so CLI stderr accompanies subprocess failures.
On failure it now also prints the local controller status, the admitted run's
public status through the existing API socket, and the controller log tail.
The two status reads have independent diagnostic deadlines. Diagnostics run
before the fixture stops its daemon.

The production background queue had no error reporter. A host error was retained
until queue shutdown, leaving a live daemon silent. Its existing OnError callback
now writes to the controller logger when the host returns. A regression builds
the production local services and shared runner, then substitutes only claim
delivery, execution and external version policy. Removing the callback with a
Go overlay reproduces `host failure stayed silent`. The corrected test passes.
Invariant: a returned background host error is reported without waiting for the
controller to stop.

## Profile construction and publication

These are implemented in the committed code. This session rechecked the CLI's
ordinary-account credential construction, verified-default read-only binding,
revocation on cleanup, attached and background host bindings, and publication.
Real-router/store/signed-filesystem credential and publication tests pass.
The lead's native publication and attached-cache results supply additional
evidence. Detached-cache completion remains open, rather than a completed
credential/publication implementation being claimed as missing.

## Contracts and size

Contract changes made: C4 records immediate background host error reporting.
Contract changes needed: none. No DESIGN change, public flag, route, environment
variable, API, schema, service, limit or caller migration was added.

Production this session: **+4/-2, net +2**. The two replaced lines are formatting.
Deleted legacy production: **0 lines**.
W9-owned production versus main: **+19,013/-23,818, net -4,805**, within -2,900.
The existing burst/profile implementation grows by two production lines.
No legacy subsystem or archived test was replaced in this diagnostic session.

Changed files are `cmd/sparkwing/local_services.go`, `local_host_test.go`,
`burst_cli_integration_unix_test.go`, C4, and W9's own handoff and report.
No foreign production package changed. No acceptance cell is newly closed.
No archived test was newly ported.

## Verification and remaining integration

Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
TMPDIR=/tmp/sw-astra-W9 and GOTMPDIR=/tmp/sw-astra-W9/go, with -p 4 at most.

- Whole-repository build and focused vet pass.
- Focused CLI burst, local queue/cache/publication, controller profile/publication,
  log-page, nodehost profile-binding and native-cache tests pass.
- Full short client, controllerwire and clisurface suites pass.
- Existing embedded-owner log-finalization acceptance and all seven native-store
  owner component modes pass. Their socket-free evidence closes no native cell.
- The queue error-reporting regression passes and fails against the old
  constructor overlay. It and the authenticated-storage regression pass under
  the race detector.
- Commentcheck against main and sleepcheck against HEAD pass.

The native profile-cache test compiles and skips both children at this sandbox's
socket refusal. The lead must rerun its existing native families without skips:
`^TestBurstPublicRun` in cmd/sparkwing and
`^TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards$` in pkg/controller,
both with -tags rewrite_integration. The updated detached failure output should
name the daemon's host error or show the stalled run state if it still hangs.

The authoritative native file still contains FAIL. Native detached cache,
deployed Cloud/DinD, network transfers and the platform/database lanes remain
real integration points. The accepted requester-secret restart residual stays
unchanged. Friction is the socket refusal and Overwing's source-lag notice.
Scratch is removed after checks finish. The shared build cache is retained.

STATUS: CONTINUE
