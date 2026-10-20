# W1 session 51: ordered placement and merge resolution

W1 remains incomplete. HEAD is unchanged at `37aa5e0dd`. Every merge conflict
has resolved working-file contents or a deletion, with no conflict markers.
The index remains unmerged because the swarm prohibits Git writes. The lead
must stage the resolutions and deletions and finish the merge.

## Implemented and retained

`ClaimPlacement.Preferences` is an ordered list of matcher preferences with
per-step holds. Holds accumulate from persisted readiness on the store clock.
An earlier runner can hold a later claimant only while its hold is unexpired
and its persisted poll resolves to live, eligible authority with capacity.
Zero holds permit immediate fallback. Earlier preferences remain eligible when
their holds expire. Explicit job preferences use a two-step list with the first
configured hold. Existing controller flags configure that same two-step case.
Named and queued claims share this implementation with reconciliation.

The generated property covers preference permutations, zero and positive holds,
exact boundaries, missing/live runners and forged name/class labels through
real claim transactions. Waiting preserves the whole node and award count.
The existing property still covers expired/revoked/foreign/grantless runners,
repository and hard-selector exclusions, capacity, named preferences and the
controller's persisted-poll integration.

A new regression reproduced stale polls being refreshed by unrelated credential
activity. Invariant: only an accepted poll refreshes its observation. The poll's
store-clock `observed_at` now lives in the existing observation JSON. Placement
checks it separately from `last_used_at`. No table, configuration or service is
added. The generated stale-activity cases failed before the fix and now pass.

Cloud metering was already fixed on this branch at `meteredPlacement`, which
selects metering from Cloud placement or the enrolled runner policy. The real
claim property covers credential origin, root flag, delegation, named/queued
claims and funded/unfunded payers. It passes after integration. No billing,
settlement, blob, log or materialization implementation was edited.

Merge resolutions preserve rotation behavior while importing the direct-session
member-provisioning predicate, binding deadlines and separate job/run
cancellation reasons. Both rotation-grace properties survive. The canonical
bootstrap gate and launcher snapshot implementations replace the integration
branch's overlapping implementations. The controller's concrete snapshot adapter
satisfies LauncherSyncReader. Sync retains safe cleanup decisions for unknown
award IDs and never returns bearer material. Launcher fixtures now fund Cloud
claims because unmetered launcher roots still require credits.

## Deleted and volume

Removed unused Store/Tenant raw debug-pause creation/release, the tenant ancestor
reader, child-trigger lookup, mutable-token NodeSettlement/NodeMetering, and an
unused private claim-identity helper. Removed the dead placement fixture file,
the ancestor reader's four obsolete tests, and the old tenant pause fixture.
Current authorized-resume and sealed-child properties retain their relevant
boundaries. The port inventory records the replacements.

Those legacy store removals delete **184 production lines**. Consolidating the
duplicate bootstrap implementation deletes another **118 lines**. Placement and
poll changes add a net **-2 lines**, including removal of unused caller-supplied
RunnerPresence/Live state. Session net is **-304 production Go lines**, excluding
imported integration changes. Controller caller changes have zero net lines.

Combined W1+W1b versus main is **18,721 added, 21,304 removed, net -2,583
production Go lines** under the lead's exclusions. That is **1,667 lines over**
the -4,250 target. No budget exception or COMPLETE claim is made.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, the shared
GOCACHE=/tmp/sw-gocache, W1 scratch for temporary files and package parallelism
at most four.

- Whole-repository `go build -p 4 ./...` passes after the production changes.
- Store/controller scoped vet passes.
- All 21 W1 package acceptance functions pass. No native matrix cell is added.
- Focused store/controller checks pass for placement, inherited Cloud metering,
  bootstrap, origin, rotation, launcher snapshots, runner polls and debug resume.
- The broader race selection passes: store 207.718s and controller 6.631s.
  The final placement-only race run passes after the last simplification
  (99.550s).
- All ten merged native log-egress tests pass (0.586s).
- Embedded docs, changelog and migration checks pass. API snapshot drift is empty.
- Commentcheck passes across 2,274 Go files. Whitespace checks pass.

No whole-repository test suite, pipeline, socket runtime, PostgreSQL/native
platform cell, release or independent review is claimed.

## Remaining contracts and integration

**Trusted intake remains open.** C1 IntakeRequest accepts a capability/session
AuthorityRef. Schedules persist an account-valued ArmedBy, not a live credential
origin. VerifiedDeliveryID already selects policy-supplied App facts, but there
is no production IntakePolicy/verified-delivery fact reader in the controller.
Normal intake resolves authority before that policy is consulted.

The required cross-area decision is a committing trusted-origin representation
for schedules and HMAC-verified App deliveries, carried through the C4 seal,
plus the production fact-reader contract. This can be a tagged noncredential
origin or a declared C2 authority kind. An account string cannot be fabricated
into a session or independent capability. W1 has not changed foreign contracts
or minted synthetic authority. After that contract exists, W1 must route both
origins through admitRun and remove triggerIntake, triggerRows and their raw
insertion path. These remain in production and prevent COMPLETE.

Full runner inventory still needs enrolled concurrency and accepted renewal
observations with immutable placement identity. Current polls describe observed
capacity and labels. They do not claim to supply that missing runtime contract.
W8/W9 own the queue-depth transport. The remaining console/run-control writer
composition is W2's end-to-end work under the 14:40 handoff.

Raw run/node constructors and token/claim fixture APIs still have callers,
including W1b-owned credit/billing tests. Those files were not edited. Their
migration remains necessary before the final raw constructors and old fence
helpers can be removed. W1b continues to own metering settlement, provider
composition, blob/log persistence and the closed materialization path.

The prior amendment implementation and harness migration remain in place. All
internal/acceptance store_* files and the six dead W1 contract tables stay
removed. Lead-owned amendments and handoffs are unchanged.

## Shared files and friction

Outside store, production edits are limited to
internal/controller/engine_claim_routes.go (ordered policy input),
internal/controller/engine_launcher_sync.go (conformance to the existing concrete
adapter), and internal/controller/missing_operation_routes.go (the canonical
store.ValidRunnerName caller). Controller merge/test changes are in
bootstrap_token_test.go, bootstrap_store_test.go, egress_test.go,
engine_launcher_sync_test.go, engine_launcher_store_test.go and
release_pause_test.go. The integration-removed store/claim_run_test.go stays
deleted. The port inventory keeps both branches' additions. C1, CHANGELOG,
the unreleased migration, their embedded mirrors and the store API snapshot
are updated.

Bootstrap startup tests retain W1's existing refusal on a different or revoked
seed. The integration's permissive no-op expectations are adapted accordingly.
A machine enrollment fixture now supplies the canonical registration selector
and repository scope instead of weakening the production gate.

Git writes and commits remain lead work. Initial repository orientation invoked
the installed `sparkwing info --for-agent` before the brief's binary prohibition
was loaded. No pipeline or candidate runtime was run. Scratch was removed after
the final checks. The shared build cache is retained.

STATUS: CONTINUE
