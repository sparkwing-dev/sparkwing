# W2 reporting credential continuation

<!-- design:begin -->

Started at ea1019646. No Git writes. Lead-owned handoffs.md and amendments
are unchanged. The previous lead-decision report remains the record for payment
verification, seeded bootstrap, LocalHTTP and SyncLauncher.

## Implemented and deleted

Twenty-two reporting GET registrations now use PolicyRouter. Run lists,
receipts, attempts, steps, events, debug pauses, approvals, pipelines, latest
runs, trends, runners, sizing and logs consume capability.Authorized. Their
handlers no longer use the legacy Principal, scope middleware or team boundary.
The authorized target selects the tenant. Run and job selectors come from the
matched final resource and its resolved owning team. Backend failures pass
through the canonical private-error boundary.

Deleted the legacy read registrations and their tenantRoutes entries. Job log
reads use /runs/{run}/jobs/{job}/logs with stream and completeness beneath it.
The old /runs/{run}/logs/{job} paths, /agents, /approvals/pending,
/capacity/profiles, /capacity/profiles/explain and the paused alias are removed.
Their Go client and dashboard callers use the final paths. No alias or alternate
credential verifier was added.

The existing egress measurement body is shared with the authorized log reads.
Its team and principal come directly from Authorized, including replica-scoped
usage writes. Log reads retain their download and stream slots and their
exemption from paid-download charging. The legacy meter wrappers remain with
the remaining accounting integration. No quota, cap, pricing or default changed.

Method discovery now combines both actual registration sets. A retired method
on a migrated GET path still returns canonical 405 with Allow. LocalHTTP searches
only its final registration set. Retired paths return canonical 404 before any
credential lookup. These changes preserve the existing raw-event refusal test.

The 258-line handwritten permission table moved from
internal/acceptance/credential_routes.go to
internal/controller/credential_route_expectations_test.go. Its declaration,
mutation-sensitivity and order tests moved from tests/acceptance beside it.
The production harness file and old comparison test file are deleted. The
expected literals are unchanged. The remaining executable policy tests iterate
runtime alternatives. The independent moved comparison still pins their exact
permission contract. No scenario, Harness method, registry or native cell was added.

W2 production delta this session: 254 added, 276 removed, net -22 lines.
W2 production delta versus main: -888 lines, within the -250 budget.
These counts follow the lead script's ownership and exclusions: test files,
testdata, acceptance harnesses and contract declaration files are excluded.
Untracked production reporting_routes.go is included. Shared client and
apiroutes edits are outside the W2 ownership total.

## Invariants, ports and acceptance

Invariant: a reporting read uses only the policy-authorized team and resource;
request selectors, legacy Principal values and old scopes grant no access.
TestReportingRouteAuthorityProperty generates selectors and revoked, expired,
wrong-audience, removed-member, wrong-grant, deployment and foreign-team
credentials across all 22 routes. It uses the real capability verifier and
policy selector against committed-fact fakes. Every negative case repeats
through the actual Server.Handler registration. No denied request reaches the
read handler. These are component proofs, not native persistence acceptance.

TestCapacityReadUsesRequestTenantProperty now exercises the real SQLite sizing
read with an explicit Authorized target. A conflicting legacy Principal and
forged query selectors cannot select another team. Missing authority is exactly
policy_missing; an unregistered team is exactly not_found. The existing
anonymous-bootstrap property uses /runners. The retained agents liveness
fixture passes its explicit authorized target.

TestReportingRetiredRoutes checks all removed paths. The actual client NodeLogs
read, stream, completeness and whole-run requests are covered by
TestNodeLogReaderUsesJobRoutes, including escaped run/job selectors and bearer
transport. The existing raw-event retired-method property remains active.
The port inventory records the moved table and these retained obligations.
No native matrix cell is newly completed.

## Remaining work and foreign integration

- Finish the remaining legacy registrations: schedules, run deletion, debug and
  approval writes, team membership/invitations/trust/GitHub/billing/storage and
  metrics. Then delete auth.go's token-table verifier/cache, legacy middleware,
  fences, tenant boundary and obsolete credential tests/callers together.
  Their remaining callers prevent deleting the verifier safely in this cut.
- Final-schema reporting store reads remain open. Several retained tenant reads
  still consume legacy runColumns and other old projections. This session moves
  their authorization and transports, not W1's SQL. Native receipt, runner,
  log and run-list composition is not established. No second SQL reader was added.
- W1 still lacks EnsureBootstrapCapability and HasDeploymentAdministrator.
  The configured seeded startup path refuses missing adapters. W2's seeded
  request/digest-only contract is unchanged.
- W1 still does not populate BindingState.CancelReason in
  authentication_binding.go. C2 already carries the field and W2 consumes it.
  Preserve job-before-run first-intent precedence when W1 projects it.
- W1's authentication reader still accepts only persisted bearer/session
  classes. Verified peer snapshot and transaction authority remain open.
  The local transport supplies verified uid/SID subjects to the shared boundary.
  Roleless sessions and the previously reported console mutations remain open.
- W1 still lacks LauncherSyncReader.ReadLauncherSync. W2's SyncLauncher remains
  implemented against that interface, without the old SyncLaunchJobs fallback.
  W8/W9 must supply immutable award/generation/Job-instance correlation.
- W1b SettlePayment still accepts only checkout outcomes. The verified card,
  payment, refund, warning and dispute facts from the previous session still
  need its committing settlement. No financial integration is claimed here.
- The earlier authority-bearing credential/account/concurrency/sizing/data/source
  and identity-link transaction handoffs remain open where not landed by owners.

No foreign contract signature changed. No new decision or approval gate was
introduced. The small reportingTenant and meterResponse helpers centralize
existing tenant selection and measurement without duplicating either concern.

## Verification and shared files

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp. Tests use -p 4.

- Whole-repository go build -p 4 ./... passes.
- Scoped vet passes for capability, apierr, controller, controller/client,
  apiroutes and executable acceptance.
- The full W2 executable acceptance selection passes in 14.753 seconds.
- Reporting authority/hard-cut, SQLite sizing, local HTTP, route guards,
  independent route declarations and startup policy selections pass.
- Actual client log/status and retained egress/accounting selections pass. Capability, apierr,
  apiroutes, apiref and apispec suites pass.
- Documentation mirror, embedded changelog and symbol-reference checks pass.
- Native Linux TypeScript TestNative selection passes in 7.352 seconds.
  No dependency setup error occurred.
- Commentcheck -base main is clean. Final formatting and diff checks pass.

Shared production callers: internal/controller/client/client.go,
internal/controller/client/logs.go and web/src/lib/api.ts. Source route discovery:
internal/apiroutes/apiroutes.go. Shared test/harness edits: deleted
internal/acceptance/credential_routes.go and
tests/acceptance/credential_routes_test.go; updated
tests/acceptance/credential_policy_test.go. Other shared edits are
t1-credential-routes.md, t1-port-inventory.md, docs/api.md, docs/auth.md,
docs/migrations/_unreleased.md, CHANGELOG.md and their embedded mirrors.
All other source changes belong to W2.

The first route-cut run exposed method discovery searching only the old mux.
The existing acceptance property failed, the registration lookup was corrected,
and the same property passed. A temporary unused-import failure and one scripted
edit's unmatched import marker were corrected before final checks. No test
expectation was weakened to admit those failures. No socket-dependent full
controller suite, pipeline, candidate binary, service, release or Git write ran.
The shared build cache is retained. W2 scratch is deleted at session end.

<!-- design:end -->

STATUS: CONTINUE
