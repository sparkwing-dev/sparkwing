# W8 probe cut continuation

Continues `f6f0a8548` and `W8-HTTP-CLEANUP-CONTINUATION.md`. The tree started
clean. The local-controller priority remains incomplete because the foreign
contracts below still lack the required operations.

Removed the unused admission-client `Cancel` entrypoint and the separate
retrying `HealthProbe` implementation. Production has no caller of either.
Idle and journal fixtures use the retained `Probe` API, and the handshake-only
probe test asserts the returned version. The old health-only protocol-refusal
test retires with that API. `Probe` reports the observed protocol for its caller.
The wire's probe classification, cancellation operation and idle behavior remain.

Moved the journal incarnation read helper into its tests because only those
tests use it. Local HTTP peer snapshots now use the existing identity-copy
helper. The existing generated identity tests retain the snapshot boundary.
No interface, policy, setting, timeout or runtime owner was added.

## First-priority evidence and contracts needed

The unchanged `rewrite_integration` production probe gives these results:

| Probe | Result |
| --- | --- |
| Local controller | Passes the production constructor and closes its store. |
| Local run | Reaches private socket dialing, which the sandbox refuses. |
| Local schedules | Refuses missing checkout, environment and pin registration. |

The constructor result does not establish sustained worker readiness. These
contracts remain open in this checkout, with the same owners as the previous
report. No foreign contract was changed.

- W2/W1 must implement persisted `CredentialPeer` authentication.
  `authenticationAuthorityTx` still accepts bearer and session only.
  W8's local claimant already supplies the captured OS subject through C2.
- W2/W9 must declare local schedule registration, pin/unpin and tick transport.
  `ScheduleRepoRequest` still carries a clone URL rather than a local checkout,
  environment snapshot and proof choice. `ScheduleManager` has no pin or tick
  operation. W1 must define the durable schedule-to-snapshot association.
  W8's factory supplies the captured lifecycle and native timer operations.
- W3 must supply the HTTP-backed `NativeHostStore` adapter for foreground runs.
  `RemoteOwnerStore` supplies reconciliation, expansion and schedule projection
  only. W3's concrete owner-policy binder is also absent.
- Native process composition still needs adopted node authority, scoped W7
  byte/record/output bindings and the W3/W1 ephemeral process relation. The
  current store relation checker expects persisted Controller authority.
  W8 must consume these bindings before claiming a complete native run.
- W2/W1 still owe production launcher sync persistence. W8's immutable award,
  generation and Job-instance correlation remains implemented.
- Native Cloud isolation, enforced egress, aggregate scratch, DinD teardown,
  Windows/Darwin execution and release packaging remain deployment evidence.

The other per-row amendment dispositions remain in `W8-RETRY-AND-SIZE.md`,
`W8-LOCAL-CONTROLLER-CONTINUATION.md` and `W8-HTTP-CLEANUP-CONTINUATION.md`.
A1-38 is closed for W8. The shared retry-origin validation and its hosting,
claim and exchange regressions still pass. The four dead contract tables remain
absent. No native acceptance cell or archived T1 test is newly closed.

## Verification

All commands used GOWORK=off, GOFLAGS=-buildvcs=false, the shared build cache
and scratch under `/tmp/sw-astra-W8`. Tests used at most `-p 4`.

- Whole-repository build passed before and after the production edits.
- Full runners, launcher, service, unitfile, engine, sdkinstall, cluster,
  localcontroller and journal suites passed.
- Focused wingd/client local lifecycle and composition tests passed, including
  the same selection under the race detector.
- `tests/acceptance -run 'W8|LauncherCloud'` passed in 3.149 seconds.
- Vet passed for wingd, runners, engine, cluster, CLI and acceptance packages.
- Commentcheck and diff whitespace checks passed.

A broader wingd/client probe selection fails because the sandbox rejects Unix
socket creation and dialing. It includes the migrated idle, journal and
handshake fixtures. Those native tests remain for the lead. The focused
lifecycle selection exercises in-memory transports and does not replace them.

## Production lines and budget

This session removes 92 production Go lines and adds 1, for **W8 net -91**.
That includes the 13-line journal helper moved into tests. Production outside
W8 changes by **0**. Against `main`, W8-owned paths total **+3,001**, which is
**5,301 above the -2,300 budget**. Counts use `--no-renames` and exclude tests,
testdata, acceptance and contract declarations.

The remaining new-package additions need the lead's budget decision:

| Package | Net versus main | Required behavior |
| --- | ---: | --- |
| engine | +2,088 | Exact pins, verified releases/cache, signed version policy, fallback, historical matrices and staged upgrades under decisions 101-103. Those additions were outside the frozen size estimate. |
| engine/sdkinstall | +169 | Selected-engine SDK installation shares binary caching and npm. Folding it into engine creates an import cycle. |
| runners | +1,357 | One claim loop adopts committed awards during interruption, joins hosts and captures authenticated execution inputs. Source, leases and processes stay with the shared hosts. |
| runners/runnerflags | +62 | Entrypoints and service inspection share one declared grammar. |
| runners/service | +486 | Native installation, ownership checks, refresh and removal preserve argv and stopped registrations. This replaces agentservice outside W8 accounting. |
| runners/service/unitfile | +344 | Runners, controllers and timers share native argv parsing, rendering and registration publication. |
| wingd/localcontroller | +750 | The elected store supplies queue, recovery, activity, secrets and native service composition. Folding it into wingd creates a dependency cycle. |

The existing-package and retired-package deltas account for the rest. No further
runtime mechanism is proposed to meet the budget. The overage remains for lead
and Korey review, so W8 is not complete.

All edited implementation and test files are W8-owned. No `cmd/sparkwing`
composition file, foreign production caller, `internal/orchestrator` file or
lead-owned contract ledger changed in this continuation. Removed APIs had no
foreign production caller. The source lifecycle document is updated.

Friction remains the socket sandbox and the missing foreign contracts above.
Overwing's stale-binary notice did not prevent standards discovery. No git write,
Sparkwing binary, pipeline, deployment or release ran. Worker scratch is removed
after the final check processes join. `/tmp/sw-gocache` is retained.

STATUS: CONTINUE
