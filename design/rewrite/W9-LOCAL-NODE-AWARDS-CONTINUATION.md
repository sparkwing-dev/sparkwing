# W9 local node awards

The foreground local host now retains each node's store-issued award through
private reconciliation. Owner evaluation continues to use its owner or eval
authority.

## Cause and change

Invariant: every local job process delegates from its current node award, and
owner authority cannot substitute for a missing node award.

The shared store already self-claims ready local nodes through `claimUnitTx`
and the canonical matcher. `OwnerHost.execute` already adopts the returned
award before execution. The missing handoff was `OwnerCapabilityPrefix`:
`ExecutionJob` omitted it, and `executionRow` lost the persisted owner relation.
Local adoption therefore rejected the node's award with `lock_held`.

The private wire now carries `owner_capability_prefix`. Its producer and consumer
preserve the field. Existing adoption, authentication, per-attempt delegation,
storage binding and complete service construction remain the enforcement path.
No grant, claim policy, schema, issuance kind or service factory changed.

## Evidence

`TestLocalControllerTwoJobProcessesUseIndependentAwards` opens the real local
controller and SQLite store, uses the real claimant, private HTTP API and remote
host adapter, and runs two child protocol processes through the native host.
HTTP uses `net.Pipe` because the sandbox refuses socket listeners. The fixture
supplies its accepted plan, selected engine, source entry and machine admission.
It does not establish SDK installation or native admission-daemon execution.

The test rejects owner authority before any node award. For each process it
checks the persisted node parent, job, generation/attempt binding and complete
process services. The jobs receive distinct awards and commit success through
their respective holder identities. The completed owner loses private access.
The existing remote reconciliation test also checks the retained owner prefix.

The regression reproduced the exact reported authority error before the fix.
An overlay restoring the three production files from HEAD reproduced it again.
The final regression passes normally and under race. Its initial race run found
that the test's direct handler adapter turned caller cancellation into HTTP 500.
The final fixture uses a real HTTP client/server over pipes, including transport
cancellation and joined shutdown.

Passing checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, `GOTMPDIR=/tmp/sw-astra-W9/go` and
`TMPDIR=/tmp/sw-astra-W9`. Package concurrency was at most four.

- `go build ./...` and `go vet ./...` in the root and `.sparkwing` modules.
- Full short suites for localcontroller and controllerwire.
- Remote host, private host, reconciliation, persisted owner execution and
  materialization tests in nodehost/controller, plus the production local
  admission and queue-storage tests in cmd/sparkwing.
- Race checks for the two-process regression and node-award fencing seeds.
- New `FuzzRemoteReconciliationKeepsOwnerAndNodeAwardsSeparate`: 5,365 executions.
- Existing `FuzzLocalNodeAdoptionRejectsForeignAwards`: 22,972 executions.
- Documentation checks, embedded documentation drift, commentcheck against main
  and diff whitespace checks.

No acceptance cell was newly unskipped and no archived test was ported.

## Next failure and limits

`go test -tags rewrite_integration -p 4 ./cmd/sparkwing -run
'^TestReviewProductionExecutionBindings/local_controller$' -count=1` reaches
the native controller listener and fails with `listen unix .../d.sock:
setsockopt: operation not permitted`. The sandbox cannot establish the native
controller or admission daemon. No later failure of `sparkwing run example`
has been established. The lead must rerun it outside the sandbox.

A broader dependent-test selection also found two inherited failures. Both
reproduced with the original production files in an overlay:

- `TestEngineBootstrapExchangeReturnsOriginalUnitOnce/{plan,node}` refuses
  the fixture with `claimant repository scope excludes work`.
- `TestNativeControllerPlanReplayAfterOwnerHandoff` refuses the fixture with
  `insufficient credits: balance 0.00, need 5.40`.

These failures are reported, not counted as passing checks.

## Scope and cleanup

Production Go in this continuation is +15/-14, net +1, including alignment.
No legacy file was deleted. W9-owned production Go versus main is
+16,917/-23,812, net -6,895, within the -2,900 budget.

Shared files touched are `internal/nodehost/remote_store.go`,
`internal/nodehost/remote_contract_test.go`,
`internal/wingd/localcontroller/node_pipeline_test.go` and
`internal/controller/engine_snapshot.go`. W9-owned changes are
`internal/controllerwire/engine.go`, `design/rewrite/contracts/c3-controller-wire.md`,
`docs/local-execution.md` and its embedded mirror, plus this report.
No foreign contract changes are needed. Lead-owned handoffs and amendments
were left unchanged. No new mechanism or environment variable was added.

No repository Git write was made. Git has no local identity or core.worktree
override. Scratch under `/tmp/sw-astra-W9` is deleted after checks join.
The shared Go cache is retained. No child protocol process remains.

STATUS: COMPLETE
