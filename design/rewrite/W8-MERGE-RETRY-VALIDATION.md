# W8 merge, retry validation and supervisor retirement

A1-38 is closed for W8. Launcher award fences, independent eval lease generations,
Job namespace/name/UID correlation and UID-preconditioned cleanup remain implemented.
W1/W2's transactional sync reader is still a separate integration obligation.

## Implemented

Resolved the working contents of all 44 conflicted paths. W8 implementation wins
in its packages, with the amended `ClaimedUnit.HostContext` supplying deadlines.
Other areas' conflicts use the integration version, including its deletions.
No conflict markers remain. No git writes ran, so the index still has 44 unmerged
entries. The lead must stage these resolutions and finish the merge.

Invariant: origin request/spec seals remain distinct from the current execution
identity. `runners.ValidateAwardIdentity` now owns that check for hosting,
launcher claim and bootstrap exchange. Accepted origins must match the request's
run, team and hash. The claim binding must name the execution run. Retry host
inputs continue to name that execution while retaining the original seals.
Generated entrypoint tests refuse changed origin IDs, teams, hashes and claim
bindings. The three W9 review regressions now run in the default test suite
against current APIs instead of remaining behind `rewrite_integration`.

Restored `wingd.ResolveBudget` through the amended `userconfig.AdmissionConfig`.
The merged CLI requires this API. Tests cover explicit precedence, config source,
ignored retired environment, missing config, malformed budgets and unknown keys.
Removed an uncalled legacy client file that referenced the deleted `Client` type.
The whole repository builds again.

Lead decisions 07:40 items 5, 6 and 10 are satisfied on W8's side. The local
controller exposes `HTTP() LocalHTTP`, the runtime owns serving/drain, launcher
parsing accepts `--docker-image` and refuses `--deadline`, and A1-38's launcher
portion is closed. The four tables named at 09:40 item 4 remain deleted:
`KeepAliveReasons`, `LocalPlatformPolicies`, `CloudUnitPolicies`,
`CloudPodInvariants`.

## Deleted

Deleted `internal/wingd/supervise` because the merged CLI starts `wingd run`
directly and no production package imports the supervisor. The 12 deleted files
contain 739 production and 1,365 test lines. The existing client startup,
lifecycle, historical-hosting-release and local runtime tests remain. Updated
`LOCAL-CONTROLLER.md` to describe the direct process. Historical release notes
and the test that inspects an old release tag retain their historical paths.

Outside W8, deleted `internal/controller/client/claim_input.go` (46 production lines).
It has no callers and uses the retired client/error API. This is the only foreign
production repair beyond choosing integration versions during conflict resolution.
No W3 implementation was changed after resolving its conflicts to integration.

## Production line budget

The lead's metric excludes tests, testdata, acceptance code and paths containing
`contract`. These counts compare the complete working tree to `main`.

| W8-owned path | Net production Go lines |
| --- | ---: |
| `internal/cluster` | -1,967 |
| `internal/engine` | +2,016 |
| `internal/engine/sdkinstall` | +171 |
| `internal/runners` | +1,381 |
| Removed `internal/runners/k8s` | -1,465 |
| `internal/runners/launcher` | +1,062 |
| `internal/runners/runnerflags` | +64 |
| `internal/runners/service` | +524 |
| `internal/runners/service/unitfile` | +376 |
| Removed `internal/runners/warmpool` | -406 |
| `internal/wingd` | +1,708 |
| `internal/wingd/client` | +413 |
| `internal/wingd/localcontroller` | +306 |
| Removed `internal/wingd/supervise` | -739 |
| **W8 total** | **+3,444** |

W8 remains 5,744 lines above its -2,300 budget. Against starting HEAD, W8 adds
58 and removes 759 production lines, net -701. Foreign integration changes are
excluded from that W8 total. The separate foreign repair removes 46 lines.
No chart source changed in this session.

The retained new packages require these overage justifications for lead review:

- `engine` adds 2,016 lines because decisions 101-103 require exact SDK pins,
  signed release verification/cache, historical closed matrices, a signed version
  index with verified fallback, revocation and verified upgrades. These behaviors
  were not sized in the frozen budget. Existing shared release trust and matrix
  owners supply verification rather than a second trust system.
- `engine/sdkinstall` adds 171 lines because the selected verified executable
  must install and compile both SDKs. It reuses the local Go build cache and npm
  instead of adding separate installation implementations per placement.
- `runners` adds 1,381 lines because one loop must adopt committed awards during
  interruption, refresh policy while hosts renew, drain before replacement and
  preserve independent failures. The same package captures seals, authenticates
  native inputs and enforces the machine owner's repository fence. Source
  acquisition and lease renewal stay with the shared hosts.
- `runnerflags` adds 64 lines because entrypoints and installed-service inspection
  must consume the same declared grammar. It adds no vocabulary of its own.
- `runners/service` adds 524 lines because Linux/macOS install, inspect, refresh
  and uninstall must preserve arguments and native policy while refusing foreign
  registrations/configurations. The removed legacy agentservice package is
  outside W8 and receives no credit in the table.
- `runners/service/unitfile` adds 376 lines because runners, controllers and
  schedules share native argument escaping/parsing, atomic registration writes
  and loaded-unit checks. Replacing it with separate service parsers duplicates
  those rules.
- `wingd/localcontroller` adds 306 lines because the elected process must open
  the store once, compose its queue/maintenance and join workers before close.
  Its service binder supplies the shared authorization and native hosts without
  another store opener.

The existing launcher grows by 1,062 lines for plan/eval/work awards, private
bootstrap publication, root/DinD, exclusive reservations, network evidence and
restart-safe teardown. Existing wingd/client packages grow by 2,121 lines for
local lifecycle, four activity holds, managed units, joined serving/drain and
OS-authenticated transports. Their admission behavior is retained. The unused
739-line supervisor is removed instead of justifying its retention. Remaining
overage still needs the lead's decision.

## Verification

- Whole-repository `go build -p 4 ./...` passes with `GOWORK=off` and
  `GOFLAGS=-buildvcs=false`, including after supervisor deletion.
- Full cluster, runners/launcher/runnerflags/service/unitfile, engine/sdkinstall
  and localcontroller package tests pass with at most four package workers.
- Focused wingd budget/admission tests and client lifecycle/API/historical-hosting
  tests pass. Direct CLI and controller-client test packages compile.
- `go test -p 4 ./tests/acceptance -run 'W8|LauncherCloud'` passes. Existing
  bootstrap, native-store, launcher, local-controller and runner components stay
  green. No native placement cell is newly closed.
- Retry entrypoints, launcher generation correlation and restart cleanup pass
  under `-race`. The expanded launcher retry property passes 7,738 fuzz executions.
  Hosting's generated property covers 128 randomized cases per invocation.
- Focused W8 vet passes. Commentcheck is clean across 2,070 files.
  `git diff --check HEAD` passes.

No archived T1 test was newly ported. The W9 retry regressions were migrated to
`internal/runners/retry_award_test.go` and
`internal/runners/launcher/retry_award_test.go`. Earlier T1 port dispositions
remain in `contracts/t1-port-inventory.md`.

## Remaining integration and handoffs

The per-row amendment dispositions from `W8-RETRY-AND-SIZE.md` remain. This session
advances the retry/identity and local-runtime entries without reopening completed
ports. No foreign contract change is needed for the shared validator.

- W1/W2 must implement the existing `LauncherSyncReader.ReadLauncherSync` with
  transactional ancestry, award and observed-Job checks. Only its interface and
  test implementations exist in this checkout.
- W2's `claimedUnit` still clears `Execution` for every non-node kind. Cloud
  plan/eval need their resolved resources under the existing wire contract.
- W2/W3/W9 must supply authenticated remote private state and principals.
  `NativeHostStore` needs snapshot/node reads and transitions that are absent
  from the remote scheduling projection. W8 must not manufacture authority by
  decoding a bearer or add another wire contract.
- W3 must provide the native Cloud plan/eval lifecycle and initialized-checkout
  consumption contract. An owner host that runs the complete lifecycle cannot
  substitute for a controller-owned plan-only pod.
- W8 still owes final executable Runtime/service bindings and deployed
  `KubernetesBoundary.Probe/Clear`. Local production State/HTTP/claimant/native
  service construction and CLI factory composition remain open. The merged
  `cmd/sparkwing/local_services.go` still returns unavailable from its factory.
- Working-tree consent still needs the source contract to distinguish uploaded
  working-tree content from a committed Git-bundle seed. Enrollment, startup
  bootstrap issuance and rollback retain their W1/W2/W6/W9 owners.
- Enforced egress, DaemonSet inventory, escape canaries, aggregate 20 GiB scratch,
  complete DinD teardown, native Windows/Darwin and real-repository placement QA
  require deployment/native evidence. Component fakes do not close those cells.

The earlier pending-claim outcome/cancellation redesign and W8 legacy
worker/pool/trigger/pod cuts remain implemented. Engine distribution and service
preservation components pass. The default runner executable still supplies an
empty Runtime, so this report does not claim production composition complete.

Friction: the no-git-writes rule leaves staging/merge completion with the lead.
Sparkwing pipelines/candidate execution and native/deployment evidence remain
with the lead under the brief. The first broad acceptance invocation was
interrupted after W8 package checks finished, then replaced by the focused W8
selection above. Race/fuzz compilation was cold and substantially slower than
test execution. Worker scratch is removed at session end. The shared
`/tmp/sw-gocache` is retained.

## Resolved conflict manifest

These are the index paths the lead must stage, including deleted paths:

- `.apidiff/pkg_controller_client.txt`
- `.apidiff/pkg_localws.txt`
- `cmd/sparkwing/crons.go`
- `cmd/sparkwing/crons_test.go`
- `cmd/sparkwing/here_resume.go`
- `cmd/sparkwing/here_resume_test.go`
- `cmd/sparkwing/pipeline_trigger.go`
- `cmd/sparkwing/pipeline_trigger_here.go`
- `cmd/sparkwing/profile_guard_test.go`
- `cmd/sparkwing/run_detached.go`
- `cmd/sparkwing/run_detached_process_test.go`
- `cmd/sparkwing/run_dispatch.go`
- `cmd/sparkwing/run_risk_gate_test.go`
- `cmd/sparkwing/wingd.go`
- `cmd/sparkwing/wingd_runtime_test.go`
- `docs/migrations/_unreleased.md`
- `internal/clisurface/registry.go`
- `internal/cluster/launch_cli.go`
- `internal/cluster/launch_cli_test.go`
- `internal/orchestrator/lapsed_lease_repro_test.go`
- `internal/orchestrator/local_trigger_loop.go`
- `internal/orchestrator/local_trigger_loop_test.go`
- `internal/orchestrator/pipeline_requires_test.go`
- `internal/orchestrator/standalone_test.go`
- `internal/orchestrator/submission_environment.go`
- `internal/orchestrator/submission_environment_test.go`
- `internal/orchestrator/submission_environment_windows_test.go`
- `internal/runners/execution.go`
- `internal/runners/hosting.go`
- `internal/runners/launcher/controller_test.go`
- `internal/runners/proxy_mask_test.go`
- `internal/runners/run_input_test.go`
- `internal/controller/client/client.go`
- `internal/controller/client/livelogs.go`
- `internal/controller/client/logs.go`
- `internal/controller/client/pace.go`
- `internal/controller/client/pace_test.go`
- `internal/controller/client/poll_advice.go`
- `pkg/docs/mirror/migrations/_unreleased.md`
- `pkg/localws/localws.go`
- `pkg/localws/localws_test.go`
- `pkg/localws/origin_test.go`
- `internal/store/default_team_callers_test.go`
- `tests/acceptance/launcher_controller_w8_test.go`

STATUS: CONTINUE
