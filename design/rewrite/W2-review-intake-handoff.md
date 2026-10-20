# W2 review intake and native integration handoff

Findings 3 and 7 remain open because their fixes require W1-owned contracts and
state. Native reproductions also confirm W1 work in findings 1, 2 and 6.
No production Go files changed in this subtask. Production line delta: 0.
No legacy code was deleted because its callers cannot migrate to an authorized
replacement under the current contract.

`W2-review-reproductions.patch` preserves the temporary regression tests. The
tests ran against the real current-schema SQLite store and controller. The
scratch `_test.go` file was removed after collecting the failures, so these open
cross-owner findings do not leave the controller package permanently failing.

## Finding 3: DESIGN: trusted origin admission

Invariant: one intake owner validates, seals and persists every origin, with
authority and repository consent checked in the admission transaction.

Both reviewer scenarios reproduce through the actual controller intake methods:

```text
TestReviewDueScheduleCreatesSealedRun:
due schedule intake: persist the trigger and its run: SQL logic error:
table triggers has no column named pipeline (1)

TestReviewVerifiedWebhookCreatesSealedRun:
verified webhook intake: persist trigger: SQL logic error:
table triggers has no column named pipeline (1)
```

`Tenant.AdmitRun` requires a tagged live capability/session authority holding
`runs.write`. `CronSchedule.ArmedBy` contains only a legacy principal string.
`GitHubAppTrigger.CreatedBy` and `GitHubAppAutomation.EnabledBy` likewise cannot
identify a live authority chain. Fabricating capability authority from these
strings would bypass C2.

`IntakeRequest.VerifiedDeliveryID` is present, but the store needs an
`IntakePolicy` to resolve it into verified facts. No production policy exists.
The final `github_app_deliveries` table stores only digest, delivery ID, receipt
time and event. It cannot reconstruct the required repository/ref/SHA,
default-branch facts, actor or pull request. `trustedIntakeRequest` clears App
facts before consulting this policy, so passing them in `Request.Git.App` is
not a usable adapter.

W1 must define a persisted, transactionally checked origin-consent contract for
controller schedules and App deliveries. Schedule admission must read the
committed schedule, current team/repository consent, effective arguments and due
instant. App admission must read the verified signed-body facts and current
installation/subscription or automation generation. Both must enter the existing
`admitRun` transaction and retain its idempotency, seal and owner-unit writes.
W1 and C2 must define the durable authority for these unattended origins, rather
than treating an account string or an expiring browser session as that decision.
W6 must provide production option/ceiling and source resolution through the
existing `IntakePolicy` seam. Once declared, W2 can migrate both controller
callers and remove `triggerIntake`, `triggerRows`, `triggerRunInvocation` and
their legacy insertion calls in one cut.

Status: OPEN, cross-owner DESIGN handoff. No optional adapter, fabricated
credential or legacy fallback was added.

## Finding 7: DESIGN: local-first placement in the claim owner

Invariant: controller placement settings affect all claims through one
store-clock decision using authenticated eligible contenders.

The temporary test configures `WithLocalFirstPlacement` with a one-hour hold,
one-hour liveness and `location=local` default preference. It registers a live
eligible local machine with one free core, then makes a real HTTP claim from
an eligible fallback runner. The configured hold is ignored:

```text
TestReviewLocalFirstPlacementHoldsForLivePreferredRunner:
fallback claimed job while a live eligible preferred runner had capacity
```

`UnitDemand` has no placement settings. `unit_matching.go` reads preferences
only from sealed run/job options. `match.Demand` has no hold instant or live
contenders. `ClaimProfileResolver` supplies enrolled profiles and available
resources, and registered capabilities retain last-seen time, but the claim
transaction does not combine them into a hold decision.

W1 should own one configured policy with default preferences, hold and liveness.
Its named, queued and self-claim transaction paths should share the store-clock
hold decision, using the existing authenticated profile resolver and current
same-team contender observations. An expired/revoked, ineligible, full or stale
contender must never hold work. The hold instant must survive mismatched polls.
W2 then wires the existing controller values into that owner. The legacy
`WithClaimPlacement` context cannot supply this integration because only
`ClaimNextReadyNode` reads it and its team lookup queries the removed `tokens`
table.

Status: OPEN, W1 contract handoff. No customer-facing placement flag was cut.

## Finding 2: DESIGN: production dependency composition

Invariant: an enabled operation has a concrete authority-bearing implementation
that reaches its committing owner.

`TestReviewProductionDependenciesHaveNativeTransactions` tests the concrete
fallbacks selected by production composition. These assertions fail:

| Concrete type | Missing controller contract |
| --- | --- |
| `*store.Store` | `BootstrapAdminIssuer`, `AccountWriter`, `GitHubConnectWriter` |
| `*store.Operator` | `LauncherSyncReader` |
| `*store.Tenant` | `TeamWriter`, `ScheduleWriter`, `RunControlWriter`, `MetricWriter`, `ConcurrencyStore`, `SizingStore`, `BillingWriter`, `OIDCReader`, `GitHubAppWriter` |

`RunTransactions`, `DataTransactions` and `PaymentSettler` conformance pass.
Those contracts therefore must not be reported as missing interfaces.

Two behavior reproductions expose gaps conformance cannot see:

```text
TestReviewNativeLocalPeerAuthentication:
credential class has no persisted authentication authority

TestReviewProductionDataObjectsSupplyLiveAuthority:
native data signing authority: route authorization policy is incomplete
```

The local test calls real `Store.ReadAuthentication` with `CredentialPeer`.
The data test supplies a real filesystem object backend and a live awarded node
claim through `WithDataObjects`, then calls its actual `BindData` method.
`WithDataObjects` leaves `ResolveAuthority` and `Access` unset.

Further code evidence: `Store.settlePaymentFacts` accepts only the three
checkout outcomes and refuses every other verified event kind.
`checkRelationTx(BindSlotOfBoundNode)` always returns unavailable because it
has no selector relation implementation. These two cases were inspected rather
than driven with a fabricated verified proof or an invalid concurrency fixture.

W1 must implement the declared transactional operations and local peer/store
relations. W1b owns non-checkout settlement. `Tenant.CheckDataAccess` already
implements the access checker and needs tenant dispatch wiring. The native
data-authority derivation exists only in private `resolveBlobAuthorityTx`, so
W1 must expose that result before W2 can wire signing without copying it. Each owner
should add compile-time conformance plus a route-through-commit test, then W2
can make enabled production dependencies required.

Status: OPEN, cross-owner DESIGN handoff.

## Finding 1: DESIGN: member-root origin gate

Invariant: a session derived from a capability cannot erase that capability's
revocation ancestry by provisioning a new root.

The native reproduction calls `CreateAccountSession`, provisions a limited
member token, calls `CreateCapabilitySession` with that token, then directly
calls `ProvisionCapability` using the derived session as its authorizer:

```text
TestReviewDerivedSessionCannotProvisionDetachedRoot:
derived session provisioned detached root ... with parent <nil>
```

W1 must gate `checkMemberProvision` on the authenticated provider-session origin
before allowing a parentless member root. W2 route validation alone cannot close
this direct store entrypoint.

Status: OPEN in W1. W2 session work is reported separately by the lead.

## Finding 6: DESIGN: native declared pipeline references

Invariant: a live claim can read a declared pipeline reference, and undeclared
pipelines remain forbidden through the same native checker.

The test awards a real node claim, stores its declared
`PipelineReference{Pipeline: "producer", Job: "output"}`, and calls
`Store.CheckBinding(BindReadSet, Target{Team: "a", Pipeline: "producer"})`:

```text
TestReviewDeclaredPipelineReferenceUsesNativeChecker:
declared pipeline reference: resource is outside the authorized binding
```

W1 must implement the pipeline-reference branch of the read-set relation from
the caller's live bound job and its sealed `JobSpec.References`. Filling in the
caller's run/job selectors alone is insufficient because the existing checker
only inspects same-run dependency edges and never checks `References`.

Status: OPEN, W1 relation handoff. The lead owns repairs to the reporting tests
that substituted a successful binding fake and replaced the handler.

## Verification and friction

The full temporary reproduction set ran with:

```sh
GOWORK=off GOFLAGS=-buildvcs=false \
GOCACHE=/tmp/sw-astra-W2/root/cache \
GOTMPDIR=/tmp/sw-astra-W2/intake/tmp \
TMPDIR=/tmp/sw-astra-W2/intake/tmp \
go test -p 4 ./internal/controller -run '^TestReview' -count=1
```

All listed failures occurred in the real package run after the concurrent
router edits compiled. Earlier runs hit transient registry compile errors and
a duplicate `/metrics` registration. The route worker corrected those before
the recorded run. No sparkwing binary or git write command ran.

After removing the scratch tests, `go build -p 4 ./...` passed and
`go run ./internal/commentcheck -base main .` reported clean across 2109 files.
`TestEngineClaimHTTPDeliversPrivateAwardOnce` also passed. The worker's
`/tmp/sw-astra-W2/intake` subtree was deleted. The lead owns the shared task cache.

The report and reproduction patch are the only retained files from this
subtask. No new acceptance cell or ported legacy test is claimed complete.
