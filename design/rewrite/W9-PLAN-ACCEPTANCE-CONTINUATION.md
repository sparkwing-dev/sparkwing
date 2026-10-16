# W9 plan acceptance and server diagnostics

The repository example failed because `RunSnapshot.Scheduling` rejected every
initial job with `Groups`. The SDK uses that field for static `JobFanOut` and
`GroupJobs` display membership. The first rejection was `initial job
"audit-secrets" has generated membership`. This happened after plan validation,
inside acceptance's scheduling projection, so the transaction rolled back and
the API returned 500. Removing the three-line rejection accepts static groups.
Generated membership still comes exclusively from accepted expansion records.

The real SDK pipe test now submits the example plan through the shared HTTP
handler with local peer intake, the production local claimant, the real store,
repository checkout declarations and local settings resolution. It checks the
persisted seal and jobs, then requires the decider to schedule `configure`.
It ran red with the exact 500 above and now passes. No sockets are used. The
verified-peer callback supplies the identity that the socket boundary normally
provides. This test does not exercise OS peer verification or execute jobs.

Server error writers preserve the original error on the existing response
recorder before sanitizing the response. Request completion emits one ERROR
record with the request ID, route pattern, status and masked cause. Legacy
internal writers, browser rendering and database health use the same path.
Backend adapters retain causes instead of discarding them when producing a
safe API error. Duplicate handler diagnostics were removed. Panic diagnostics
are masked before propagating the HTTP abort sentinel, which prevents net/http
from logging the original unmasked panic a second time.

The controller shares the existing secrets masker implementation across its
request diagnostics, authenticated credentials and opened masked plan secrets.
The invariant tested is that no registered secret appears in the new server
failure diagnostic or client error, including raw, base64 and URL-escaped forms.
The property covers policy, legacy, internal, malformed-envelope, typed 503,
browser-template, bare-status and panic paths. It requires exactly one error
record and the response's request ID. An overlay removing the logger makes it
fail immediately. Numeric retry delays survive generic 5xx responses. Codes
survive too; other private messages, hints and details are withheld.

The only added mechanism is error attachment to the existing response recorder.
It lets response writers without a request parameter share one logging boundary.
Changing every legacy handler signature would be larger. There is no new public
setting, environment variable, interface contract, retry loop or schema.

Verification passed with `GOWORK=off GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache` and `GOTMPDIR=/tmp/sw-astra-W9/go-tmp`:

- `go build -p 4 ./...` and `go vet -p 4 ./...` in both modules.
- 28 focused controller tests from audit, policy router/store, error response,
  plan secret, peer intake and repository SDK pipe files.
- The new diagnostic property and repository example regression under `-race`.
- `go test -p 4 ./pkg/store -run '^TestScheduling' -count=1`.
- Engine client tests, embedded documentation/catalog/search tests,
  `bin/sync-docs.sh --check`, commentcheck against main and `git diff --check`.

Next failure: the production local controller probe cannot bind its Unix
socket. `TestReviewProductionExecutionBindings/local_controller` reports
`setsockopt: operation not permitted`. Its cleanup also cannot query the socket.
A process check found no surviving test/controller process. A wider controller
selection stops when a GitHub test opens a TCP listener; the broader docs suite
also stops in a web-client listener test. Those runs establish no runtime result
past the sandbox refusal. Native execution of `sparkwing run example` remains
with the lead. No new numbered acceptance cell or archived test port is claimed.

Shared production files touched under `pkg/controller` are `server.go`,
`handlers.go`, `policy_router.go`, `policy_store.go`, `plan_secret_adapter.go`,
`browser.go`, `browser_flow.go`, `browser_github_app.go`, `browser_login.go`,
`browser_login_routes.go`, `browser_oauth_routes.go`, `billing_routes.go`,
`engine_routes.go`, `git_credentials_team.go`, `github_actions_exchange.go`,
`github_app.go`, `github_app_extra_repos.go`, `github_app_routes.go`,
`github_app_webhook.go`, `github_runner_admin.go`, `identity_link_routes.go`,
`intake_policy.go`, `intake_source.go`, `oidc_token.go` and `session_routes.go`.
The store change is `pkg/store/scheduling_contract.go`. Tests in both packages
and `docs/local-execution.md` with its embedded mirror changed too.

This task adds 98 production Go lines and deletes 48, net +50. No legacy file
was removed. W9-owned production versus main is +16,916/-23,812, net -6,896.
No foreign contract declaration changed. There are no open design questions.
The friction is blocked socket operations. No git metadata was written. Scratch
is removed after checks finish, while the shared Go cache is retained.
