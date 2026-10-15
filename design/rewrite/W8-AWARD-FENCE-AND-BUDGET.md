# W8 award fence, runner composition and budget

Continues `8a5498096`. **W8's A1-38 launcher-side award fence and claim-generation
correlation are closed.** The controller/store integration remains separate.

## Implementation

Invariant: a machine runner hosts only sources admitted by its captured
`runner.repos`, using its captured claimant and loop selection.

`RunnerCLI` now constructs `NewManagedRunner` itself. Its factory returns
`runners.RuntimeConfig` services rather than an assembled runner that could
omit the machine repository fence. The CLI captures the parsed repository list,
controller URL, bearer and loop settings before calling the factory. It clones
loop slices. Factory edits to the supplied options or returned claimant settings
cannot replace that capture. The factory still supplies HTTP transport, hosting,
signed policy, installation and capacity services.

Removed `RunnerOptions.Actions`, the raw issued capability, and the direct
factory-to-loop path. Actions supplies the in-memory bearer callback and claim
expiry. No alias preserves the old factory signature. The new property first
failed with a foreign accepted source reaching the host, then passed after
construction moved to the CLI. It also checks forged factory transport settings
and mutation of the controller pointer, repository slice and claim-kind slice.

No new package, setting, environment variable, retry or compatibility path was
added. The existing shared constructor fixes the composition gap.

## A1-38 evidence

Invariant: reconciliation can retire only the observed Job instance for the
observed immutable award, and cannot release its reservation before teardown.

- Persisted bindings retain unit kind/ID, holder and claim generation. Eval
  retains its live expansion lease separately from terminal source generation.
- Observations send that fence, actual Kubernetes namespace/name/UID and reserved
  node UID. Sync keeps its original Job identity separately from mutable request
  data. Returned credentials and changed, duplicate or missing decisions fail.
- Restart reconciliation handles old and current generations of the same unit
  together. Cleanup matches the complete observed Job reference. Job deletion
  uses the original UID precondition before reservation release.
- `TestStoreLauncherW8` exercises the default shared HTTP client, real controller
  handlers and store authorization. Its transactional sync reader and Kubernetes
  transport remain fakes. That does not reopen W8's completed wire correlation.

Existing regression/property tests cover these requirements. No duplicate test
was added to claim completion. `FuzzControllerSyncSeparatesClaimGenerations`
passed 7,177 generated executions this session. The independent reviewer checked
the fence, both eval generations and the UID precondition with no finding.

W1/W2 must bind `LauncherSyncReader.ReadLauncherSync` to transactional ancestry,
live-award and Job-instance checks. The launcher cannot supply that store check.

## Production lines

Counts exclude tests, testdata, acceptance code and contract declarations.
Charts have no production Go delta. No W3-owned code was changed or deleted.

| W8-owned package | Net production Go lines against main |
| --- | ---: |
| `internal/cluster` | -1,967 |
| `internal/engine` | +2,045 |
| `internal/engine/sdkinstall` | +176 |
| `internal/runners` | +1,376 |
| Removed `internal/runners/k8s` | -1,465 |
| `internal/runners/launcher` | +1,058 |
| `internal/runners/runnerflags` | +64 |
| `internal/runners/service` | +553 |
| `internal/runners/service/unitfile` | +376 |
| Removed `internal/runners/warmpool` | -406 |
| `internal/wingd` | +1,679 |
| `internal/wingd/client` | +413 |
| `internal/wingd/localcontroller` | +306 |
| **W8 total** | **+4,208** |

W8 is **6,508 lines over its -2,300 budget**. This session adds 23 and removes
19 production lines, **net +4 W8 production lines**. It changes **zero production
lines outside W8**. No further legacy package was deleted this session. The
previously deleted worker, pool, trigger, pod and dispatcher paths stay deleted.

## Package retention justifications

`internal/engine` retains 2,045 lines because decisions 101-103 require exact
Go/npm pin selection, signed policy with rollback refusal and verified fallback,
closed-set release verification, executable caching and verified repository and
runner upgrades. It reuses releaseasset/releaseauth rather than introducing a
trust root or mirror. Those decisions were not sized in the frozen budget.
Removing one of these owners would drop a named contract requirement. Together
with sdkinstall, the engine additions total 2,221 lines.

`internal/engine/sdkinstall` retains 176 lines because the verified selected
engine must own dependency installation and compilation for both SDKs. The child
package keeps the binary-cache dependency out of the engine identity package,
which runspec/store already import. Folding it into engine creates an import
cycle. Letting each placement install separately restores the removed copies.

`internal/runners` retains 1,376 lines because it owns the single claim loop,
pending-award/stop outcomes, immutable host inputs, repository admission,
credential-file startup and native verified replacement. Hosts keep the shared
lease keeper. The factory shortcut is now removed. A single synchronous claim
would lose cancellation, policy refresh and committed-award adoption while hosts
are active. Native update code preserves the existing registration and paths.

`internal/runners/runnerflags` retains 64 lines because CLI parsing and installed
service inspection consume the same registered grammar. Replacing it with
separate flag tables would recreate the drift this cut removes. It adds no
independent command vocabulary.

`internal/runners/service` retains 553 lines because enrollment and automatic
updates must install, inspect, refresh and remove the existing Linux/macOS runner
registration while preserving stopped state and refusing foreign registrations.
It replaces the removed 645-line `internal/agentservice` package, whose deletion
is outside W8's directory accounting and is not credited in the table above.
Dropping inspection or native source checks would let refresh affect another
configuration or a loaded registration from another path.

`internal/runners/service/unitfile` retains 376 lines because runner services,
controller services and schedules share native argv encoding/decoding, atomic
file publication and loaded-registration checks. This replaces separate native
parsers and keeps dollar/percent escaping, XML and command refresh in one owner.
Using shell splitting or string replacement alone loses those required argument
and registration checks.

`internal/wingd/localcontroller` retains 306 lines because it opens the elected
store once and composes the shared controller, queue, recovery and singleton
maintenance. It joins queue/activity/maintenance before closing services and the
store. Folding it into wingd creates a cycle through runners/nodehost. Opening
the store in each adapter would violate the single-owner contract.

The existing launcher package grows by 1,058 lines because one builder must cover
plan/eval/work, mandatory root/DinD, private one-use bootstrap transport,
suspended publication, exclusive node reservations, network evidence and
restart-safe UID-fenced cleanup. Kubernetes construction and sync reuse shared
wire contracts. A Job-name-only map or immediate reservation release cannot
satisfy A1-38/A1-42. Deployment probe/clear implementations remain open below.

The existing wingd and client packages grow by 2,092 lines because the local
controller adds readiness, four activity holds, shared HTTP/dashboard drain,
detached startup, native managed units and peer-authorized Unix/Windows
transports. Windows SID and private socket-handle checks replace unsupported
stubs. The retained admission daemon, journal and supervisor serve the unchanged
`d.sock` contract. Removing them to meet the budget would remove required local
admission behavior. These are retention justifications for lead review, not a
claim that the original budget is met.

## Verification and ports

- Whole-repository `go build -p 4 ./...` passes.
- Full cluster, runners, launcher, service/unitfile, engine and sdkinstall suites
  pass. Focused vet passes.
- `go test -p 4 ./tests/acceptance -run 'W8|LauncherCloud'` passes. No native
  Windows, Darwin, Kubernetes/DinD or real-repository acceptance cell newly closes.
- Repository-fence property passes 4,672 generated executions. The focused
  cluster/launcher race selection passes, including generation and Job identity
  properties. The existing native-source/store fixtures retain their limits.
- `go run ./internal/commentcheck -base main .` passes across 2,124 files.
  `git diff --check` passes. Independent review found no actionable defect or
  unnecessary mechanism in the changed constructor boundary.

No legacy tests were newly ported. Existing W8 port dispositions remain in
`contracts/t1-port-inventory.md`. The changed acceptance tests retain their
credential-file and installed-service assertions with the new factory result.

## Amendment audit and remaining integration

- A1-08/09/18/19/26/27/36/39/51: unified queue, selected identity and engine,
  immutable inputs, shared hosts/keeper and old W8 executor deletions remain
  implemented. Real per-process source, log, storage and controller adapters and
  actual hello still require final production composition.
- A1-10/11/12/43/45/46/48/50/52/55: selected-engine installation, captured source
  and environment, policy fallback, historical matrices and named here claims
  remain implemented. Native Windows bridge packaging, full CLI/source wiring
  and placement QA remain integration work.
- A1-20/49: private credential startup and CLI repository binding are implemented.
  Production enrollment/revocation and Actions issuance are W2/W9 integration
  points. The factory no longer owns enforcement of `runner.repos`.
- A1-38: **W8 closed**. W1/W2 transactional sync reader binding remains open.
- A1-40/41: local election, store/queue/maintenance, readiness, four holds,
  managed units and joined drain remain implemented. Production state/activity,
  peer-authorized controller handlers and CLI service bindings remain open.
- A1-42/53: the deployment must implement Probe/Clear against its actual network
  policy, DaemonSets and node lifecycle. No manifest-only success or no-op clear
  is supplied. Enforced egress, exclusive placement, sidecar teardown, aggregate
  scratch eviction and full native QA still need the deployment lane.

The approved pending-claim outcome/cancellation redesign and the four dead
declaration removals remain finished. Lead-owned handoffs and amendment files
are unchanged. `cmd/sparkwing-runner` still calls `MainWithVersion`, so its
production Runtime bindings remain explicit integration work rather than a
claimed working pod entrypoint.

No new foreign contract change is requested. The existing W3/W6 request to
distinguish an uploaded working-tree snapshot from a committed Git-bundle seed
remains unresolved. The source contract still carries only BundleDigest and
remote/local acquisition location, so that consent distinction cannot be
implemented safely in W8 alone.

Shared files changed are only `tests/acceptance/runner_credentials_w8_test.go`
and `tests/acceptance/runner_service_w8_test.go`. No production caller outside W8
needed migration. No file under internal/orchestrator or another W3 package was
touched. Source docs changed in runners/HOSTING.md and launcher/CLOUD-JOBS.md.

Friction: native sockets/deployments are unavailable in this sandbox. Overwing
reports a binary ten commits behind source, but standards discovery and reads
succeed. No Git write, Sparkwing execution, candidate install or deployment ran.
Worker scratch is removed after checks. `/tmp/sw-gocache` is retained.

STATUS: CONTINUE
