# W8 machine source and consumer residue cut

Continuation from `f8a8de00a`. The cluster worker, warm-pool dispatcher,
Kubernetes run-host dispatcher and old pod executor were already deleted.

`NewManagedRunner` now applies the machine owner's parsed repository list to
captured intake and accepted source before invoking its host. Empty lists,
unlisted remotes, machine-local checkout paths, invalid SHA and escaping
pipeline directories cannot reach hosting. Controller claim authority cannot
grant use of machine credentials. The managed real-store fixture supplies the
same list. Shared award capture avoids a second copy of its serialization code.

Deleted the orphan `GCWarmRoot` sweeper and the old trigger-source URL and
admission helpers. Deleted the retired consumer's snapshot reader,
consume-at-start adapter, cleanup cursor and environment overlay. They had no
production callers. The CLI capture/discard writer remains for W9's sealed
intake migration. Final host snapshots already survive retries and children.

Production Go adds **59 lines** and deletes **443 lines**.
**Net production delta: -384 lines.** W8-owned directories add **52 lines**.
Shared orchestrator cleanup removes **436 lines**. Tests, testdata, acceptance
and contract declarations are excluded. Whole obsolete production files removed
are `run_node_remote.go` (65 lines) and `warmgc.go` (156 lines).

Directory-based W8 accounting is **+4,221 against main**, **6,521 over** the
-2,300 budget. This cut does not meet that budget or request a waiver. That
accounting assigns the shared legacy deletions to W3. Engine/version support
alone adds 2,221 lines against main. The original component estimate did not
size the later signed-index, exact SDK-pin and native upgrade requirements.
Removing those behaviors would violate decisions 102 and 103. Further reduction
still needs implementation work.

## Verification and ports

- Whole-repository `go build ./...` passes.
- Full runners, launcher, runner service and cluster suites pass.
- All `W8|LauncherCloud` acceptance components pass, including
  `TestNativeStoreOwnerW8/managed_remote` and `/local_controller`.
- Machine repository admission passes the race detector and **8,473 fuzz
  executions**. The property varies source names, accepted-location changes,
  empty lists, local paths, escaping directories and non-commit revisions.
- Retained CLI capture, standalone filtering, profile guards, final snapshot
  tests and the default-team caller ledger pass. Six stale consumer/worktree
  ledger entries from earlier cuts were removed after the ledger rejected them.
- Focused vet, commentcheck against main and diff whitespace checks pass.

The port inventory records the source-admission and snapshot replacements.
The accepted working-tree snapshot case remains unported. No native placement
cell is newly closed. Foreign HTTP, enrollment, source, process and storage
boundaries still use fixtures. Native Windows capture/runtime was not run.

## Decisions, contracts and remaining integration

07:40 items 5 and 6 remain implemented. `HTTP() LocalHTTP` supplies the two
handlers. Launcher grammar accepts `--docker-image` and `--poll` and rejects
`--deadline`. Item 10 remains implemented at W8's wire boundary. Observations
carry operational unit identity, holder, live claim generation, Job namespace,
name and UID, and node UID. Eval source generation stays separate.
**A1-38 remains open.** W1/W2 still need transactional ancestry, live-award and
Job-instance correlation in `LauncherSyncReader.ReadLauncherSync`.
09:40 item 4 remains done. The four named dead declarations have no Go matches.

**Contract change needed, W3/W6:** `runspec.Source` and `source.Spec` describe a
Git-bundle seed but do not distinguish an uploaded working-tree snapshot.
Preserve that distinction in a sealed source-kind field before implementing
remote `accept_working_tree` consent. A commit seed does not require that consent.
No foreign declaration was changed and no source restriction was inferred from
the presence of a bundle. The existing committed-source acquisition still
verifies the sealed SHA. Final working-tree transport remains open.

**W9:** the runner factory must parse `RunnerOptions.Config.Repos` and supply
`RuntimeConfig.Repositories`. The default runner factory remains unbound.
The local service resolver, sealed intake and schedule adapters still need the
final snapshot store. The remaining legacy capture writer has no consumer.

| Amendments | Remaining real integration |
| --- | --- |
| A1-08/09/18/19/26/27/36/39/51 | Ordered claims, shared hosts, keeper, granted identity and final executor boundaries remain implemented. Remote NativeHostStore, per-process source/log/storage bindings and native hello remain. |
| A1-10/11/12/43/45/46/48/50/52/55 | Engine, source/environment, snapshots, settings, upgrade and here components remain. Final CLI/source composition, native Windows packaging and placement QA remain. |
| A1-20/49 | Production enrollment/provisioning and failed-enrollment revocation remain. Working-tree source classification needs the contract change above. |
| A1-40/41 | Owned-store queue, admission, reaper, readiness, four holds, managed units and joined drain remain. W1 activity projection, W2 peer-authorized HTTP and W9 service construction remain unbound. |
| A1-42/53 | Deployment Probe/Clear, enforced network isolation, aggregate scratch, teardown and native platform/real-repository QA remain open. |

## Shared files and mechanisms

Shared production files changed are `internal/orchestrator/local_trigger_loop.go`
and `submission_environment.go`. Shared production files deleted are
`internal/orchestrator/run_node_remote.go` and `warmgc.go`.
Shared test changes are orchestrator's submission/Windows/standalone tests,
the deleted repository-admission/source-URL/warm-root suites,
`cmd/sparkwing/profile_guard_test.go`, `pkg/store/default_team_callers_test.go`
and `tests/acceptance/native_store_w8_test.go`. Documentation changes are
`internal/runners/HOSTING.md`, `internal/wingd/LOCAL-CONTROLLER.md` and the port
inventory. Lead-owned handoffs and amendments are untouched.

The added mechanism is one internal host adapter for machine repository policy.
Relying only on controller authorization cannot enforce machine-owner consent.
It uses the existing source projection and allow-list instead of another source
implementation. Its undo is to remove that adapter and the new RuntimeConfig
field when the shared source boundary owns the same machine check. No new
environment variable, user flag, service or compatibility path was added.

Checks used the shared build cache and worker-local scratch, with package
concurrency at most four. Overwing discovery worked despite its stale-binary
notice. No Sparkwing runtime, pipeline or Git write was attempted. Scratch is
removed at the end and the shared build cache is retained.

STATUS: CONTINUE
