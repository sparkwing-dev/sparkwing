# W8 orphan run-node cut

Continuation from `b50180520`. The cluster worker, warm-pool dispatcher, run-host
Kubernetes dispatcher and old W8 pod builders remain deleted. This session
removes their orphan `orchestrator.RunNodeOnce` entry point and its options,
including `ClaimedNodeFence`, `ClaimedNodeAttempt` and `WithRepoAllowlist`.
There are no remaining production callers to migrate.

The separate `runNodeRemote` checkout/build path is removed with its dead path
selector, baseline adapter, remote-binary cache adapter and metric hook. The
exported TypeScript installer wrapper and unused node-execution histogram are
removed too. W3's trigger host still calls `hostDispatchedNode` and its stopping
state adapter, so those remain. The surviving repository-policy helper and URL
helper have tests but no production caller after this cut. Their production
replacement belongs in the real runner-source binding. The private
`prepareTypeScriptOnRunner` and `maskedInstallOutput` now survive only for W3's
old install-output tests. Their remaining ports must cover the selected-engine
installer and captured remote environment before deleting those tests.

## Evidence and test ports

The sourceless-node regression now constructs persisted native inputs and calls
`ClaimedNodeHost.Execute`. It checks the exact source-location refusal and
forbids acquisition and execution. The cancellation regression now calls
`source.PrepareCheckout` while a Unix git-fetch process fixture is blocked.
Cancellation joins that process and removes its owned checkout. The fixture
delegates initialization to real git. It does not contact a network remote.

The two repository-refusal tables retain every refusal and accepted snapshot
case at `AdmitTriggerSource`. They no longer depend on a deleted fetch wrapper.
They are helper-policy evidence. They do not close machine-source authorization
through native execution. The old path-selection and unused-histogram tests are
retired. The complete mapping is in `contracts/t1-port-inventory.md`.

No native acceptance cell is newly closed. W8 acceptance components, including
Cloud pod construction, pass. No Cloud, Docker, native platform login or actual
SDK-process integration is claimed.

## Lead decisions and remaining integration

- 07:40 item 5 remains implemented as `LocalController.HTTP() LocalHTTP`.
  W2 must supply its production authorized API/dashboard binding.
- 07:40 item 6 remains implemented. The launcher accepts `--docker-image` and
  rejects `--deadline`. The full cluster and launcher suites pass.
- 07:40 item 10 remains implemented on W8's wire side. Observations carry
  operational unit kind/ID, holder, live claim generation, Job namespace/name/UID
  and node UID. Eval source generation remains distinct from live eval generation.
  **A1-38 is not closed.** `EngineController.SyncLauncher` delegates to
  `LauncherSyncReader.ReadLauncherSync`, which still has no production store
  implementation. W1/W2 must transact authority, award and Job-instance correlation.
- 09:40 item 4 remains done. `KeepAliveReasons`, `LocalPlatformPolicies`,
  `CloudUnitPolicies` and `CloudPodInvariants` have no Go declarations or callers.

A1-39 advances through deletion of the orphan source/build entry point. The
existing A1-08/09/18/19/26/27 wire, queue and native-input components pass.
A1-11/12/36/45/46/48 retain the shared sealed source, environment, settings,
storage and identity contracts. Production remote `NativeHostStore`, source
authority, per-process delegation and fresh per-attempt storage remain
W2/W3/W7 assembly work. Cloud plan/eval hosting and adoption of the initialized
Cloud checkout remain W3/W8 integration work.

A1-40/41 retain the elected local store, queue, leased maintenance and managed
lifecycle composition. The full localcontroller suite passes, including orphan
recovery before readiness and joining workers before store close. Its `State`
and HTTP services are still contract fakes. W1's live activity projection and
typed incompatible-store signal, W2's local peer authorization, and the
foreground CLI's production `localcontroller.Config.Bind` remain open.
No second store opener or SQL projection was added around those missing seams.

A1-10/20/43/49/52 retain the selected-engine installer, private credential-file
startup, enrollment cleanup, signed updates and release support. Production
provisioning and native Windows evidence remain open. A1-42 still needs real
`KubernetesBoundary.Probe` and `Clear` deployment bindings plus network, scratch
and teardown evidence. A1-50/51/53/55 retain their native here/CLI, old W3 host
retirement and real-repository QA obligations.

No foreign contract declaration changes are requested by this cut. The existing
missing integrations above are unchanged. There is no new design decision,
production mechanism, environment variable, limit or compatibility API.

## Production line count

Against starting HEAD, production Go adds **0** lines and deletes **348**.
**Net production delta this session: -348 lines.** W8 accounts for -20 and
shared W3 caller cleanup accounts for -328. Tests and Markdown are excluded.

Using the lead's exclusions against main, W8 is **+4,165 production lines**,
which is **6,465 over** its -2,300 budget. Cluster is -1,971, engine +2,217,
runners +1,521 and wingd +2,398. This remains incomplete. Signed-engine
distribution and cross-platform local/service lifecycles account for substantial
additions, but this session does not establish that their size is necessary or
that a smaller implementation cannot satisfy the design. No budget waiver is
requested. The shared deletions above count toward W3 under the lead's ownership
measurement, not toward W8.

## Verification

All Go checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR=/tmp/sw-astra-W8. Package concurrency is
at most four. The final focused source/caller run also sets TMPDIR to that scratch.

- Whole-repository `go build -p 4 ./...` passes after the cut.
- Full `go test -p 4` for internal/runners/..., internal/cluster and
  internal/wingd/localcontroller passes.
- Focused orchestrator repository-refusal and source-URL suites pass.
- `tests/acceptance -run 'W8|TestLauncherCloud'` passes.
- Focused vet over runners/..., cluster, orchestrator and localcontroller passes.
- Full runners and cluster suites pass with the race detector.
- `go run ./internal/commentcheck -base main .` passes across 2,119 Go files.
- `git diff --check` passes. No local user or core.worktree override is set.

## Shared files and friction

Shared production files deleted are `internal/orchestrator/run_node.go` and
`run_node_options.go`. Shared production files changed are `run_node_remote.go`
and `pod_node_host.go`. Shared tests changed are `repo_admission_test.go`,
`source_url_test.go` and `trigger_host_test.go`. The old
`run_node_remote_cancel_test.go` is deleted. The shared port inventory is updated.
W8 changes are prom.go/prom_test.go, run_input_test.go, the new
source_cancel_unix_test.go and HOSTING.md. Lead-owned handoffs and amendments
are untouched.

Friction: Overwing reports its installed binary behind source, but standards
matching and reading work. W3's remaining legacy source helpers still need final
ports. Full old orchestrator runtime execution remains outside this focused cut.
No Sparkwing binary, pipeline, deployment, release, external write or Git write
was run. Scratch is removed at session end. The shared build cache is retained.
