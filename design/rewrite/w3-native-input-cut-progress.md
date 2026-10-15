# W3 native input cut

<!-- design:begin -->

This session removes **240 net production lines** (12 added, 252 deleted).
The W3 directories account for **227 net deleted lines**. The shared CLI caller
accounts for the other 13. Against main, W3 is **+4,605** (12,206 added,
7,601 deleted), **6,905 over** its -2,300 budget. This remains unfinished
legacy deletion, not a justified overage.

Here resume now calls `nodehost.NewRunInput`, as runner hosting does. It sends
the request payload consumed by `OwnerHost`, copies the awarded owner fence and
runner before preparation, and preserves the binder's delegated authority.
Preparation cannot replace those inputs. A retry retains its original sealed
request and accepted spec while the host's RunID names the newly awarded run.
Accepted plans retain strict field decoding at the shared constructor.

Deleted the unused dispatcher result reconstruction and claim-loss helpers,
runner request fields and accounting types, worker options, old trigger source
admission and URL resolution, unused API-health reader, unused logger and box-ID
test setter, and abandoned sampler sink/delivery declarations. Their production
callers were already gone. The three obsolete helper test files are deleted.
The sampler interval override remains because the native usage test needs it.

## Verification

The whole repository builds. The nodehost suite passes **492 top-level tests**
with zero skips. Lease, source, runners and runner helper suites pass. Here
tests and fuzz seeds pass normally and under race. The generated preparation
identity property passes 128 fuzz executions. The new owner test exercises the
shared input through `OwnerHost.Run` for new/resumed runs and original/retained
request identities, using the existing store/source contract fakes.

Scoped vet, commentcheck against main and sleepcheck against HEAD pass.
Orchestrator and both acceptance
packages compile. The default-team caller audit and expansion concurrency
selection pass. No native acceptance cell or additional archived-test port is
claimed complete. No new scenario or registry was added.

The first nodemetrics run failed two native session-snapshot assertions during
concurrent checks. A baseline Go overlay and the final full package run pass.
The five-second fuzz run ended with `context deadline exceeded` after 5,588
executions and produced no failing input. The bounded 128-execution run passes.
These transient failures are retained here rather than counted as passes.

All Go checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR under the worker scratch, and at most -p 4.

## Remaining cut and foreign contracts

`RunHosted`, `HostTriggerRun`, dispatched-pod assembly, `Server`, the old scheduler
and its layers remain. Session and StartSession were deleted in an earlier
session. The initial here trigger still uses the old submission/hosting path.
Its new submission preparation and here binding remain W9 integration points.
`resolveHereRuntime` still returns an explicit unavailable error.

## Contract changes needed in other areas

The verified gaps blocking the full caller cut remain:

- W9/C3 `Controller.ReadSnapshot` returns ScheduleSnapshot. Native owners and
  claimed execution require sealed request/spec/plan, owner authority, full
  execution rows and accepted expansions. A public run read cannot replace it.
- W2/W9 still need the authenticated native run-start operation. Unit renewal,
  owner/node process delegation, bound storage and state following need real
  placement adapters. The C3 source-attempt and step-report fields are present.
- W8/W9 still need the matching engine's typed host invocation. InstallSDK alone
  cannot prove that the selected release owns host execution.

The earlier amendment audit remains open for production placement composition,
cross-host mask propagation and the other named foreign contracts. No foreign
contract shape changed. Lead decisions 09:40 items 1-3 and 07:40 item 9 remain
done. Host checks stay in package tests with consolidated fakes.

Shared files touched are `cmd/sparkwing/here_resume.go`, its test file,
`contracts/c3-protocol-sdk.md`, and this report. The shared production edit is
the smallest caller change for the common input constructor. No new mechanism,
customer setting, compatibility layer or API was introduced.

No Git write, Sparkwing executable, pipeline, install or release ran. The local
Git config has no user-identity or core.worktree override. Worker scratch is
removed after checks join. The shared build cache is retained.

<!-- design:end -->

STATUS: CONTINUE
