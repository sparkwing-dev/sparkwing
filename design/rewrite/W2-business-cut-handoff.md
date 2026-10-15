# W2 schedule and review credential continuation

<!-- design:begin -->

Started at d9e4cc463. No repository Git writes. Lead-owned handoffs.md and
amendments are unchanged. Earlier reports retain the payment, seeded bootstrap,
LocalHTTP and launcher-sync work; their component regressions pass again.

## Implemented and deleted

Removed 22 legacy registrations: ten schedule/team reads, API metrics, eight
schedule writes, approval resolution, debug-pause creation and raw approval
request. The ten final schedule routes use /api/v1/schedules, including DELETE
for a single schedule. There are no crons or disarm aliases. Approval objects
use job_id; resolution returns the shared ApprovalResult envelope. Human
resolution cannot select an approver or the owner-only timeout outcome.
Debug-pause creation accepts only job_id, reason and optional expires_at.
Run-now, approval resolution and debug creation require a replay header.

ScheduleWriter and RunControlWriter are C2-local transaction interfaces in
real implementation files. Their resolvers check the authorized team against
the returned tenant. Default resolution refuses missing adapters with
policy_missing. No old schedule, review or debug mutation is a fallback.
Schedule declarations and overrides use the existing shared wire types and
validators. List/detail preserve all/next/fires queries and explicit zero.
Existing cron rendering owns metadata and history presentation.

The migrated reads select tenants from Authorized. Infrastructure errors stay
behind the canonical private-error boundary. Billing availability follows
authentication. API metrics uses deployment operator.limits or console policy;
the dedicated metrics listener keeps its existing network boundary.

Controller endpoints now register once in one PolicyRouter and mount by exact
method. Browser proof routes retain their own registration helper. This removes
six redundant registration wrappers and two per-endpoint router loops. Exact
mounting also keeps the remaining legacy write methods reachable through their
existing boundary while final local HTTP excludes them. Method discovery omits
ServeMux's implicit HEAD unless HEAD was registered. Shared read/write paths
return canonical 405 with their complete explicit Allow set.

Deleted 709 W2 production lines and added 590: session net -119. W2 production
net versus main is -1007, within the -250 budget. Counts use the lead's ownership
rules, excluding tests, testdata, acceptance harnesses and contract declaration
files, and include untracked production files. Thirteen replaced cron HTTP test
functions are deleted, removing 323 old test lines. The legacy token verifier
and its remaining callers are still present, so W2 is incomplete.

## Invariant, ports and acceptance

Invariant: only the verified authority and matched resource select the team
and actor; request fields and legacy credentials grant no access.

TestReportingRouteAuthorityProperty now covers the new reads, schedules and
review writes as well as the prior reporting routes. Generated revocation,
expiry, audience, membership, grant, deployment and foreign-team cases pass
through the real verifier/policy selector against committed-fact fakes. Denied
cases repeat through Server.Handler and never reach the read or write handler.
TestRunControlHTTPAuthorityProperty generates comments/reasons and transaction
refusals while checking verified team/run/actor forwarding. Its refusal cases
reject forged approvers, team/lifecycle fields, timeout decisions and body keys.
These are component properties, not production transaction evidence.

TestScheduleHTTPReadWriteAndReplay, OverrideAndWithdrawal,
InvalidInputAndBackend and RetainedHistoryAndCounts port thirteen old HTTP
regressions. Real SQLite holds schedule metadata and sixty retained fires.
The writer fake supplies fixed verified GitHub IDs, invokes the existing cron
library and simulates run-now replay. It does not prove GitHub resolution,
atomic authority checks, final intake, admission/billing limits or durable
idempotency. The old run-now and team/admission fixtures remain integration
work. Their legacy trigger/state APIs are not accepted by the new endpoints.

TestBusinessRetiredRoutesAndExactMethods checks retired crons and raw gate
requests plus exact methods on shared paths. The mixed-boundary regression
checks a migrated read cannot shadow an unfinished legacy write. Metrics policy
and local dashboard route specificity pass. Actual source discovery is pinned
by TestParseControllerBusinessRegistrations. No acceptance registry or native
matrix cell was added or newly marked complete.

## Remaining work and foreign integration

- Finish the 17 remaining legacy registrations in server.go: run deletion;
  team rename/deletion; invitation writes; build-trust write; GitHub App
  unbind, trigger/automation/extra-repository writes and automation read;
  billing checkout/card/pay/budget. Then delete auth.go's token-table
  verifier/cache, legacy middleware/fences, tenant boundary, Principal and
  obsolete callers/tests together.
- W1 must implement the exact ScheduleWriter and RunControlWriter contracts in
  c2-credentials.md. Recheck authority, membership, tenant, repository and
  current lifecycle inside each committing transaction. Use the store clock,
  preserve current schedule/admission/billing limits, mask public text and
  commit state/events/replay together. These adapters are absent here.
- W1/W3 must connect gate creation to final owner lifecycle. Client.CreateApproval
  still belongs to StateStoreOld, used by nodehost/approvals.go and maskstate.go.
  It calls the retired HTTP request path. The old client approval/debug methods
  and surviving legacy tests need that runtime retirement; no unauthorized
  replacement route or guessed foreign signature was added. W9's typed
  RunReview and ScheduleManager clients use the final messages.
- The old pkg/localws scheduler is still a separate legacy implementation.
  Only its paths and corresponding caller tests changed to follow the
  dashboard. W9/W8 still own its retirement into the shared LocalHTTP runtime.
- W1 still lacks EnsureBootstrapCapability and HasDeploymentAdministrator.
  Seeded startup refuses missing adapters. Seed acceptance must happen once
  inside gated Issue, retaining only the digest and surviving revocation.
- W1 still does not project BindingState.CancelReason from independent first
  cancellation intent. C2 and W2 already consume the field; job intent must
  precede run intent. W1's authentication_binding.go still reads cancellation
  flags without that projection. No alternate SQL reader was introduced.
- W1's roleless/console/peer authority representation, LauncherSyncReader,
  credential/source/data transaction handoffs and final-schema reporting reads
  remain open as described in prior W2 reports.
- W1b SettlePayment still refuses the verified non-checkout event kinds. W2's
  provider verification and callback handoff remain implemented and tested.

No other area's contract signature changed. No new product/security decision
is requested. The source-table correction makes a missing bare schedule resolve
as store.ErrNotFound while preserving its existing message. Contract and
migration notes describe the hard cuts and required adapters.

## Verification and friction

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR under /tmp/sw-astra-W2/tmp.
Tests use -p 4 and commands select this worktree with go -C.

Passed:

- Whole-repository go build -p 4 ./... and scoped vet for controller,
  controller/client, localws, apiroutes, crons and executable acceptance.
- Controller component selection in 6.180 seconds: ^Test(Payment|
  EnsureBootstrapAdminToken|EngineLauncherSync|LocalController|PolicyPeer|
  Console|ScheduleHTTP|Reporting|RunControlHTTP|Business|MetricsHTTP|Policy|
  RouteGuard|CredentialRoute|StoreDataStorageRoutes|CallerStorageStanding).
  The line breaks above are prose; join the alternatives in the actual regex.
- The full prior W2 executable acceptance selection in 13.704 seconds:
  ^Test(Account|Authority|Credential|ConcurrencyHTTP|Data|Secret|Source|
  Session|Console|Login|OAuth|OIDC|IdentityLink|Operator|Deployment|Discovery|
  Payment|RootProvisioning|GitHubRunnerAdmin|RunnerMetering|Credit|Queue|
  Sizing|SelfService|Engine|ReportingClient).
- Full capability, apierr, apiroutes, apiref and apispec suites.
- Localws TestMuxSpecificity_CronRoutes.
- Native Linux TypeScript: go test -p 4 ./sdk/typescript/test/nativehost
  -run '^TestNative' -count=1 -timeout=180s, in 7.882 seconds. No setup error.
- Web type check: env -C <repo>/web ./node_modules/.bin/tsc --noEmit
  --incremental false.
- Commentcheck -base main: clean across 2149 files. Documentation mirror,
  embedded changelog/private-content and symbol checks pass. API documentation
  and public API snapshots are regenerated. git diff --check passes.

The broader internal/crons suite fails on old fixtures: Git fixture commits
cannot use the sandboxed GPG agent, old scalar on.schedule YAML is refused with
"expected a sequence", and pushed GitHub rows lack resolved numeric IDs.
An overlay of HEAD's unchanged crons.go reproduces
TestArmPushed_StoresCloneURLBranchAndPin's exact "GitHub repository requires a
resolved numeric ID" failure. The targeted legacy Resolve tests also fail at
that old YAML fixture before reaching resolution. No assertion or skip was
weakened. The new in-process schedule tests use fixed verified GitHub facts and
exercise bare-name not-found through the actual HTTP boundary.

During implementation, checks caught duplicate path mounts, implicit HEAD in
Allow, missing JSON headers in new test requests and the billing guard running
before authentication. Those issues are fixed and the final regressions pass.
No socket-dependent full controller/browser suite, candidate binary, pipeline,
service, release or repository Git write ran. The lead owns final review and
landing. Scratch is deleted at session end; /tmp/sw-gocache is retained.

## Shared files

Caller changes outside W2: pkg/localws/crons.go; its crons, mux_specificity,
localws and token_gate test files; web/src/lib/api.ts, app/page.tsx,
components/Nav.tsx and browser dashboard/cloud-admission fixtures. Source
discovery changes: internal/apiroutes/apiroutes.go and its tests. Shared cron
change: internal/crons/crons.go's typed missing-name error. Other shared edits:
c2-credentials.md, t1-port-inventory.md, docs/api.md, docs/auth.md,
docs/migrations/_unreleased.md, CHANGELOG.md, their embedded mirrors and
.apidiff/pkg_controller.txt. All other source changes belong to W2.

<!-- design:end -->

STATUS: CONTINUE
