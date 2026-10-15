# W8 launcher sync decisions

Continues `f180c2ab8` and `W8-MERGE-RETRY-VALIDATION.md`. The starting tree was
clean and the whole repository built. There was no merge to resolve.

A1-38 remains closed for W8. Retry award validation across hosting, launcher
claim and exchange remains implemented. Lead decisions 07:40 items 5, 6 and 10
remain satisfied on W8's side. The four tables named by 09:40 item 4 are absent.

## Implemented

Invariant: launcher synchronization returns exactly one decision for each
observed Job, and no decisions for unobserved Jobs. Invalid responses return no
partial state and cannot authorize response-driven cleanup.

`ControllerControl.Sync` previously ignored an unobserved award when the response
also covered every observed Job. The generated regression failed for plan, eval
and work, with either a retain or remove decision for the observed Job.

Sync now checks the complete response cardinality and matches every decision to
an observation. A populated result binding records assignment, replacing the
separate `seen` map. Observation validation already requires a nonempty unit ID.
Together these checks establish the complete one-to-one mapping without another
state owner. Existing generation, binding, UID and credential checks remain.

No public API, contract declaration, customer limit or configuration changed.
The documented extra-decision refusal in `launcher/CLOUD-JOBS.md` now holds.

## Verification

- Initial and final whole-repository `go build -p 4 ./...` pass.
- Full cluster, runners, launcher, runnerflags, service, unitfile, engine,
  sdkinstall and localcontroller tests pass. Launcher tests pass after the fix.
- `go test -p 4 ./tests/acceptance -run 'W8|LauncherCloud'` passes in 4.052s.
  No native placement cell is newly closed.
- The new property passes 38,201 fuzz executions. Focused sync, generation,
  retained-identity and cleanup properties pass under `-race`.
- Focused launcher/cluster vet passes. Commentcheck is clean across 2,070 files.
- Independent review found no correctness issue and accepted removal of the
  assignment map. `git diff --check` passes.

The initial acceptance command selected all tests instead of just W8. It failed
in existing `TestLifecycle` host-event/step fixtures with
`holder transition requires a nonnegative source attempt`, then timed out at
three minutes in `TestStorage/storage-memo-and-depcache-scope/s3-archives`.
Those failures were outside W8 and preceded this fix. The focused W8 selection
above supplies this session's acceptance evidence. No W3 file was changed.

No archived T1 test was newly ported. The only new test is
`FuzzControllerSyncRequiresExactlyObservedDecisions`. Earlier port dispositions
remain in `contracts/t1-port-inventory.md`.

## Production lines

W8-owned paths add 8 and remove 11 production Go lines this session, **net -3**.
The removed state is the separate sync assignment map and its final count check.
No legacy file or subsystem was newly deleted. Production changes outside W8
are **0 lines**. Charts are unchanged.

Against `main`, W8-owned paths total **+3,441 production Go lines**, which is
**5,741 above the -2,300 budget**. Counts use the lead's exclusions for tests,
testdata, acceptance and contract declarations.

The new packages retain these deltas and constraints for the lead's budget
decision. This session adds no package.

| New package | Net production lines | Required behavior |
| --- | ---: | --- |
| `internal/engine` | +2,016 | Decisions 101-103 add exact SDK pins, signed releases and policy, verified cache/fallback, historical closed matrices, revocation and verified upgrades. These additions were not sized in the frozen budget. Shared release trust remains the verifier. |
| `internal/engine/sdkinstall` | +171 | The selected verified executable installs/builds both SDKs. It shares the Go binary cache and npm. Folding it into engine creates a cycle through bincache, runspec and store. |
| `internal/runners` | +1,381 | One claim loop adopts committed awards during interruption, drains for updates while hosts renew, preserves stop failures and captures authenticated native inputs. Source and lease execution stay with shared hosts. |
| `internal/runners/runnerflags` | +64 | Entry points and native service inspection consume one declared grammar. It owns no vocabulary. |
| `internal/runners/service` | +524 | Linux/macOS service install, inspection, refresh and removal preserve arguments and stopped state while checking registration ownership. It replaces the deleted agentservice package outside W8's accounting. |
| `internal/runners/service/unitfile` | +376 | Runners, controllers and schedules share native argv encoding, parsing, registration-source checks and atomic publication. Separate parsers would duplicate these rules. |
| `internal/wingd/localcontroller` | +306 | One elected store handle feeds the controller, queue, reaper and maintenance. Workers join before service/store close. Folding this into wingd introduces a dependency cycle. |

Existing package deltas are cluster -1,967, launcher +1,059, wingd +1,708 and
wingd/client +413. They retain the unified CLI, private Cloud bootstrap,
restart-safe teardown, local lifecycle, four activity holds, joined serving and
OS-authenticated transports. Retired k8s, warmpool and supervise packages total
-2,610. These figures include earlier deletions, not new work claimed here.

No further package cut is claimed. The overage remains a lead/Korey decision,
and this report does not mark W8 complete.

## Amendment dispositions and remaining integration

The per-row dispositions in `W8-RETRY-AND-SIZE.md` and
`W8-MERGE-RETRY-VALIDATION.md` remain. This session strengthens A1-38's sync
validation without reopening the completed retry, grammar or table cuts.

- A1-08/09/18/19/26/27/36/39/51: W8's unified claim loop, immutable execution
  inputs, embedded/remote compositions and legacy executor deletions remain.
  Native source/log/storage/process services and actual SDK hello still need
  production bindings.
- A1-10/11/12/43/45/46/48/50/52/55: selected-engine installation, source and
  environment projections, signed fallback, retained matrices and here claims
  remain. Native Windows packaging and placement QA remain open.
- A1-20/49: private credential startup and repository admission remain. W1/W2/
  W6/W9 still own enrollment/rollback, Actions and bootstrap-admin issuance.
- A1-38: W8 closed. W1/W2 must supply the existing transactional
  `LauncherSyncReader.ReadLauncherSync`. This checkout has its interface and
  fixtures, but no production reader.
- A1-40/41: elected runtime, store/queue/maintenance composition, activity holds,
  managed units and joined drain remain. W2's authorized local handlers and
  W9's executable service factory are still integration points.
- A1-42/53: deployed `KubernetesBoundary.Probe/Clear`, enforced egress, DaemonSet
  inventory, escape probes, aggregate 20 GiB accounting and complete DinD
  teardown require a real deployment. Native Linux/Darwin/Windows and repository
  placement acceptance remain open.

Contract/composition requests for other areas are unchanged and still block the
default executable. `cmd/sparkwing-runner` supplies an empty Runtime. W2/W3/W9
must supply authenticated private state/principals for `NativeHostStore`, rather
than treating a scheduling snapshot or decoded opaque bearer as authority.
W3 must supply plan/eval hosting and initialized-checkout consumption. The source
contract still needs to distinguish uploaded working-tree content from a
committed bundle seed. No foreign contract was amended here.

The W9 local factory still returns unavailable from `openLocalController`.
W8's `localcontroller.Config.Open` accepts the existing store, state, HTTP and
native service contracts and is tested with fixtures. Component results do not
establish those production bindings or deployed probes. No substitute policy,
wire schema, cluster assumption or success-returning stub was added.

Only the two W8 launcher files and this report changed. No shared implementation,
lead-owned handoff/amendment or W3 file was touched. No git writes ran.

Friction: the broad acceptance selection above was corrected to the focused W8
selection. Overwing's stale-binary notice did not prevent standards discovery or
review. Pipelines, candidate execution and native/deployment evidence remain
with the lead under the swarm brief. Worker scratch is deleted at session end.
The shared `/tmp/sw-gocache` remains.

STATUS: CONTINUE
