# W1 session 58: launcher merge and GitHub App administration

STATUS remains CONTINUE. Base HEAD is 95292b45e. No Git writes ran. Lead-owned
handoffs/amendments, W1b files, materialization, billing and blob/log persistence
are unchanged.

## Merge resolution

Resolved the only conflicted working-copy file, internal/store/launcher_snapshot.go.
W1 retains the store-owned award snapshot and controller adapter. W8's amended
reservation observation and cleanup response are implemented through that same
snapshot. No deleted integration file was restored. Removed launcher_contract.go
and moved its implemented types/method into launcher_snapshot.go.

The index still lists launcher_snapshot.go as unmerged because SWARM-BRIEF forbids
Git writes. The lead must stage the resolved files and deletion, then commit the
merge. The working copy contains no conflict markers and the full build passes.

Store.ReadLauncherAwards now takes a third argument, the observed reservation
LeaseRefs. This is a local C1 contract amendment, documented in c1-data.md and
migrated at every caller. It checks the persisted original bootstrap award against
the authenticated root and service subject, even after expiry, cancellation,
exchange or a later generation. Unknown/foreign fences reject the complete
snapshot. Live awards retain their fences and private inputs. Controller sync
returns exact reservation identities and never a bearer. The parallel Operator
wire-producing implementation was not restored.

W8 fixtures now use current enrollment registration IDs and repository authority,
reuse the bootstrap gate once, and fund Cloud claims through RecordCreditGrant.
They no longer depend on the old unmetered-Cloud defect. The existing wire fixture
now includes the mandatory remove_reservations field.

## Implemented and deleted

Tenant implements all six C2 GitHubAppWriter methods. Each transaction reuses the
team administration boundary, locks the team, and rechecks current ancestry,
membership, selected grant, actor and store time. Subscription/automation writes
lock a live tenant installation. Automation replay preserves consent generation.
Revoke then regrant creates a new generation. Extra repository slugs and positive
verified numeric IDs replace together. Missing IDs and failed inserts roll back.
The stored active installation owner constrains nonempty extra lists. Revocation
by clearing a list remains possible after unbinding or suspension.

Removed seven raw Tenant APIs: UnbindGitHubAppInstallation, PutGitHubAppTrigger,
DeleteGitHubAppTrigger, PutGitHubAppAutomation, DeleteGitHubAppAutomation,
SetGitHubAppExtraRepos and SetGitHubAppExtraRepoIDs. The trusted Operator uninstall
uses its existing transaction helper. No forwarding API or compatibility alias
was added. Existing validation and persistence SQL remain private transaction
helpers. The old split names/IDs state is removed.

## Volume

Budget-counted store production against HEAD: **205 added, 74 deleted, net +131
Go lines**. Combined W1/W1b against main: **19,526 added, 23,481 deleted,
net -3,955**, which is **295 over** the -4,250 target. Tests, testdata, acceptance
harness and paths containing contract are excluded as in the lead's budget script.

Part of this increase moves implemented launcher code out of an excluded contract
file. The rest supplies previously missing committing authorization and original
reservation proof. Both reuse existing transaction owners. No new table, setting,
queue or generic mutation framework was added. This is not a budget waiver or a
COMPLETE claim. Remaining raw intake and token paths still need larger cuts.

## Verification and ports

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and TMPDIR/GOTMPDIR under /tmp/sw-astra-W1, with -p 4.

- Full repository build passes. Scoped vet covers store, controller, client and
  tests/acceptance.
- Focused store GitHub, launcher, consent, extra-repository and check-reporting
  tests pass (9.228s). The generated launcher property covers plan/eval/node,
  delegation, exchange, expiry, cancellation, replacement and forged fields.
  A refused fence discards a snapshot containing an otherwise valid fence.
- The GitHub administration property generates authority loss across all six
  methods and checks unchanged rows. Tests pin actor/time, consent generations,
  missing IDs, foreign owners/installations, suspension, forged actor, late
  membership loss and insert rollback. Clearing after unbinding is covered.
- TestGitHubAppRemovalRoutesUseTenantTransactions drives the three actual HTTP
  deletion routes with SQLite authority and concrete Tenant writes. The existing
  team-write HTTP property and launcher snapshot/ambiguity routes pass (0.640s).
  No provider-discovery HTTP proof is claimed for the three configuration PUTs.
- TestStoreLauncherReservationW8 and FuzzStoreLauncherReservationFenceW8 pass
  through the real store/controller/launcher with fake Kubernetes clients.
  Broader launcher/bootstrap acceptance and client tests pass (2.223s, 0.025s).
  These do not enable native Kubernetes cells.
- The established focused W1 acceptance-function group, including request/plan
  seals, claims, generated dependencies, owner recovery, schedules and sync,
  passes (6.968s). Cloud-placement metering, ordered preference and exact rotation
  deadline properties pass explicitly alongside the new GitHub tests (5.139s).
- Race tests for the new GitHub mutations, launcher ownership property and
  controller launcher sync pass (store 83.663s, controller 6.256s).
- Ported TestReportGitHubCheckAdmitsOneHolderAtATime and
  TestReportGitHubCheckRecordsNothingPastTheHoldersDeadline to real sealed
  admission. The old raw-trigger fixture failed on removed columns. The consent
  generation regression now checks the current transaction fence, replacing
  TestGitHubAppAutomationAdmissionChecksConsentGeneration's legacy insertion.
- Commentcheck, whitespace, changelog and embedded-doc drift checks pass. The
  store API snapshot is regenerated. No whole-repository API gate is claimed.

The broader controller test selection could not finish because
TestDrainGitHubChecks_DoesNotInheritASpentBudget opens an httptest TCP listener,
which the sandbox refuses. The in-memory controller selection passes separately.
No whole-repository test run, pipeline, PostgreSQL, native Kubernetes, independent
review, landing or release is claimed. The lead owns those checks.

## Remaining work and contract requests

Priority Cloud metering, ordered placement, rotation grace, harness moves and
dead contract-table cleanup were already implemented and remain covered.
SyncLauncher reservation integration is now adapted to the amended W8 contract.
Materialization remains closed and untouched.

Trusted schedule/App admission remains blocked on C4's tagged committing origin
through IntakeFacts/SealedRequest and C2's verified-delivery reader usable inside
the store transaction. An arming account or verified delivery is not a credential.
Do not synthesize a credential to pass IntakeRequest's authority check. Once the
foreign contracts are declared, route both paths through admitRun, then remove
triggerIntake, triggerRows and the raw insertion path. No foreign contract was
silently extended here.

Remaining W1 work from session 57: RunControlWriter creation/approval masking,
trusted schedule run-now, full runner inventory/capacity and renewal reporting,
sealed GitHub completion facts instead of TriggerEnv, publisher metric binding,
usage-report identity, raw CreateRun/CreateTrigger, raw token minting and token
run-rate queries, and their legacy fixtures. The HTTP cleanup retry gap after
run deletion still needs W2's authorized absent-run selector contract. Local
end-to-end composition remains W9's lead-assigned integration. Billing/blob/log
amendments stay with W1b.

## Shared files, assumptions and friction

Shared production files touched by W1:
- internal/controller/engine_launcher_sync.go
- internal/controller/github_app_routes.go

Shared tests touched by W1:
- internal/controller/github_app_native_routes_test.go (new)
- tests/acceptance/bootstrap_store_w8_test.go
- tests/acceptance/runner_store_w8_test.go
- tests/acceptance/launcher_reservation_w8_test.go
- tests/acceptance/store_launcher_w8_test.go
- tests/acceptance/launcher_sync_contract_test.go

C1, the test-port inventory, migration source, changelog, embedded mirrors and
store API snapshot are updated. Integration branch edits to other areas remain
in the merge. W1 made no additional edits to those imported files.

Assumptions: the existing controller owns provider verification. The store consumes
its numeric repository IDs and rechecks persisted installation ownership. C2's
extra-repository interface carries no installation selector, so the store requires
an active installation for the stored repository owner. Undo means changing that
C2 evidence contract and its callers together, without restoring split writes.
No new human decision is requested.

Friction: W8 fixtures used obsolete registration IDs, omitted repository scope,
reissued the single-use bootstrap and depended on unmetered Cloud placement.
Those premises are corrected. Native sockets remain unavailable. Local Git config
has no user or core.worktree overrides. Scratch is removed at completion and the
shared build cache is retained.

STATUS: CONTINUE
