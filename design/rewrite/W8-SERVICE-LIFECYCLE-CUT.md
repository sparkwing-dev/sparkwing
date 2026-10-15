# W8 service lifecycle cut

Runner service installation and removal now share their Linux/macOS lifecycle
flow. Install, refresh and uninstall use one loaded-registration check. Install
and refresh use one private registration writer. Preflight shares absolute-path
validation and service-manager error handling. Refresh still leaves stopped
services stopped. Foreign files, another config and another loaded source remain
protected by the existing checks. The four platform-specific install/uninstall
functions and redundant command/config forwarding helpers are deleted.

Removed `ManagedUnitPolicy` and `ManagedUnitPolicies`. Their login, restart and
socket metadata had no consumer. Doctor now checks each supported kind's exact
arguments directly. Native renderers and their existing tests retain the login
and restart behavior.

Removed `MayExitIdle`, which had only test callers and duplicated the daemon's
idle decision. Deleted its duplicate acceptance check and unit test. The existing
daemon test still exercises all four activity holds, stay, unchanged observations
and invalid counts. It now checks the actual shutdown operation immediately
before and at the idle boundary. No acceptance cell was added or unskipped.

## Priority and amendment status

- Lead item 5 remains implemented through `LocalController.HTTP() LocalHTTP`.
  W2 must supply the production handlers and local state binding.
- Lead item 6 remains implemented. Launcher parsing accepts digest-pinned
  `--docker-image` and refuses `--deadline`.
- Lead item 10 remains implemented on the launcher side. Sync carries the award
  holder/generation and actual Job/node identities. Eval source and live lease
  generations remain separate. **A1-38 remains open end to end.**
- The requested cluster worker, warm-pool dispatcher, run-host Kubernetes
  dispatcher and old W8 pod builders remain deleted. No legacy caller migration
  was required in this session.
- The other assigned amendments retain their implementation and open integration
  obligations from [the prior report](W8-CONTRACT-RETIREMENT.md). The four dead
  catalogs assigned at 09:40 remain deleted.

## Lines and contracts

Against starting HEAD `73d183cdb`, production Go adds 119 lines and removes 268.
**Net production lines deleted this session: 149.** Tests and Markdown are excluded.
The lead's W8 metric against `main` is now **+4,688**, which is **6,988 over** the
-2,300 budget. W8 is incomplete.

The overage still needs lead review. The retained signed engine resolver and
native SDK upgrade implementation add 2,233 lines. Managed runner services add
929, local controller/admission lifecycle paths add 2,649, and the sole Cloud
launcher adds 1,113. Other cluster/runner paths remove 2,236. D91 and D102-103
require these runtime responsibilities, and removing them would cut ratified
features. This session removes duplicate implementation without adding a new
abstraction. The remaining overage justification is unchanged.

The retired lifecycle helper and policy catalog are local W8 contract changes.
Their export rows were removed from `contracts/c4-surface-config.md`. Their only
callers were migrated or deleted. No foreign production API was changed.

## Real integration points left open

- W1/W2: bind `LauncherSyncReader.ReadLauncherSync` to transactional current
  authority. W2's plan/eval awards still clear `Execution`, which the existing
  launcher contract requires for resolved resources.
- W2: provide state, authorized API/dashboard handlers and claimant enrollment
  over `localcontroller.Binding`'s sole store handle. W8's queue, elected store
  owner, reconciliation, maintenance and joined runtime use those interfaces.
- W3: supply the remote `NativeHostStore`, Cloud plan/eval lifecycle and initialized
  checkout adoption contracts. W9/W2 must align the source-credential response.
- W8: finish default executable composition once those bindings exist, plus
  deployed `KubernetesBoundary.Probe` and `Clear` bindings. Native Kubernetes,
  Docker, Windows/macOS transport and real-repository QA remain with W8/T1/lead.

No new foreign contract change or product/security decision was introduced.
No historical test port or native placement cell is newly closed. Fakes continue
to supply the unfinished foreign boundaries in the existing component tests.

## Verification and handoff

Whole-repository build, focused vet, service/unitfile suites, local runtime and
managed-unit suites, and the existing W8/launcher acceptance selection pass.
Commentcheck passes against main across 2,123 Go files. Diff whitespace passes.
Checks used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`, at most four
package workers, `/tmp/sw-gocache` and temp files under `/tmp/sw-astra-W8`.

An overbroad wingd selector also ran four socket-dependent tests. They failed
because Unix socket bind/dial operations are denied in this sandbox. A second
selector still included `TestServiceOrderPromotesEarlierParticipantAcrossOwners`
and hit the same restriction. The final `^Test(Local|Managed)` selection passes.
These failures do not establish native transport behavior. No new tests were
added solely to restate working code.

Shared files touched: `contracts/c4-surface-config.md` removes three W8 export
rows, and `tests/acceptance/local_controller_w8_test.go` drops the helper-only idle
subtest. The private config-parser tests now call the shared parser directly.
All production edits are within W8. Source service documentation is updated.
Lead-owned handoffs and amendments are unchanged.

Friction: Overwing reports its installed binary behind source. Production
composition cannot be completed from the foreign interfaces available on this
branch. No Sparkwing binary, pipeline, deployment, release or Git write ran.
The Git configuration has no identity or core.worktree override. The lead retains
independent review and commits. Scratch is removed and the shared cache is kept.

STATUS: CONTINUE
