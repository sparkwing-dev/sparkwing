# W8 bootstrap deadline continuation

Bootstrap exchange and hosting now run under the immutable award's deadline.
The Job builder, source initializer and bootstrap host share one calculation
using `limits.JobDeadline`, capped by parent capability expiry. An earlier
caller deadline still wins. Expired awards fail before constructing transport.
Expiry during exchange prevents dispatch. Expiry during hosting cancels and
joins the host, and remains an error even when the callback returns nil.

Invariant: bootstrap exchange and dispatched execution cannot outlive the
owner, attempt-plus-slack, parent-capability or caller budget carried into them.
`FuzzBootstrapRespectsAwardDeadline` generates owner, attempt and parent bounds
for plan, eval and work. Its expired-owner and expired-parent seeds failed
before the fix. `TestBootstrapDeadlineCancelsExchangeAndHost` checks both phases
under synctest without sleeps. The existing exchange test now accepts the child
context that carries the deadline.

## Lead decisions and deletions

- Item 5 is retained. `LocalController.HTTP() LocalHTTP` is the handler boundary.
  W2 still supplies its production API/dashboard adapter. Runtime cancellation
  and joined drain remain W8-owned and their focused tests pass.
- Item 6 is retained. The registry and runtime accept required digest-pinned
  `--docker-image` and reject launcher `--deadline`. Existing parser/runtime and
  polling tests pass.
- Item 10 is implemented on the launcher side. Sync sends operational kind,
  unit ID, holder, live claim generation, actual Job namespace/name/UID and node
  UID. Eval lease generation stays separate from terminal source generation.
  Responses match the full fence, and deletion retains the observed Job UID.
  **A1-38 remains open end to end.**

The cluster worker, warm-pool dispatcher, run-host Kubernetes dispatcher and old
W8 Job builders remain deleted by earlier commits. The production scan finds
one Job builder in `internal/runners/launcher`. This session removes duplicated
deadline calculation. It introduces no replacement dispatcher or compatibility
path. No caller outside W8 needs migration.

Production Go against the starting HEAD adds 26 physical lines and removes 17.
**Net production lines deleted: -9, meaning nine lines added.** Tests and Markdown
are excluded. No additional legacy file was deleted in this continuation.

## Amendment and handoff coverage

The 26 amendment entries naming W8 and the W8 handoffs retain their implemented
boundaries and open integration obligations from
[the prior report](W8-AWARD-FENCE-CONTINUATION.md). This continuation changes
deadline enforcement for A1-38 and A1-48. It does not claim another acceptance
cell or archived test port. Pending-claim outcome and arbitrary-cancellation
regressions remain passing.

The merged W3 contract now includes `RunInput.GrantedRunner`. W8's run-input and
owned-store host paths already carry it, so the earlier missing-field request
is resolved. Actual hello across remote placements still needs the final host
bindings. W1's lifecycle billing join is present. W8 continues to consume the
awarded billing facts without calculating settlement.

## Contract changes needed and real integration left open

- W1/W2 must implement and bind `LauncherSyncReader.ReadLauncherSync`. Only its
  interface and controller delegation exist. W8 tests that interface with fakes.
- W2's `claimedUnit` clears `Execution` for plan/eval. Those awards need resolved
  resources under the existing contract before W8 can launch or exchange them.
- W3 needs a Cloud plan/eval lifecycle contract. `OwnerHost.Run` owns a complete
  trigger-hosted run. It cannot stand in for a controller-owned plan unit.
- W3 needs an initialized-checkout adoption contract. `source.Entry` keeps its
  checkout ownership private. W8 cannot turn the shared init checkout into an
  acquired entry by changing the sealed source's `LocalCheckout`.
- Native remote hosting needs W3's `NativeHostStore` adapter over the private
  controller wire. `RemoteOwnerStore` supplies reconciliation, expansion and a
  scheduling projection, but does not implement that interface. Safe public run
  detail and the scheduling projection do not supply its full persisted inputs.
- W9's source client still returns `store.SourceCredential`, while W2 emits
  `SourceCredentialResponse`. W8 needs the shared transport contract before
  binding `source.Access` to it.
- Local startup still needs production `State`, HTTP, claimant/enrollment and
  native-service bindings over `localcontroller.Binding`'s sole store handle.
  W8's queue, maintenance, admission and lifecycle compositions are implemented
  against their interfaces. Existing SQLite and fake-backed components pass.
- W8 still owes default executable composition and deployed `Probe`/`Clear`
  bindings. Native Kubernetes, Docker, Windows/Darwin, transport and real-repo QA
  remain open with T1/lead. No deployment evidence or native cell is claimed.

No foreign contract was changed. The lead-owned handoffs and amendment files
were read and left unchanged. No shared production or test file was edited.

## Mechanisms and verification

`cloudDeadline` removes the duplicate calculation because all three phases use
the same award. The bootstrap host adds the corresponding child context. Undo
restores the duplicate calculations and removes that child context. No exported
API, setting, timeout value, environment variable or trust root was added.

Checks use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`, `-p 4`, and
GOCACHE/GOTMPDIR/TMPDIR under `/tmp/sw-astra-W8`.

- Whole-repository build and focused vet pass.
- Runners, engine, localcontroller, internal acceptance, clisurface and chart
  suites pass. Cluster passes with its socket-binding metrics test excluded.
- Existing W8/launcher acceptance components pass. Focused local runtime,
  lifecycle, HTTP, dashboard and managed-unit tests pass.
- The new deadline property passes 2,000 fuzz executions with four workers.
- Commentcheck is clean across 2,125 Go files. `git diff --check` passes.

No Sparkwing executable, pipeline, release, deployment or Git write ran.
Overwing reports its installed binary ten commits behind source. Native sockets
remain unavailable under the swarm's sandbox contract.
All check processes joined. `/tmp/sw-astra-W8` was removed, and the local Git
configuration has no identity or core.worktree override.

STATUS: CONTINUE
