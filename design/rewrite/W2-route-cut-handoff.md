# W2 runner administration and billing hard cut

<!-- design:begin -->

Starts from 30017d6a9. No Git writes or Sparkwing executable ran.

## Implemented and deleted

Removed twelve legacy registrations: eight credits.grant service routes, three
team/github-runners routes and token-prefix metering. Deleted the inbound
financial service handlers, their private wire types, credits-token loading
and seeding, EnsureCheckoutToken, ScopeCreditsGrant and requireServiceScope.
The surviving card worker keeps its in-process settlement helper.

The Go production diff removes 677 lines and adds 247, a net reduction of 430.
This includes the shared caller and reference-generator changes. The former
credit_reversals.go retains only the two still-used principal helpers in
principal.go. tokens.go is deleted. Retired service transport tests and the
old token metering and GitHub permission tests are removed. The owner-budget
test remains. T1's port inventory records the pending financial-event ports.

GET/POST /api/v1/runners/github and DELETE /api/v1/runners/github/{repository}
use PolicyRouter and the existing member grants. The authenticated team and
subject select tenant and actor. GitHub App coverage resolves repository IDs
when configured. Mutations use C2's GitHubRunnerRegistry. Reads keep the
existing tenant projection. The dashboard uses the final paths.

POST /api/v1/operator/runners/{runner}/metered uses console proof and CSRF,
requires an explicit boolean and returns runner/metered after commit. The
RunnerMeteringWriter transaction selects the enrolled root by its public
capability prefix because machine names repeat across teams. It must keep
existing awards' billing provenance unchanged. Missing writers refuse with
policy_missing. No token-table mutation or auth-cache invalidation remains
in these handlers.

Invariant: only live team administration changes repository enrollment, and
caller-supplied team selectors never redirect reads or writes. The generated
HTTP properties exercise that invariant and console-only metering. Session
CSRF, private errors, invalid bodies, positive numeric IDs, foreign reader
results and transaction refusals have active component tests.

## Other-area contracts and open integration

C2's local declarations add GitHubRunnerRegistry and RunnerMeteringWriter.
Their exact signatures and transaction requirements are in C2 and handoffs.md.
No C1 signature, schema, issuance implementation or SQL adapter changed.

A real SQLite probe of Tenant.AddGitHubRunnerBinding failed with
`NOT NULL constraint failed: github_runner_bindings.repo_key (1299)`.
The probe's input was repository acme/widgets, repository_id 42 and owner_id 7.
It cannot implement the new transaction contract. The default empty inventory
and missing-writer refusals pass against real SQLite. Enrollment mutation,
root revocation against concurrent Actions issuance and enrolled-root metering
use fakes. C1 still needs their committing adapters and final timestamp reads.

The signed checkout callback remains. PaymentEvent and SettlePayment cover
checkout payment, failure and expiry only. Card setup, off-session outcomes,
refunds, warnings and disputes need a joint verified-event extension. Old
bearer callbacks are deleted. Existing store tests retain their financial
invariants. No final provider-HTTP port is claimed for those events.

A1-20/21/23/24/26/28 advance through the route cuts and transaction declarations.
A1-27 billing calculation, A1-31/34 data signing, A1-38 launcher transport and
A1-55 here behavior retain the prior W2 implementations and handoffs. Other
W2-assigned amendments are unchanged from W2-oauth-handoff.md and its linked
continuations. No finished mechanism was rebuilt.

Legacy bearer/claim authorization, startup bootstrap, login/link cutovers and
console/peer adapters remain. W1's cancellation reason authentication
projection is absent in this checkout. W2 still consumes the declared
BindingState.CancelReason interface. No cancellation code changed here.

## Shared files

- cmd/sparkwing-controller/main.go and credentials.go remove the sole seeding
  caller, credential field and help entry. Whole-repository build passes.
- internal/apiroutes/apiroutes.go and apiroutes_test.go discover the new
  registered group. internal/apiref/main.go removes the service-scope prose.
- internal/orchestrator/wingd_api_routes_test.go drops the retired metering
  route from its socket API inventory.
- web/src/lib/githubApp.ts and githubApp.test.ts use the final GitHub paths.
- api/openapi.yaml, docs sources, embedded mirrors, CHANGELOG and
  .apidiff/pkg_controller.txt follow the changed surface.
- .apidiff/pkg_runner.txt is removed because pkg/runner no longer exists.
  The generator writes existing packages and left this retired snapshot behind.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W2/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp.
Commands use go -C with the absolute worktree. Tests use -p 4.

- Whole-repository go build passes.
- Full executable tests/acceptance passes in 73 seconds. Existing native
  skips remain. The final changed payment/GitHub/metering HTTP selection passes.
- capability, apierr, apiroutes and apiref full tests pass. apispec full tests
  pass after obsolete paths/types are removed and the final routes described.
- Scoped controller policy, tenant inventory and handler-boundary checks pass.
- Scoped vet and commentcheck against main pass. API snapshot, embedded docs
  guard, changelog mirror, scrub and sync-docs --check pass.
- The dashboard's githubApp.test.ts passes under node --import tsx --test.
- npm run typecheck in sdk/typescript passes. Native TypeScript setup works:
  go test -p 4 ./sdk/typescript/test/nativehost -run '^TestNative' -count=1
  passes both native protocol and conformance tests on Linux in 9.3 seconds.
  This does not complete the credential/placement native matrix.

Two broader checks remain red outside this cut:

- pkg/docs TestDocsNameOnlySymbolsThatExist names 17 retired SDK symbols in
  unchanged pages, including Await, CacheKey, Memoize and WhenRunner. W4/W9
  own those ports. The changed docs/mirror checks pass.
- Full cmd/sparkwing-controller tests fail TestCheckRequireAuth_AMultiTeamControllerWithNoTokenRefusesToStart
  with `no such table: tokens`, then a secrets-key test cannot bind a socket
  (`socket: operation not permitted`). The scoped TestPaymentConfiguration, TestReadCredentials* and
  TestSecretsKeyCredentials checks pass.
  The legacy startup-auth test still needs its final contract port.

No live provider, PostgreSQL, native browser, full credential matrix, deployment,
performance, pipeline or gate result is claimed. The lead owns pipelines and
independent review under the swarm brief. Scratch is deleted after verification.

STATUS: CONTINUE

<!-- design:end -->
