# W2 concurrency and typed step continuation

This transport handoff is superseded by the concurrency admission and queue-view
contracts in [c2-credentials.md](contracts/c2-credentials.md). Public single-group
mutations and their holder selectors are deleted. The retained group view and
priority control use the shared queue client. Execution uses unit admission.


<!-- design:begin -->

## Implemented

Resolved the contents of all nine merge-conflicted files before implementation.
The generated OpenAPI file follows integration and is regenerated. Documentation
keeps integration's contract structure and W2's current login budgets. No conflict
marker remains. amendments-1.md and handoffs.md are unchanged from MERGE_HEAD.
The merge index still needs the lead to stage these resolutions and finish the
merge because the swarm brief forbids Git writes.

The eight legacy concurrency registrations and their tenant catalog entries are
removed. The old runner, here and Actions slot-route permissions are removed.
No production Go source references /api/v1/concurrency. Final group operations
use PolicyRouter's declared credential, grant, binding and liveness alternatives.
Acquire and resolve are sensitive. Cancellation preserves reporting operations.
Force release requires runs.control. Malformed bodies, duplicate query selectors
and overflowing durations fail without exposing private diagnostics.

C2 declares ConcurrencyStore and WithConcurrencyStore, plus Target.the group selector. The complete methods and committing requirements are recorded in
c2-credentials.md. Missing authority-bearing adapters refuse operations. There
is no fallback to the old unfenced store methods or token-table checks.
The shared QueueGroup replaces duplicate client wire types, uses job names and
one-based positions, and preserves numeric duration values. The orchestrator
converts positions back to its store model. Domain errors retain their typed
API error and store sentinel. A rate-limit envelope cannot become an admission
result. Hosted transport retries use final paths and do not replay an uncertain
acquire.

The merged Controller.RecordStep handoff is now connected to W2's real handler.
It consumes shared StepReportRequest, validates state against the selected path,
compares captured source_attempt to the authorized/current job attempt and
passes derived AttemptIdentity and actor to C1. A mismatch returns lock_held
before writing. Optional timestamps retain nanoseconds. Masked error text uses
the existing ErrorBytes bound. HTTP 200 with recorded:true follows persistence.
The old private start/finish bodies and decoder are deleted.

Two merged acceptance fixtures are adapted to amended wire shapes. Failure
reads select generation, and job transition results carry structured failure.
These are fixture corrections. Product handlers already use those shapes.

## Deleted

- concurrency.go shrinks from 595 to 356 lines, a net removal of 239 lines.
  Legacy slot authorization/decoding and claim-specific declaration/lease
  handling leave this file. The new routes and transaction boundary add 163 lines.
- runner_claim.go loses 34 lines of slot routing and holder/body inference.
- claim_route.go loses the unused six-line bound-reporting constructor.
- claim_run.go loses the seven-line own-claim slot binding helper.
- Eight legacy registrations, eight catalog rows and both old scope-fence
  entries are removed. The hosted retry table removes the legacy acquire rule.
- Duplicate client ConcurrencyState and StateWaiter types and their duration
  parser are removed. Group reads use the shared wire contract and validator.
- engine_reporting.go removes 42 old lines and adds 16, including deletion of
  both private step bodies and their decoder. Its net reduction is 26 lines.

The rest of auth.go, claim_route.go and the legacy execution routes remain.
This continuation does not claim priority 1 complete.

## Invariants and acceptance evidence

A concurrency request can affect only the declared group and work identity
permitted by its current capability. A selected holder or leader cannot grant
reachability to another job or team. Binding and transaction adapters own the
persistent declaration, holder and read-edge facts.

TestConcurrencyHTTPTransactions exercises eight real registrations with job,
trigger and plan authority and fake transaction adapters. BindingProperty varies
encoded group, body/query run, job and holder selectors. Refused requests never
enter the writer. Refusals covers credential liveness, cancellation classes,
stale generation, transaction errors, tenant selection, safe errors, malformed
inputs and unsupported methods. ClientUsesGroupRoutes drives the actual Go
client through an in-process server transport. RetiredRoutes reaches no adapter.

TestObserveSlotMapsMissingHolder and TestHeartbeatSlotMapsLostOwnership move to
TestConcurrencyClientDomainErrors. The tests use canonical errors and in-process
transport. Non-policy errors do not become ownership failures. The admission
error test distinguishes a canonical rate limit from a skipped/failed slot.
The existing hosted retry policy test now checks final and retired group paths.

A step report stays attached to its captured attempt. TypedStepHTTP connects
W9's actual typed client to W2's actual handler with fake persistence.
TypedStepAttemptProperty varies captured/current ordinals and nanosecond times
for job and owner reports. A delayed report never reaches RecordStep for a newer
attempt. Existing reporting authority, cancellation, body-race and malformed
request cases use the shared wire contract.

No native acceptance matrix cell is newly completed. The older concurrency
claim and foreign-leader tests remain native integration obligations. Fakes
cannot establish sealed declaration/memo checks, inheritance refusal, lease
continuity/ceilings or committing races.

## Contract changes needed and real integrations

- W1 must implement ConcurrencyStore on Tenant and consume the group selector
  in BindSlotOfBoundNode. authority_binding.go still returns unavailable for
  that binding. Transactions must recheck live ancestry, tenant, authority,
  generation, declared group/memo policy, capacity/cost, inherited holder and
  output/leader edges. Release and force release include promotion atomically.
  No W1 implementation or signature was edited.
- The typed step route uses C1's existing RecordStep. Native host/HTTP/store
  composition remains with W3/W1/W9. The retained StateStoreOld runtime still
  calls Client.StartNodeStep, FinishNodeStep and SkipNodeStep. Those signatures
  lack a captured ordinal. They remain an unfinished caller cut. There is no
  old-body fallback on the final HTTP routes and no lookup that replaces an
  observed ordinal with a later attempt.
- W1 cancellation intent is persisted and reaches renewal. Its authentication
  projection still selects only cancellation timestamps in
  authentication_binding.go. BindingState.CancelReason already exists in C2.
  W1 must populate the first committed intent, preferring job intent over run
  intent. W2 does not add a second read that would split the auth snapshot.
- The previously recorded C1 peer authority, roleless/direct-console session,
  committing console writes, OIDC facts, root origin/enrollment, rotation,
  secret/source, data/read-set and billing integration gaps remain open.
  Existing W2 adapters continue against their declared interfaces.
- The lead's seeded bootstrap decision requires a C1 Store.Issue contract that
  accepts a supplied bootstrap presentation only inside the gated transaction.
  ProvisionSpec has no seeded input. The old startup token-table path remains
  an unfinished cut. No alternate issuer or raw-token persistence was added.
- Verified non-checkout payment events still need the joint C1/C2 contract for
  card setup, off-session outcomes, refunds, warnings and disputes. No old
  caller-selected team/credit callback is restored.
- W8's ratified LocalController.HTTP composition needs W2's final shared
  handlers and verified local identity adapter. The native local runtime and
  remaining legacy middleware cut remain open. Prior console/peer proof
  component results do not establish this production composition.

## Amendment and handoff disposition

| Assignments | Disposition |
| --- | --- |
| A1-01/03/05 | Existing process-service policies and canonical decoder retained. W3 owns real host composition. |
| A1-08/09/19/26/38/55 | Claim, enrollment, here and sync adapters retained. SyncLauncher is implemented through LauncherSyncReader. W1/W8 production fences, enrollment and sync evidence remain open. |
| A1-11/14/15/16/17 | Existing source, failure, cancellation and execution projections retained. Two merged fixture shapes are corrected. Authentication cancellation-reason projection remains with W1. |
| W9/W2 typed steps | Actual typed client/handler connected. Captured ordinal and nanosecond timestamp properties pass. Retained StateStoreOld callers and native composition remain open. |
| A1-20/49 and seeded bootstrap | Existing root issuer retained. Seeded bootstrap/delivery needs the joint contract and startup caller cut. |
| A1-21/22/23 | Eight more routes leave legacy credential middleware. Slot selectors and typed steps use the shared boundary. Other legacy execution/team routes and auth.go still need retirement. |
| A1-24/25 | Existing source, plan-secret and data transaction seams retained. Production adapter composition remains with the owners. |
| A1-27 | Existing shared billing calculator retained. No pricing or financial policy change. |
| A1-28 and verified provider events | Committed checkout settlement retained. Other event contracts and settlement remain joint W1b/W2 work. |
| A1-29/30/31/32/33/34/35 | Existing signed-data, record, memo and depcache adapters retained. Native authority, metadata, quota and retention composition remains open. |
| A1-39/40/53 | Native runners, local HTTP/runtime composition and full placement evidence remain with the owners and lead. No cell is marked complete from a fake. |

## Shared files

Production caller changes outside W2 are internal/orchestrator/httpconcurrency.go
and hosted_retry.go. They use final shapes, typed errors and route paths.
The retry test follows them. internal/apiroutes adds the actual concurrency
endpoint file and a drift test. The three engine acceptance fixtures follow
amended failure and step wire contracts. No SDK implementation, store schema,
W1 implementation, foreign contract signature or lead-owned ledger is changed.

Shared generated/documentation surfaces are api/openapi.yaml,
docs/api-reference.md, docs/api.md, docs/auth.md, docs/oidc.md, docs/security.md,
docs/migrations/_unreleased.md, CHANGELOG.md, .apidiff/pkg_controller.txt,
.apidiff/pkg_controller_client.txt and their embedded mirrors. perf/budgets.json
records that authorized group-operation timings and response sizes are unmeasured.
C2 and the test port inventory describe the new boundary and its open proofs.

## Verification and friction

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W2/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp.
They use go -C on the assigned worktree and tests use -p 4.

Whole-repository go build, scoped vet, controller policy/route/engine tests,
capability/apierr/apiroutes/apiref/apispec tests, focused client/hosted-retry tests,
API spec/snapshot drift, changelog, whitespace and commentcheck pass. The final
broad W2 acceptance selection passes in 21.592 seconds. The focused typed-step
and reporting selection passes in 2.048 seconds. The final selection is
^Test(Account|Authority|Credential|ConcurrencyHTTP|Data|Secret|Source|Session|Console|Login|OAuth|OIDC|Operator|Deployment|Discovery|Payment|RootProvisioning|GitHubRunnerAdmin|RunnerMetering|Credit|Queue|Sizing|SelfService|Engine|ReportingClient).
No native TypeScript input changed or setup command was run.
The prior session's native setup result is not reclassified as a new cell.

The unfiltered acceptance run failed. Its observed W3 FuzzExecutorFailureMask
seed failures say the rendered host error contains an SDK secret payload.
The merged W2 failure-selector and transition-body fixtures also failed before
their corrections. Focused reruns establish those corrections. The full suite
is not represented as green. Pipelines and independent review remain with the
lead under the swarm brief. No Sparkwing binary, pipeline, Git write,
deployment or release was run by this worker. Native browser, Unix-socket,
Windows and Darwin results are not claimed.

Scratch is removed after final checks. The lead must stage the nine resolved
merge files. W2 remains incomplete.

<!-- design:end -->

STATUS: CONTINUE
