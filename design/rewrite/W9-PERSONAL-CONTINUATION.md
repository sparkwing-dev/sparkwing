# Personal-machine enrollment continuation

The personal enrollment slice is implemented. `runners add --personal
--session-stdin` consumes an existing independent account session, fetches its
CSRF proof, and enrolls the machine through the real controller route. The
session and proof stay in memory. Only the returned runner credential reaches
the private credential file. Missing `--session-stdin` refuses before service
preparation or an HTTP request.

`client.NewSessionRunnerTokenManager` uses the existing Session authorization
and CSRF contract for enrollment and token management. It refuses simultaneous
bearer input, malformed header values and redirects. Enrollment cannot replay a
lost one-time credential. Failed service installation revokes the new machine
through the same session and restores the local configuration.

## Authority invariant

A member bearer, including a session derived from that bearer, cannot establish
independent machine consent. The controller and transactional store retain this
boundary. Generated foreign presentations and capability-derived sessions,
including those with administrative grants, fail through the client and real
Handler. The existing personal claim test now uses the session client to enroll,
refuses a different run creator, accepts the authenticated owner, and verifies
revocation ends access.

## Evidence

These focused checks passed with `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache` and `go test -p 4`:

- `./cmd/sparkwing -run '^TestRunners' -count=1`.
- `./pkg/controller/client -run '^(TestCredential|TestRunnerSession|TestRunnerEnrollment|FuzzCredential|FuzzRunnerEnrollment)' -count=1`.
- `./pkg/controller -run '^(TestBurstPersonalEnrollmentAndClaimsUseTheAccountOwner|TestPersonalEnrollmentTransportNeverPromotesCapabilityAncestryProperty)$' -count=1`.
- `./internal/clisurface -count=1`.

`TestRunnersPersonalEnrollmentCLIThroughController` exercises both registration
and failed-install rollback through the actual CLI, client, Handler, CSRF and
SQLite store. Only service-manager execution and the HTTP connection are
substituted. The HTTP adapter calls Handler directly and opens no sockets.
`TestRunnersPersonalEnrollmentUsesEphemeralSession` checks that session material
appears in neither saved configuration nor output. Header-injection properties
and lost-response/redirect tests cover the new transport.

The whole repository build passed. `commentcheck -base main .` passed across
2,663 files, and `git diff --check` passed. Focused vet encountered a concurrent
store edit with an unused variable in `blob_authority_projection.go`. The lead
owns final build, vet, generated documentation and native service verification.
No acceptance cell was newly unskipped, and no old test port was pending in this
slice. The existing personal enrollment/owner claim test was moved from manual
HTTP construction to the production session client.

## Contract changes made

- `c2-credentials.md`: the personal enrollment paragraph now names ephemeral
  Session transport and CSRF, preserves independent consent, and documents the
  HTTPS/insecure-HTTP cookie convention.
- `c3-controller-wire.md`: records `NewSessionRunnerTokenManager`, its refusal
  rules, `--session-stdin`, and rollback through the same session.
- `internal/clisurface/registry.go`: adds `--session-stdin` and explains the
  existing independent-session requirement. The lead regenerates its reference
  pages and adds the run-document usage example.

No controller permission, route policy, persistent schema or SDK protocol
changed. No lead-owned amendments or handoffs file changed.

## Scope, budget and friction

Production delta for this slice is **+92 lines**: `runners.go` +15,
`registry.go` +1, and the new session credential client +76. No legacy production
implementation was replaced. Thirteen lines of manual enrollment HTTP test
setup were replaced with the production client. Changed shared files are
`registry.go`, `burst_overflow_test.go`, and the two named contract documents.
The other files belong to the runner CLI and credential client, plus their new
tests.

Mechanism added: an ephemeral session input flag and a credential-only session
transport. Reusing the saved member bearer cannot supply independent consent,
and adding a login/session store would be a larger change. Removing the flag
and constructor reverses this addition. Existing account sessions must come
from the authenticated account flow. This work adds no session export or login
system.

Temporary concurrent source/store compile failures were reported to the lead.
The source failure resolved before the native CLI tests ran. No candidate
Sparkwing binary, installed service, socket, git write or durable account was
created. No worker temporary directory remains. Native enrollment against
Cloud and the actual runner service remains a lead verification step.

STATUS: COMPLETE
