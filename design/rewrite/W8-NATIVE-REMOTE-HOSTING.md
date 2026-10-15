# W8 native remote hosting

Continuation from `55261512b`. Remote `Hosting` now constructs the shared native
owner and claimed-job hosts, through the same persisted-input check as
`StoreHosting`. Its old preparation callbacks, outer lease keeper, injected
keeper starter, watch callback and prepared-node identity override are removed.
Native hosts own source acquisition, renewal and joining. There is no second
runner keeper around them.

`Hosting.Bind` returns native services and the current authenticated C2 principal.
The services contain an award-bound `nodehost.NativeHostStore`. The runner keeps
the claimant transport separate from each award's transport and confirms renewal
before binding. A cancelled confirmation cannot reach service construction.
Partial services close on every exit, and binding failures retain an independent
cancellation cause.

The invariant is: source acquisition can use only the authenticated award's
persisted run, job, seals, engine, settings, holder, generation and attempt.
The award is captured before callbacks. Both placements use persisted rows and
`nodehost.NewNodeInput`. The remote-store row must name the requested job.
Binding comparison preserves exact fields and compares deadline instants across
JSON transport without depending on Go's monotonic clock or zone identity.

## Deletions and evidence

Production Go adds 65 lines and deletes 152 against starting HEAD.
**Net production delta this session: -87 lines.**
Tests add 351 and delete 928, for -577 test lines. Markdown is excluded.

The deleted `nodeExecution` constructor and `.input` override duplicate W3's
persisted-input constructor. The removed hosting tests exercised the deleted
preparation API and its fake outer keeper. Their surviving obligations move to
persisted-input properties, the real native-owner composition and the existing
shared-host lease/source tests. Bootstrap tests still prove real C2/C1 exchange,
one-use ancestry, zero-to-one start, fenced completion and replay refusal. Their
unit host is now an explicit component fake. They make no source-lifetime claim.

`TestNativeStoreOwnerW8` now also passes `remote_award` and `managed_remote`.
These construct the native owner through remote Hosting and NewManagedRunner,
retain granted labels across binding mutation, complete through real C1/C2 and
close services. The transport and remote store adapter are in-process fixtures.
The embedded queue, award-snapshot and elected local-controller variants pass too.

`FuzzNativeInputsRejectChangedAwards` passes 1,000 generated executions.
`FuzzNativePrincipalBindingFence` passes 1,000 generated executions. The first
covers mutated request/spec/plan seals, settings, engine and job/holder/generation/
attempt fences. The second covers transport-preserved instants and changed
deadlines. Existing native-store, authority, mixed-claim, bootstrap, launcher-sync
and pending-claim properties pass in the focused suites.

No native placement acceptance cell or historical T1 port is newly closed.
The changes replace rewrite-era component arrangements. Deployed and native
platform evidence remains open.

## Lead decisions and amendments

- 07:40 item 5 remains implemented as `LocalController.HTTP() LocalHTTP`.
  W2 supplies authorized API/dashboard handlers. The elected runtime owns drain,
  cancellation and joining.
- 07:40 item 6 remains implemented. Launcher grammar accepts `--docker-image`
  and refuses the removed `--deadline`. The cluster suite passes.
- 07:40 item 10 is implemented on W8's wire side. Sync observations carry unit
  kind/ID, holder, live claim generation, Kubernetes namespace/name/Job UID and
  node UID. Eval source generation remains separate. **A1-38 is not closed end
  to end.** W1/W2 still need the production transactional `LauncherSyncReader`
  binding and store authority/award fence.
- 09:40 item 4 remains done. KeepAliveReasons, LocalPlatformPolicies,
  CloudUnitPolicies and CloudPodInvariants have no remaining Go declarations.
- A1-39 advances from prepared host callbacks to shared native hosts on both
  embedded and remote runner paths. Real remote transport/store composition and
  Cloud plan/eval host execution remain open.
- A1-08/09/18/26/27/55 retain the implemented claim and ordinal boundaries.
  Enrollment, billing and here authority are server-owned integration points.
- A1-10/43/52 retain signed engine installation, exact selection, update and
  release support. Windows release/runtime evidence remains with W5/W9/T1.
- A1-11/12/19/36/48 now reach the same native source, input and renewal owners
  from both runner compositions. Production source credentials, process grants
  and fresh storage bindings remain W2/W3/W7 integrations.
- A1-20/49 retain file-backed startup and enrollment cleanup. Production local
  provisioning, including the ratified bootstrap-admin-token flow, remains W2.
- A1-40/41 retain the implemented local lifecycle and managed-unit boundaries.
  Production state/activity, enrollment and authorized handlers remain unbound.
- A1-42 still needs deployed network/isolation, aggregate scratch and teardown
  evidence. KubernetesBoundary.Probe and Clear need real deployment bindings.
- A1-45/46 consume shared settings and procenv through the native hosts.
  No runner-specific precedence or credential environment builder was added.
- A1-50/51/53 remain open for CLI/native placement, retained W3 runtime retirement
  and the full native/real-repository QA matrix. Fakes do not close those cells.

## Contracts and remaining work

Only W8's Hosting binder signature changes. No foreign Go interface changes.
The W8 integration paragraph in `contracts/c3-protocol-sdk.md` and source
`internal/runners/HOSTING.md` describe the new composition. Lead-owned handoffs
and amendments are untouched.

W3/W2 must supply the production award-bound NativeHostStore and authenticated
principal together. The opaque bearer cannot be decoded to invent a principal.
The component binder takes its principal from real C2 and uses a Tenant as the
remote-store fixture. W3 still owns Cloud plan/eval lifecycle and adoption of the
initialized Cloud checkout. W8 must connect those bindings to the runner executable
once available. The node-duration metric still needs a committed-outcome source.

W2's local service binder still needs W1's typed local activity projection and
incompatible-store signal, authorized API/dashboard handlers and provisioning.
The W8 local-controller constructor and its native queue already run against the
existing binding contracts.

A remaining deletion target outside W8 is W3's orphan `RunNodeOnce` and
run_node_options.go. No production Go caller remains after the runner cuts.
Its remote acquisition helper still shares code with the retained trigger host.
This session leaves those W3 files and their historical test dispositions alone.

## Budget

Using the lead's production-line filters against `main`, W8 is **+4,197 lines**,
which is **6,497 over** the -2,300 budget. By directory, cluster is -1,951,
engine is +2,217, runners is +1,533 and wingd is +2,398.

The old cluster worker, warm-pool dispatcher, run-host Kubernetes dispatcher and
old W8 pod builders were deleted before this session. They are not counted again.
The overage includes signed engine/index/install/upgrade support from decisions
102-103 and the cross-platform local HTTP/process/service lifecycles from decision
91. These required features exceed the old deletion sizing. They already share
release verification, native hosts and unit rendering. Removing whole features
would violate the agreed scope. This report does not establish that the remaining
implementations are minimal and requests no budget waiver. W8 stays incomplete.

## Verification and shared files

All checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and
GOCACHE=/tmp/sw-gocache. Package workers never exceed four.

- Whole-repository `go build -p 4 ./...` passes.
- Full `internal/runners/...`, `internal/cluster` and
  `internal/wingd/localcontroller` suites pass.
- Focused W8/launcher acceptance components pass.
- Focused vet and race checks pass.
- Both generated property runs pass 1,000 executions each.
- `go run ./internal/commentcheck -base main .` passes across 2,122 Go files.
- Diff whitespace passes. Local Git config has no identity/core.worktree override.

Shared edits are `design/rewrite/contracts/c3-protocol-sdk.md` and these
acceptance fixtures: bootstrap_store_w8_test.go, initial_attempt_w8_test.go,
native_store_w8_test.go, runner_authority_w8_test.go,
runner_controller_w8_test.go, runner_identity_w8_test.go and
runner_store_w8_test.go. All production Go edits are under internal/runners.

Friction: Overwing reports its installed binary behind source, but standards
matching and reading work. No Sparkwing binary, pipeline, deployment, release,
Git write or external report-file write ran. Scratch is removed after checks.
The shared build cache is retained. The lead owns review and commits.

STATUS: CONTINUE
