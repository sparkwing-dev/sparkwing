# W8 admission and composition cut

The admission reader now uses `userconfig.ReadConfig` and W6's typed admission
settings. Removed its private YAML types and duplicate range, enum and class
validation. The runtime retains the conversion to scheduling/Jev policy,
explicit zero values, omitted-field defaults and trimmed Jev model/endpoint.
Existing policy tests pass. The complete-machine validation was already working
through the old section reader, so no new validation test or acceptance cell was
kept for it.

Deleted `ResolveBudget` and its private parser. They had no production caller.
Kept the runtime's resolved-budget value and provenance. Deleted the unused
`RefreshRunning`, `RestartRunning`, `RefreshResult` and their implementation and
tests. Installed controller/schedule refresh remains in `ManagedUnitManager`.
Admission election, takeover and drain remain in their existing owners.

Deleted `Binding.NewQueue`. `Config.Open` now checks the owned store directly and
calls `runners.NewStoreRunner`. Its foreign-store refusal and partial-service
cleanup still pass. The two queue fixtures call the same runner constructor.

Deleted `BootstrapHost.RunFile`. `cluster.BootstrapRuntime` owns loading the
private award, checking the CLI fence and calling `BootstrapHost.Run`. Migrated
the two bootstrap fixtures to `ReadBootstrapAward` plus `Run`. Moved the existing
cancellation-before-file-acquisition regression to `BootstrapRuntime`'s test
file, where the production file reader is composed. Updated source hosting,
Cloud Job and local controller documentation.

## Priority and amendment status

- Lead item 5 remains implemented as `LocalController.HTTP() LocalHTTP`. W2's
  production HTTP/state binder is still absent.
- Lead item 6 remains implemented: launcher accepts `--docker-image` and refuses
  `--deadline`. The existing launch CLI tests pass.
- Lead item 10 is implemented on the launcher side. Sync sends the immutable
  operational unit kind/ID, holder and claim generation, actual Kubernetes Job
  namespace/name/UID and node UID. Eval source and live claim generations remain
  distinct. The launcher suite and W8 acceptance selection pass.
  **A1-38 remains open end to end.** `EngineController.SyncLauncher` delegates to
  `LauncherSyncReader`; its transactional production implementation is absent.
- The cluster worker, warm-pool dispatcher, run-host Kubernetes dispatcher and
  old W8 pod builders remain deleted. Their historical deletions are not counted
  again here. `launcher.BuildJob` remains the sole W8 Cloud Job builder.
- A1-08/09/18/19/39/40/41/49/55 retain the shared claim, captured identity,
  native/store hosting, lifecycle, managed-unit, credential-file and named here
  claim work recorded in the preceding reports. This session removes duplicate
  composition without changing their authority or execution contracts.
- A1-10/11/12/36/43/45/46/48/50/51/52 retain their existing engine/source,
  storage, environment and SDK integrations and foreign handoffs. A1-42/53 still
  require deployed isolation, teardown and native/real-repository evidence. The
  four dead catalogs assigned at 09:40 remain deleted.

## Lines and contracts

Against starting HEAD `154da9a68`, production Go adds 52 lines and deletes 255.
**Net production lines deleted this session: 203.** Tests and Markdown are
excluded. Using the lead's area metric against `main`, W8 is **+4,396**, or
**6,696 over** its -2,300 budget. W8 remains incomplete.

The overage still needs lead review. D102-103 require the signed engine
resolver, cache and SDK upgrade implementation, and D91 requires local process,
HTTP and managed service lifecycles. Those additions were not separately sized
in the estimate. This session cuts duplicate readers and constructors while
retaining those ratified features. No new service, setting, environment
variable, trust root, customer restriction or timeout was added.

The removed APIs belong to W8. No foreign production signature changed and no
foreign contract change is requested. No off-area production caller needed
migration. Shared files touched are these fixture migrations:

- `tests/acceptance/bootstrap_store_w8_test.go`
- `tests/acceptance/bootstrap_w8_test.go`
- `tests/acceptance/native_store_w8_test.go`
- `tests/acceptance/store_queue_w8_test.go`

Other edits are in W8. Lead-owned handoffs and amendments are unchanged.

## Real integration points left open

- W1/W2: implement the transactional launcher sync reader over current authority
  and award fences. W2 still clears non-node award `Execution`; plan/eval awards
  need policy-owned resolved resources before the launcher can consume them.
- W2: supply local state/activity, authorized API/dashboard handlers,
  provisioning and claimant enrollment over `localcontroller.Binding`'s store.
- W3: supply the remote `NativeHostStore`, Cloud plan/eval lifecycle and
  initialized-checkout adoption contracts. W9/W2 must align source credentials.
- W8: finish executable composition against those services and deployed
  `KubernetesBoundary.Probe`/`Clear`. Native isolation, scratch accounting,
  teardown, Docker, Windows/macOS and real-repository QA remain open with
  T1/lead. Component fakes do not close these points.

## Verification

All checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`, at most
four package workers, `/tmp/sw-gocache` and scratch under `/tmp/sw-astra-W8`.

- Whole-repository `go build -p 4 ./...` passes.
- Focused vet passes for cluster, launcher, wingd and its child packages, and
  acceptance.
- Full launcher and localcontroller suites pass.
- Existing local lifecycle/runtime/client, managed-unit, admission policy and
  resolved-budget tests pass, including race checks. Localcontroller's full
  race suite passes.
- Existing bootstrap and launch CLI tests pass. The moved cancellation
  regression passes through `BootstrapRuntime.Run`.
- The existing `W8|LauncherCloud|LauncherController|LauncherPolling` acceptance
  selection passes. No native placement cell was unskipped and no historical
  T1 test port was newly closed.
- Commentcheck against main passes across 2,123 Go files. Diff whitespace passes.

Friction: Overwing reports its installed binary behind source and rejects
`standards match --type`; ordinary matching and card reads worked. Required
foreign service bindings remain absent. No Sparkwing runtime, pipeline,
deployment, release or Git write ran. Local Git configuration has no identity
or core.worktree override. All check processes joined. Scratch is removed and
the shared build cache is retained. The lead owns review and commits.

STATUS: CONTINUE
