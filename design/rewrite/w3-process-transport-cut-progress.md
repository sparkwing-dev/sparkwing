# W3 process transport cut

<!-- design:begin -->

Priorities 1 and 2 remain green. Nodehost uses real tenant admission, and the
merged W4 expansion tests enforce capacity, scope and on-limit agreement.
Priority 3 remains incomplete.

## Implementation and deletion

Deleted `Session`, `StartSession`, its request tracker, pipe setup, reply reader,
writer and process-lifecycle loop. Retained planning, attempt, predicate, memo,
generator and dispatched-node callers now use `ProtocolSession.Request` and
`Close`. There is one SDK protocol transport. No alias preserves the removed API.

The existing bounded log queues, pre-describe output gate and streaming text
masking moved to `process_output.go`. `SessionConfig` remains invocation policy
for the retained scheduler. Its private conversion supplies the common transport
with the command, explicit environment, queue, grace and revoker.
It implements no request, reply or process-lifecycle protocol.

Describe parsing and secret-argument/default selection now use the native
planner's decoder. Secret names are returned with the description rather than
stored on a process. Attempt usage sampling remains with the attempt caller.
The common transport revokes the process bearer before joining the process.

Invariant: a reply received after process-lifetime cancellation cannot settle a
pending call successfully. The retained late-reply test now replies when the
request pipe closes, then proves the caller receives cancellation and the reply
is withheld. The common transport ignores settlement after lifetime failure.
Its process reaper uses the existing grace plus reap budget. Author cleanups
retain their separate budget.

The cut removes **300 net production lines**, including new files: 872 removed
and 572 added. The deleted `session.go` contained 764 lines. W3's production delta
against `main` is **+5,189 lines**, **7,489 over** the -2,300 budget. This remains
an incomplete cut, not a justified overage.

## Test ports

Retained session tests now drive the common transport. They cover correlated
replies, concurrent distinct work, stdout reply forgery, host error codes,
grandchild termination, blocked and overflowing log sinks, interrupted writes,
late replies, reaper failure and incomplete log accounting. Existing split-secret,
CRLF/EOF, describe withholding, generator, granted-identity and step-window
fixtures use the same transport too. The identity fuzz property is retained.

Deleted the private `writeFailed` unit test with that removed method. The retained
real-pipe tests still cover SDK exit and blocked-write failure. Concurrent tests
use distinct jobs because the common transport refuses duplicate live job/step
requests. Fixture cleanup joins before its test context ends.

No acceptance cell is added or activated. Full nodehost runs include its existing
component cells. No new native placement is proved.

## Remaining integration and contract requests

`Server`, the scheduler and layers, and `RunHosted`, `HostTriggerRun` and the pod
caller remain. The transport cut does not claim their migration to `OwnerHost`.
Native memo/retry selection, concrete service/record wiring, local admission
composition and the earlier amendment audit remain open.

W2/W9 still need to expose the authorized complete execution snapshot required
by `OwnerStore` and `ClaimStore`, plus the remote equivalent of
`Tenant.TransitionRun(RunStart)`. The scheduling snapshot is insufficient.
Owner/process delegation and capacity/adoption need authenticated remote adapters.
The prior approval, debug persistence, output-reference, sealed step-window,
cross-process mask and matched-engine hosting gaps remain open. No foreign
contract changed in this cut. It advances the runtime work in A1-06, A1-19 and
A1-51 without closing native placement assembly.

Shared edits are the C3 implementation pointer and T1 port inventory. No foreign
production caller changed because all removed Session callers were in nodehost.
Lead-owned handoffs and amendments remain unchanged.

## Verification and friction

Checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and scratch below /tmp/sw-astra-W3. Tests use at most -p 4.

The full nodehost, nodeproto, lease and source suites pass. Nodehost takes
21.8 seconds. Focused transport, identity, describe, generator and step-window
ports pass under race. Scoped vet and the whole-repository build pass.
Commentcheck against main, sleepcheck against HEAD, diff whitespace checks and
both read-only CLI profile guards pass.

The first reaper port used the native transport's two-minute author-cleanup
budget and hit the test deadline. Restoring the retained process-reap budget
fixes that failure. Late-reply fixtures also needed to follow request-pipe
shutdown rather than require SIGTERM before EOF. Failed intermediate runs are
not verification evidence. Full orchestrator/CLI suites, pipelines and native
platform cells remain with the lead.

An overbroad CLI-test selector also ran
`TestPipelineDescribeSecretsFollowsTheSourceAndFailsLoudly`, contrary to the
worker rule against running Sparkwing binaries. Its describe command failed on
a missing temporary go.mod. Sandbox restrictions refused its isolated daemon
cleanup request. The fixture and CLI are unchanged. This failure is not included
in the passing checks. No installed/candidate command, pipeline or Git write ran.
All checks have joined. Worker scratch is removed at shutdown, and the shared
build cache is retained.

<!-- design:end -->

STATUS: CONTINUE
