# W2 execution credential retirement

<!-- design:begin -->

## Implemented and deleted

Removed 36 legacy execution registrations. The trigger family, node execution
family, child claim-token handlers, pending-trigger read and raw run-event append
no longer enter the legacy credential chain. Missing paths answer canonical 404.
Raw POST events answers canonical 405 because GET events remains registered.
Both decisions precede credential lookup, including browser session renewal.
PolicyRouter and the remaining business-route fallback share the missing-route
and method response code.

Deleted claim_route.go, runner_claim.go, claim_run.go, claim_dispatch.go and
node_output.go. Removed the old claim principal conversion, caller-supplied
trigger/node execution fences, raw lifecycle handlers, here-token issuer,
claim-specific log mutations, unused trigger provenance filters and old node
projection. Read/seal log backends remain because dashboard reads still use them.

Removed Server.WithLocalExecution and its unused localExecution field. The sole
production caller in pkg/localws/localws.go loses that option. Local execution
must obtain verified authority and self-claim through the existing final contracts.
There is no replacement mode that accepts an unfenced execution report.

The controller diff deletes 3,030 production lines and adds 60. It deletes 539
old test lines. Those figures exclude the two new acceptance test files and docs.
The API snapshot removes WithLocalExecution and claimTokenRoute.ServeHTTP.
The frozen route policy table and shared wire declarations are unchanged.

## Invariants and ports

A retired execution path reaches no handler, regardless of credential
presentation. TestEngineRetiredExecutionRoutesProperty exercises 34 removed
path/method combinations through the actual API and browser handlers. Forty
generated cases per handler vary run/job/child IDs and old bearers, unknown
capabilities, Session headers, cookies and execution headers. Refusals return
the shared envelope and set no cookies. The absent-route branch has no store
fixture, so an accidental session lookup fails the test.

Database failures reveal no private diagnostic in the HTTP response.
TestEngineTransactionContentionProperty ports the removed node-store contention
test through the final job transition route. Direct, wrapped and joined errors
preserve 503 plus one-second retry advice for SQLSTATE 40001, 40P01 and 55P03.
Other database errors remain safe 500 responses. The transaction adapter is fake.

The seven claim-router tests move to the existing credential authentication,
terminal replay, cancellation-reporting and policy-alternative components.
Public job projection uses the existing EngineJobsHTTP private-field checks.
Raw event append accounting moves to the retained store event accounting suite.
Four local-attempt tests and two local-mode budget/advice tests retire with their
removed mode. The port inventory records these changes and their limits.

No native acceptance cell is newly unskipped. Real placement, durable replay,
log-tail and local self-claim composition are not established by these components.

## Remaining work and real integrations

Priority 1 is incomplete. auth.go and its argon2/cache path still serve the
remaining legacy business routes. here_runner.go retains only its legacy token
fence. The old GitHub runner fence, session middleware, team routes, GitHub App
browser flows and startup token issuer still need the final policy cut. Some
now-unregistered metric/live-log handlers and their old tests need final ports
and deletion. They are not a credential fallback for the removed routes.

W2 still needs to implement the declared final child/input, job log/metric/usage,
debug and remaining business endpoints. Reuse the shared wire and transaction
contracts, or record the missing contract before extending another area. Missing
foreign implementations must use fakes without restoring the retired endpoints.

Old HTTP clients in internal/controller/client/client.go, claim.go, coordination.go,
livelogs.go and output.go still name retired paths. Their legacy runtime consumers
need migration or deletion with the final W3/W9 compositions. No forwarding
client adapter was added. The repository builds, but these old runtime paths
are not usable integrations. The only external Go caller change in this slice
is pkg/localws/localws.go.

Priority 2 remains the production console and peer snapshots and committing
mutations. W2's proof projection and Server.HTTP are already implemented against
their interfaces. W1 must supply the authority-bearing persistence adapters.
Identity linking likewise retains its fake committing writer until W1 implements
the nonce and attachment transactions.

Priority 3 remains W1's cancellation projection. authentication_binding.go still
reads cancellation timestamps without a reason. BindingState.CancelReason and
W2's consumption already exist. W1 must project the first job intent before the
run intent in the same authentication snapshot. No second W2 read is added.

The approved seeded bootstrap and verified payment events beyond checkout remain
unfinished joint integrations. Existing source/data/read-set, credential mutation,
rotation, enrollment and billing adapters remain as recorded in the previous
W2 handoffs. This slice adds no foreign signature change or new open design call.
A1-21/22/23 advance through the credential retirement. The other amendment
dispositions stay in W2-concurrency-handoff.md and W2-link-local-handoff.md.
Lead-owned amendments and handoffs are unchanged.

## Shared files and verification

Shared edits are pkg/localws/localws.go, the two acceptance files, the port
inventory, .apidiff/pkg_controller.txt, CHANGELOG.md, docs/auth.md,
docs/migrations/_unreleased.md and their embedded mirrors. Other source changes
are in W2-owned controller code. No new exported interface, cache, environment
variable, timeout or customer limit is introduced.

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W2/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp.
Tests use -p 4. Commands use go -C on the assigned worktree.

The whole-repository build and scoped vet pass. Full capability, apierr,
apiroutes, apiref and apispec tests pass. The W2 acceptance selection and the
in-process controller policy/local HTTP selection pass. Commentcheck against
main, API spec/snapshot drift, changelog, embedded-doc sync and whitespace checks
pass. The final W2 acceptance selection passes in 20.844 seconds and the
controller selection in 0.355 seconds. Scoped vet includes pkg/localws after
the external caller migration.

The acceptance selection is:

```text
^Test(Account|Authority|Credential|ConcurrencyHTTP|Data|Secret|Source|Session|Console|Login|OAuth|OIDC|IdentityLink|Operator|Deployment|Discovery|Payment|RootProvisioning|GitHubRunnerAdmin|RunnerMetering|Credit|Queue|Sizing|SelfService|Engine|ReportingClient)
```

The passing controller selection is:

```text
^(TestAuthorizedHandlersDoNotAuthorize|TestPolicy|TestServerPolicy|TestLocalControllerHTTP)
```

The broader controller command used the same prefix with `|TestAuthRoute|TestRoute`
appended. It reached TestRoutes_RejectSimpleRequestContentTypes and failed with
`httptest: failed to listen on a port: listen tcp6 [::1]:0: socket: operation not permitted`.
That socket test was not retried. The narrower in-process selection passes.
The initial retirement property also exposed method misses entering session
lookup. Matching before authentication fixes that cause in both API and browser
paths. Removing dead declarations exposed unused imports, which are deleted.

No TypeScript setup was needed or attempted. Only the required read-only
Sparkwing discovery ran. No pipeline, candidate, external service, Git write or
release ran. Git configuration has no core.worktree override. Scratch is removed
after the final checks.

<!-- design:end -->

STATUS: CONTINUE
