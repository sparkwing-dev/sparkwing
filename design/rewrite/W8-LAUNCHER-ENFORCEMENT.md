# W8 launcher enforcement continuation - 2026-10-17

The launcher fixes below are committed in 37ed391e6. The lead's native report
after that commit contains FAIL, including build failures and a burst CLI SDK
installation failure. Completion remains blocked on a passing harness report.
The lead owns commits, independent review, the whole-repository suite and native
verification. This worker leaves the harness report unchanged.

## Implemented

1. Admission preserves policy-selecting metadata. The shared admitted-Pod
   verifier compares both key presence and value in both directions for labels
   and annotations. Only the existing explicit owner-label and validated ENI
   exceptions survive. Empty values cannot make missing and present keys equal.
   The generated production-boundary regression injects empty metadata while
   Reserve creates its probes. Its label case includes an additive egress policy
   selected by DoesNotExist. A separate generated property covers removing as
   well as injecting empty labels and annotations.
2. An unauthenticated capacity identity cannot retain capacity or stop unrelated
   work. Capacity observations are reconciled individually. Forbidden with
   reason=binding and lock_held consume only that request's refusal, log it and
   delete the exact observed Pod with a UID precondition. Other requests still
   authenticate and Run still reaches LaunchOne. Deletion failures remain loud.
   Transport, unavailable, rate-limited, service-credential and missing-grant
   failures preserve the Pod and retry. The restart property changes only the
   recorded holder, uses a controller that refuses it and tests replacement
   between observation and deletion. An actual Run regression proves claiming
   continues after the refusal.
3. Controller latency cannot control local enforcement cadence. Run owns a
   separate local goroutine and poll ticker. Every verification pass has its own
   deadline of one poll interval. Shutdown cancels and joins the goroutine.
   Controller reconciliation and claiming retain their existing cooldowns.
   Local passes also retry recorded rejections after interrupted containment
   and discard malformed Job ownership. Otherwise those states would still
   wait for controller reconciliation after a local deadline or API failure.
   Tests block the controller call until cancellation, mutate Jobs and requests
   only after it starts, and verify containment before unblocking it. Separate
   in-memory HTTP transport tests block Kubernetes reads until their deadlines
   and verify later passes still start. No test sleeps or sockets were added.
4. The boundary comment and both launcher documents now account for probe Pods.
   Reservation removes requests before probing. Probes occupy the node during
   measurement and leave before Job creation. consolidateAfter covers the empty
   gaps around those probes.

These implement the lead's approved structural fixes for the repeated verifier,
demand-authority and cadence mechanisms. No further DESIGN decision is requested.

## Contract text for the lead

No contract file changed. The lead already corrected C4's obsolete capacity
identity in commit 2c5a20291, merged into this branch. Retain or supersede its
capacity paragraph with this text as needed:

> The launcher creates at most one capacity-request Pod per immutable award.
> Its name is sw-capacity- followed by the Job award hash of unit ID, team, run,
> node, kind, generation, attempt, eval source node and eval source generation.
> Overlapping generations have separate names. Only the same award adopts an
> existing request. Requests are never rebound. The name locates an award and
> does not authenticate its holder or allocation. The recorded demand only
> supplies an observation. Create and adoption verify against the captured
> award, local passes use the held award where available, and reconciliation
> uses the controller's binding and committed cloud_resources. A definitive
> binding or fence refusal contains only the observed Pod by UID and does not
> abort unrelated reconciliation or claiming. Other controller failures retry.
> Local verification and containment run on a separate poll ticker with their
> own per-pass deadline, independently of controller cooldowns and in-flight
> requests. Shutdown cancels and joins that local worker.

No Go contract shape, wire format or foreign API changed. No foreign caller
migration was needed. No amendments or handoffs file was edited.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, GOTMPDIR and TMPDIR under /tmp/sw-astra-W8, and -p 4.
Commands use go -C /home/bombuntu/.xwing/envs/1086/sparkwing.

- Whole-repository build ./... passes.
- Vet passes for launcher, cluster, controller, cmd/sparkwing and acceptance.
- The full launcher suite passes. The blocked-controller, local-deadline,
  interrupted-containment, restart-refusal, Retry-After and new property seeds
  pass with the race detector.
- Focused cluster, controller and acceptance tests pass: TestLauncher*,
  TestStoreLauncher*, TestSurfaceLauncherSync*, TestProductionLauncher*,
  TestEngineLauncherSync* and FuzzStoreLauncherReservationFenceW8 seeds.
  This retains the SQLite/controller/Kubernetes component proofs for award
  authority, reservation fencing, publication, Pod rejection and restart sync.
- Generated fuzzing passes: production boundary reservation 335 executions,
  empty metadata membership 1,000, and capacity identity refusal 1,015.
- Commentcheck is clean across 2,881 Go files. git diff --check and
  bin/sync-docs.sh --check pass. Git has no local user identity or core.worktree
  override. The worker scratch directory is removed and the shared cache kept.

The initial regressions failed on the committed code for each assigned major
finding. The interrupted-containment and malformed-ownership cases also failed
before the local retry change. Generated capacity fuzzing exposed an invalid
UTF-8 fixture: JSON normalization caused shape rejection before the intended
identity-refusal path. The generator now encodes arbitrary bytes into valid
forged holder text and retains that byte input as a seed.

No acceptance catalog skip was removed and no new native placement proof or
historical T1 test port is claimed. Previously completed W8 amendment and handoff implementations remain intact. A1-38's launcher fence/transport and A1-42's local enforcement are
retained; A1-42/A1-53 still need target-cluster and full native placement evidence.
No foreign stub blocks these fixes. Real EKS admission/defaulting, effective RBAC,
escaped-node networking, DinD termination and scratch cleanup remain integration
proofs for the lead. Windows/macOS and real-repository QA remain native lanes.

## Deletion, budget and handoff

The implementation session changed production Go by +86/-39, net +47. No whole
production file was deleted. The synchronous enforcement scheduling and batch-wide capacity
failure path were replaced. Removed the 65-line fake/test that claimed to stall
but only exercised cooldown. Prior legacy runtime and admission-policy cuts stay
deleted. No compatibility surface was added.

W8-owned production Go against main (244d71bd2), using the lead's filter excluding
tests, testdata, acceptance and contract paths: +13,215/-6,898, net +6,317.
Directory net: cluster -1,535, runners +2,933, engine +2,552, wingd +2,367,
charts 0. The -2,300 budget remains unmet. The implementation's extra loop and
per-request refusal handling were explicitly assigned and add 47 lines.
A synchronous loop cannot satisfy controller-independent
cadence. Existing signed-engine materialization, local-controller lifecycles,
Karpenter proof, probes and reservation cleanup remain required implementations
behind the earlier budget justification. Removing them would reopen assigned
contracts. The lead must adjudicate the overage; this report grants no waiver.

All changed code and tests are under internal/runners/launcher. Documentation
changes are that package's CLOUD-JOBS.md, design/rewrite/W8-SCALE-FROM-ZERO.md and
this report. No shared production file changed. Source behavior tests cover the
updated documentation claims. Public CLI/config/API docs and generated mirrors
have no behavior change from this slice.

Assumption/mechanism: the new local worker reuses Poll for both ticker and pass
deadline. It adds no setting or execution limit and does not reject a Pod for a
transport timeout. The smaller sequential scheduling was rejected because a
blocked controller prevents subsequent enforcement. Undo requires restoring the
old scheduling, which reopens the assigned cadence finding.

Friction: generated fuzzing must first compile instrumented Kubernetes/controller
dependencies. The sandbox cannot run native listeners or commit Git metadata.
Overwing discovery works but reports an older installed binary. No pipeline,
installation, service, deployment, release or Git write ran. Worker scratch is
removed after verification; the shared cache is retained.

## Follow-up at 37ed391e6

All five assigned review fixes are already committed. The remaining W8 launcher
handoffs retain their implementations. No runtime code, test, shared file or
lead-owned contract changes in this follow-up. Production lines added/deleted
are 0/0. The W8 net delta is +6,317 against main using the lead's filter, correcting
this report's earlier +6,039 count. No new acceptance cell or historical test port
is claimed.

The native report contains package build failures without their compiler
diagnostics. The one detailed failure is
`TestBurstCLIRejectsNonPreferredAndTeamClaimants/{false_false_false}`. Its selected
engine SDK installer reports missing standard-library packages under the host's
Go 1.26.9 installation, including `math/rand/v2`, `encoding/asn1`,
`crypto/tls/internal/fips140tls` and `net/netip`.

Those directories exist in this session. Whole-repository `go build -p 4 ./...`
passes. The exact reported subtest passes with
`go test -p 4 ./cmd/sparkwing -run '^TestBurstCLIRejectsNonPreferredAndTeamClaimants$/\{false_false_false\}$' -count=1`.
The full `TestBurstCLIRejectsNonPreferredAndTeamClaimants` test also passes,
including all four sequential subcases.
The full `internal/engine/...` suites pass, including real SDK materialization.
Launcher and engine vet pass. The full launcher suite passes with the race
detector, including the production-boundary metadata properties, restarted
forged-holder refusals and tests that block controller requests. Focused launcher
tests in cluster, controller and acceptance pass, using the test selection
recorded above. Commentcheck is clean across 2,881 Go files.
The reported failure has not reproduced, so there is no demonstrated runtime
cause to patch. The lead needs a native rerun and compiler diagnostics for any
remaining package build failures. Local passes do not supersede that report.

The C4 capacity identity proposal remains in "Contract text for the lead" above.
Real EKS admission, RBAC, network enforcement, DinD teardown and scratch cleanup,
plus the native placement and platform lanes, remain integration evidence for
the lead. No foreign implementation stub blocks the five assigned fixes.

`git diff --check` passes. Only this report changed. Worker scratch is removed
and `/tmp/sw-gocache` is retained. No Git write, pipeline, deployment or release
ran.

STATUS: CONTINUE
