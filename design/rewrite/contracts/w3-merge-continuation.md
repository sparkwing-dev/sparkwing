# W3 merge continuation

<!-- design:begin -->

W3 remains incomplete. This continuation resolves the six conflicted working
paths, connects holder authority and failure records to the amended store
contracts, and ports the obsolete recovery assertions. No Git write ran.

## Merge disposition

The working files have no conflict markers. W3's versions of debug.go,
debug_test.go, scheduler.go, hosted_rehydrate_test.go and hosted_run.go retain
their behavior. Their conflicting hunks were comments or equivalent test wording.
The deletion of internal/orchestrator/run_node_verb.go is preserved because its
remaining heartbeat caller uses dispatched_heartbeat.go. Restoring it would
duplicate the heartbeat implementation and revive the removed command.

The index still lists six unmerged paths. The lead must stage these resolutions
because the brief forbids worker Git writes and the sandbox cannot write the
shared Git metadata. All amended contract files were already merged without a
conflict. None was reverted or changed here.

## Implementation and invariants

Holder renewals and executor transitions now set Actor.Authority to the awarded
capability reference. Prefix, holder and generation remain the exact persisted
claim. The generated claim-fence test changes each award component and the sealed
execution settings and requires rejection before any renewal, source or SDK work.

Every failed retry or terminal report now carries the bound job's masked failure
and action/verify stage. SDK hints and details never enter the persisted failure.
Source setup failures also get an action failure. Verify timeouts retain verify.
Success and cancellation clear the failure. The report boundary rebuilds the
canonical payload rather than trusting a nested error's extra fields. Additive
error codes survive unless the shared masker changes them. Changed codes use
sdk_error. Returned canonical host errors apply the same code masking.

RemoteOwnerStore refuses MaterializeSource.Result before transport because the
wire cannot represent its terminal state sequence. This prevents a request from
silently changing source identity. It does not complete remote result reuse.

Two active component cells are added under lifecycle-dependency-verdicts:
hermetic-recorded-failure and hermetic-recovery-graph. Native target cells stay
skipped. The latter ports inventory rows 101, 102 and 117, plus cancelled-parent
and optional-recovery verdict coverage. The 89-line legacy onfailure_test.go is
deleted. No additional legacy production code is deleted in this continuation.

## Amendment audit

| Rows | Current W3 disposition |
| --- | --- |
| A1-01, A1-02, A1-05 | Existing process services and identifier/mask registry retained. Native router, resolver and cross-process propagation remain open. |
| A1-03, A1-04, A1-06, A1-07 | Shared error decoder/record bounds, targeted cancellation and optional declarations retained. No private replacement added. |
| A1-10, A1-11, A1-12 | Existing selected-engine install, sealed source location and shared environment calls retained. W8 installation/hosting and W6 Git policy remain integration points. |
| A1-13, A1-15 | Materialize-only probe and scoped cancellation retained. The new C1 terminal-result source is rejected by the remote adapter until the wire can carry it. |
| A1-14 | Failure reads retained. Retry, terminal and source-setup writes now populate the persisted failure. Remote write transport needs the field below. |
| A1-16, A1-17, A1-18 | Execution/replay projections and authoritative start/retry ordinals retained. Full restricted remote snapshots remain missing. |
| A1-19 | Existing hello/environment paths retain the supplied granted runner and sealed engine. Native award assembly remains with W3/W8. |
| A1-21 through A1-25 | Holder Actor.Authority now accompanies W3 renewals and transitions. Existing Authorized service/source seams remain. The store owns authentication, release and secret transactions. |
| A1-36 | Verified artifact, output, depcache and memo adapters retained. Native bound-client assembly and owner memo composition remain open. |
| A1-38, A1-39 | Common claimed host and executor retained. W8 runner/bootstrap wiring and W3 native host cut remain open. |
| A1-44 through A1-48 | Intake classifications, snapshots, shared environment and distinct execution/no-progress budgets retained. Guard assembly and native capture remain open. |
| A1-50, A1-51 | One obsolete recovery test file is retired. CLI adapters and the remaining v1 runtime/fixtures still need the coordinated cut. |
| A1-53, A1-54 | Component cells pass. Native acceptance and approval transition/read gaps remain open. SkipIf is still an approval condition. |

## Contract changes needed in other areas

1. W9 must add persisted JobFailure to the job result wire and its controller
   adapter. JobResult currently has only Outcome, Reason, Error, ExitCode, Output
   and Manifest. A recovery reader cannot recover the stage from that shape.
2. W9 must carry C1's MaterializeSource.Result, including its NodeRef and state
   sequence. ReuseSource carries only an execution attempt and record. Terminal
   results can exist without an execution attempt.
3. W9 still needs a restricted full execution snapshot/read, including request,
   spec, owner, generated job state and result record identities. ScheduleSnapshot
   is only a scheduling view. It also omits C1's GuardRefusal projection.
4. C1 still lacks typed owner policy-error, debug-activity and approval SkipIf
   success edges and the approval opening/resolution read shape. Eval log source
   generation still requires greater than zero in schema.sql. Materialized
   sources may have generation zero.
5. W7/C1 memo source/publication and remote import shapes, W2/W4 cross-process
   masks, and W8's selected-engine host handoff remain as listed in the previous
   handoffs. These were not replaced with host-invented contracts.

W4's default parser/validator and W8's installer still contain panic stubs in
this checkout. Component tests use their declared interfaces with fakes. W6's
procenv.Git declaration still lacks an implementation branch. These are integration
points, not a reason to skip the W3 component tests.

## Scope and remaining work

RunHosted, HostTriggerRun and pod_node_host still use the old native assembly.
W3 still needs complete owner policy assembly, native process delegation,
source/data authority wiring and the coordinated legacy scheduler/session/layer
cut. The current component work does not complete those paths.

Shared edits are internal/acceptance/host_claimed_node.go, host_executor.go,
host_failure.go and host_recovery_graph.go, tests/acceptance/failure_test.go and
skeleton_test.go, c3-protocol-sdk.md, t1-port-inventory.md and this handoff.
No production caller outside W3 needed migration. The six merge paths are listed
above. No public command behavior is newly exposed because native commands still
use the legacy assembly. Contract prose and executable component checks describe
the library behavior.

No new public API, configuration, environment variable, timeout, limit or service
was added. The private failure serializer uses the amended NodeResult field.
The lead retains review, Git staging/merge completion, pipelines and native QA.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W3/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W3/tmp.
Tests use at most -p 4. The scratch directory is removed at shutdown.

The new acceptance test first failed because every failed report omitted
NodeResult.Failure. The stricter claim fixture first failed before source work
because Actor.Authority was empty. Both pass after the implementation changes.

The repository build, scoped vet, full source/lease/acceptance suites and focused
current host suite pass. The host suite uses the selection in w3-amendment-handoff.md.
Sleepcheck against HEAD passes. Commentcheck against main reports three inherited
comments in internal/controllerwire/contracts.go:47,
pkg/store/state_materialize.go:15 and pkg/store/state_result_records.go:9. It
reports no W3 finding.

The initial broader test selection reached legacy failures for missing
sparkwing_meta/tokens tables and a sandbox-denied TCP listener. These remain
failed evidence. No Sparkwing binary or pipeline ran. Native HTTP/api.sock,
Postgres/S3, real runner/launcher, Windows/Darwin and real-repository QA remain
unverified.

Final verification passed:

- `go build -p 4 ./...`.
- `go vet -p 4` for nodehost, lease, source, orchestrator and both acceptance packages.
- Full source, lease and both acceptance suites.
- The focused current host suite and the resolved manual-retry selection test.
- Race checks for NodeExecutor, ClaimedNodeHost and Remote tests and the new
  acceptance cells and fuzz seeds.
- `FuzzExecutorFailureMask` and `FuzzClaimedAwardFence`, each with
  `-run '^$' -fuzztime=3000x -parallel=2` under tests/acceptance.
- Sleepcheck against HEAD and `git diff --check`.

The generated failure test also checks the canonical error returned to renderers,
including a secret-bearing error code. Final failure fuzz took 23.9 seconds.
Claim-fence fuzz took 1.8 seconds. No failing corpus was written. No test process
remained after the tracked commands completed. Git config retains Korey's default
identity and has no core.worktree override.

<!-- design:end -->
