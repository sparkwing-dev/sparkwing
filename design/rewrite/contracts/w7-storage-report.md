# W7 storage handoff

<!-- design:begin -->

## Continuation at d9a8ac959

No runtime code changed in this continuation. No code was deleted (0 lines),
no test was ported and no acceptance cell was added or unskipped. The available
`bw-3095-rewrite` ref is an ancestor of this checkout. There are no unmerged
integration-branch store or data-adapter changes to consume.

Audited every W7 entry in amendments-1.md and handoffs.md against the live
callers. The byte, record, manifest, tagged-log and leased maintenance joins
from earlier sessions remain implemented. Their remaining callers need these
other-area implementations or contract additions:

- W2 must implement `DataController.DataAuthority`, `DataController.BindData`
  and `client.NewDataClient`. Each still panics. `PolicyRouter` also remains a
  stub. W7's consumers already use the declared byte and record interfaces.
- W2/W9 must declare the authenticated log transport and segment-commit wire
  binding. `blob.LogTransport` and `logseg.SegmentRecorder` exist, but their
  authenticated constructors do not. `SubjectUploader` already consumes them.
- W1 must install its transaction-local materialization copier. The only
  assignments to `Store.materializations` are in materialize_runtime_test.go.
  The missing production binding still returns `unavailable` from
  state_materialize.go. A separate public `CopyMemo` call cannot replace it
  because the copied records and terminal transition must commit together.
- W1/W3/W9 must carry committed output/manifest record identity to readers of
  materialized results. Their attempt may be zero. W7 cannot derive an exact
  result key from an invented execution attempt.
- W1b must expose leased expired-upload staging selection for physical cleanup.
  `releaseExpiredBlobUploadQuotasTx` releases holds but returns no staging keys.
  The approved record-mutation redesign remains W1b's work. It was not patched
  or reprobed here under the brief's two-strikes rule.
- W3/W8/W9 must construct fresh host storage bindings and finish local/profile
  cutovers. Live legacy callers still use S3 output/log routes, raw S3 log
  objects, store-local outputs and run-directory log readers. Their deletion
  remains unfinished. No replacement credential protocol or compatibility
  adapter was added.

Fresh verification passed: whole-repository build, all storage package tests
(`internal/blob/...`, artifacts, depcache, teamblob and `pkg/storage/...`), vet
on those packages, controller `TestDataStorage*`, standalone
`TestFilesystemDataStartup*`, and commentcheck against main (1,244 files).
Commands used the scratch/cache environment recorded below and tests used
`-p 4`. The previous full acceptance result below is historical evidence, not
a new run. Native authenticated acceptance remains open.

Only this report changed. No shared caller or foreign contract changed. There
is no new open design question. The remaining friction is the missing joins
listed above. The next continuation needs those other-area changes or a
declared contract addition; another component-only proof would not close them.
No Git write, Sparkwing binary, service or pipeline ran. Scratch was deleted.

## Previous continuation

Continued from ec920048a. The previous real-store record, physical log,
manifest verification and maintenance integrations remain. No component-only
acceptance cell was added or unskipped. No contract declaration changed.

Removed the unused cache-service egress surface: CacheSurfaces,
ControllerSurfaces, Surfaces, ClassGit and the service-selection branches in
Bind. The controller is Bind's only production caller. It now registers its
existing three flags directly. Their names, values, defaults and refusals pass
the existing flag tests. Anonymous-byte accounting and hijack fixtures use the
surviving artifact classification. The obsolete cache-service flag test is deleted.

This removes **103 tracked Go lines and adds 25**, a net reduction of 78.
Of those, 72 removed lines are production code and 31 are tests. It adds no
mechanism, interface, configuration, credential format or customer restriction.

Removed the deleted cache package from DELIVERY.md's check table and the
removed dependency-proxy defaults from VERSIONING.md. Corrected two stale
internal/blob README claims: standalone filesystem construction is installed,
and the generic storage.ArtifactStore contract is deleted. The remaining
s3.ArtifactStore serves live legacy logs and measurement. The changelog and
its embedded copy record the egress cleanup.

## Verification

Commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W7/cache, GOTMPDIR=/tmp/sw-astra-W7/gotmp and
TMPDIR=/tmp/sw-astra-W7/tmp. Tests used -p 4 from this worktree.

Passed:

- Whole-repository go build -p 4 ./....
- Full pkg/storage/..., internal/blob/..., internal/artifacts,
  internal/depcache and internal/teamblob tests.
- Existing egress tests, excluding the two socket-dependent tests named below.
- Controller TestDataStorage* and standalone TestFilesystemDataStartup*.
- Real-store output replay/scope, unrelated-job/dependency checks, memo/depcache
  pointers, destination rollback, source retention and configured result adapter.
- Vet for egress, cmd/sparkwing-controller and the storage packages listed above.
- Required commentcheck against main and current-diff sleepcheck against HEAD.
- Embedded-doc checks, sync-docs --check and git diff --check.

Existing TestStorage passed in **194.978 seconds**, with **81 pass events, zero
failures and 424 skips**.
No test was newly ported. Existing cells still exercise physical filesystem/S3
transfers joined to real tenant upload, receipt, record and log persistence.
Authentication, native placements and the skipped platforms remain unproved.

The first full egress run could not finish because
TestServeKeepsTheStreamControlsAHandlerAsksFor opens a TCP listener and the
sandbox returned `socket: operation not permitted`. The subsequent passing run
used `-skip '^(TestServeKeepsTheStreamControlsAHandlerAsksFor|TestServeForwardsHijack)$'`.
Both tests remain in the repository. TestServeForwardsHijack also needs a listener.
No source test was skipped or weakened to accommodate the sandbox.

## Remaining contracts and integration

All W7 entries in amendments-1.md and handoffs.md were audited against this
checkout. The earlier completed cuts were retained. No W7-owned amendment
implementation remains a panic stub. The live old paths cannot yet be removed
through the declared authenticated host constructors.

| Entry | Remaining join |
| --- | --- |
| A1-24/29 | W2 authenticated data binding and server-derived tenant/default ref for repository-only lookup. |
| A1-30/31/32/34 | DataAuthority, BindData, NewDataClient and PolicyRouter remain panic stubs. Bind manifest-capable records through Tenant.BlobRecords(storage.BlobManifests{Objects: objects}). |
| A1-33 | W1b must expose leased expired-staging cleanup. The internal quota-release helper does not return staging keys for physical deletion. |
| A1-35 | W1 transactional materialization binding and W1b's approved transaction-local record mutation redesign remain open. |
| A1-36 | W3/W8 must construct fresh host storage bindings and local-controller filesystem storage. W1/W3/W9 need committed result-record identity for artifact inputs from materialized jobs. |
| A1-37 | The SDK helper and lint-cache cuts remain complete. |
| A1-43/50 | W8/W9 own profile, host, publication and historical signed-asset integrations. |
| A1-53 | T1/lead own native authenticated transports, placements and real-repository QA. |

The W1 materialization finding remains unchanged: production never assigns
Store.materializations. Only materialize_runtime_test.go installs a copier.
The previous session's failing overlay already proves this absence. It was
not repeated. W7 cannot implement a private storeTx mutation from the physical
backend, and public CopyMemo would commit separately from the terminal state.

The result-reader contract is still missing. NodeRow and ExecutionJob expose
execution fences but no committed result-record keys. A materialized producer
may retain attempt zero. The host must not invent a positive attempt to form
its artifact manifest key. No foreign contract was changed here.

W2/W9 must declare the segment-commit wire operation and construct bindings for
blob.LogTransport and logseg.SegmentRecorder. Both shared object backends
already implement the exact-key reader used by CommittedReader and
UploadVerifier. NewSubjectUploader already uses those contract interfaces.
No replacement authentication protocol is needed in W7.

Live legacy paths still include the S3 output/log handlers, their clients,
host sinks, raw S3 log reader/writer, store-local outputs and disk-log readers.
They require those authenticated bindings and host migrations. Their deletion
is not claimed. The previous six record-mutation expiry failures remain the
approved W1b redesign finding under the two-strikes rule. That mechanism was
unchanged and the failing check was not rerun.

## Shared files and friction

Shared Go caller changes outside W7's physical storage:

- cmd/sparkwing-controller/main.go: pass only the flag set to egress.Bind.
- internal/egress/flags.go and egress.go: delete cache/proxy-only declarations
  and the service-selection branches.
- internal/egress/flags_test.go and egress_test.go: remove the obsolete cache
  test and migrate retained flag/accounting fixtures.

Other files are CHANGELOG.md, its generated pkg/docs/changelog.md copy,
DELIVERY.md, VERSIONING.md, internal/blob/README.md and this report.

The socket restriction is the only new verification friction. No Git command
wrote metadata. No candidate, service, pipeline or Sparkwing executable ran
after the required initial discovery. Scratch is deleted after verification.

<!-- design:end -->

STATUS: CONTINUE
