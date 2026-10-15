# W3 runtime helper cut

<!-- design:begin -->

This session removes **385 net production Go lines** (53 added, 438 deleted).
W3 removes **392 net lines**. The shared Windows process owner adds seven.
Against main, W3 is **+4,213** (12,217 added, 8,004 deleted), **6,513 over**
its -2,300 budget. The overage remains unfinished legacy deletion.

## Implementation and deletion

Trigger planning, pod planning and runner SDK installation now call `procenv.Build`
with the remote base and configured allow list. Deleted the separate base-env,
operator filtering and proxy-masking implementation. Install uses the Install
policy. The environment property asserts that credential-named inputs and forged
runner identity never enter either process, while admitted proxy credentials are
masked before launch. The duplicate proxy tests are removed because procenv owns
their policy checks.

The local trigger child now uses the same `procgroup.Job` as native Windows SDK
processes. Deleted its private Job Object creation, accounting layout, thread
enumeration, resume and handle-management code. It retains ownership until the
job is empty and the child is reaped, including failed starts. The shared owner
now rejects an unexpected resume count and returns termination errors. The old
suspend-count regression moves to a shared-owner property over counts and resume
errors, without a mutable package-level test hook.

Deleted `internal/orchestrator/runner`. Trigger dispatch uses the existing
node-host request and result types directly. Its sole production executor ignored
the removed timeout, resource, pipeline and claim-wait fields. Deleted the
conversion function, aggregate attempts-budget helper and three tests that only
checked those removed surfaces. The job-status reader now asserts its output
read method locally.

## Verification

Checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR under worker scratch, and at most four test packages.

- Whole-repository build and scoped vet pass.
- Nodehost passes **490 top-level tests**, lease three and source 21, with no skips.
- Focused environment, install-output and child-lifecycle checks pass, including
  their race selection. The environment property passes 128 generated inputs.
- Windows amd64 orchestrator, nodehost and procgroup tests compile. The Windows
  property and console/descendant checks still require native execution.
- The existing expansion-concurrency mismatch test passes. Lead decision 07:40
  item 9 remains done. Lead decisions 09:40 items 1-3 remain done: no host files
  remain in internal/acceptance, and each keeper check has one package test.
- Commentcheck against main, sleepcheck against HEAD, whitespace checks, the
  docs mirror test and docs-sync drift check pass.

The broader TriggerHost selection fails before hosting because its fixtures
insert into the removed `tokens` table. A HEAD overlay reproduces
`TestTriggerHost_TheCheckoutsGuardRefusesTheRun` with the same error. The legacy
integration selection is not green. Initial compile checks during API removal
also found missing or unused imports, which are corrected.

No new acceptance cell or archived-test inventory row is claimed complete.
The Windows regression was moved, not executed on Linux. Native HTTP/api.sock,
Windows/Darwin and the placement matrix remain open.

## Remaining work and contract changes needed

`RunHosted`, `HostTriggerRun`, pod assembly, Server, the legacy scheduler and its
layers remain. Session and StartSession were already deleted. The three cuts
above remove duplicate mechanisms but do not complete production host migration.

The existing foreign contract requests still block that cut:

- W9/C3 `Controller.ReadSnapshot` returns only ScheduleSnapshot. Native hosts
  need the sealed request/spec/plan, owner, full execution rows and accepted
  expansions. The interface also lacks the authenticated native run-start
  transition used by OwnerHost. Legacy run reads cannot supply those contracts.
- W2/W9 must bind unit renewal, process delegation, storage and state following
  to production placements. Here runtime still returns its existing unavailable
  error. Native initial here submission is not wired into its CLI caller.
- W8/W9 must expose the selected engine's typed host invocation. InstallSDK
  alone does not establish that the selected release owns host execution.

These missing declarations remain requests to their owners. No foreign contract
shape, compatibility path, customer setting or limit is added here.

Shared files touched are `internal/procgroup/job_windows.go`, its new Windows
property test, `internal/orchestrator/jobs_cli.go`, `docs/node-protocol.md`, its
generated mirror and the C3 protocol contract's report link. The job-status caller
changes only to remove its dependency on the deleted runner package. No caller
in another Go package needs a change.

The lead owns review, commits and placement integration. No Git write, Sparkwing
executable, pipeline, install or release ran. Git has no local user/core.worktree
override. No worker test process remains. Scratch is deleted after checks join.
The shared build cache is retained.

<!-- design:end -->

STATUS: CONTINUE
