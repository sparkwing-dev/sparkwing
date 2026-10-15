# W2 session lifecycle continuation

<!-- design:begin -->

HEAD is bc69b95fadbe8273db4e473ebef4a79f25b996fe. The checkout started clean.
No merge conflict or Git write was part of this continuation. Earlier completed
work and amendment dispositions remain in W2-secret-reads-handoff.md.

## Implemented

GET /api/v1/auth/session and POST /api/v1/auth/logout use PolicyRouter and
the existing AuthBrowserFlow proof. A single Session authorization header or
session cookie supplies the credential. An Authorization header takes
precedence. A malformed header cannot fall back to a live cookie. Duplicate
selected presentations and HEAD are refused before renewal.

Invariant: failed proof cannot renew or delete a live session, and logout can
end only the verified session. Generated HTTP tests check forged and ambiguous
presentations, absent and cross-origin CSRF, planted tokens and both transports.
Logout verifies the persisted-key-derived session token, its matching header
and cookie, and the same origin. It takes no body. Query selectors confer no
authority. Successful logout clears the browser cookies and leaves another
session untouched.

The read verifier uses C1 LookupSession and LookupSessionAndRenew. Both recheck
current authority through real SQLite. Renewal preserves current membership
and parent grant ceilings. A property varies session ages across the existing
absolute lifetime, checks the persisted renewal ceiling, and proves that
over-age and future-created sessions are removed. A separate real parent
revocation test refuses both operations without renewal or deletion. Other
session consumers' absolute-lifetime enforcement remains open.

Both routes return no-store and canonical errors. The browser router sends
these paths directly to their proof boundary, avoiding generic pre-renewal or
plaintext CSRF errors. No new persistence interface, key, clock, configuration,
customer lifetime limit or compatibility path was added. The context value
carries verified facts because Endpoint.Verify returns only an error.

The local C2 wire correction removes body-selected logout. Its route, proof
kind and retry policy are unchanged. Public docs, OpenAPI, generated reference,
embedded mirrors and the performance manifest describe the change. Performance
is unmeasured and the manifest states the missing authenticated corpus.

## Deleted and ported

Removed 67 lines of legacy session handlers and the logout body type, plus
two imports and two direct registrations. The missing-session regression now
uses an in-memory HTTP recorder and checks the canonical error. Its obsolete
flat-error assertions and network listener are removed.

Six TestSessionFlowHTTP tests now pass: RenewsWithNamedProof,
LogoutUsesVerifiedSession, ProofProperty, RenewalCeilingProperty,
ParentRevocationEndsRenewalAndLogout and RefusesEndedAndInvalidSessions.
The existing TestSessionRoutePreservesCapabilityScopeProperty still passes.
The T1 session inventory records the additional component evidence. No native
placement, HTTP listener or api.sock matrix cell was unskipped. The wider
legacy login fixtures and their ports remain with the browser cutover.

## Other areas and remaining W2 work

No foreign contract signature changed. W1 must consistently wrap session
infrastructure failures with ErrSessionBackend. In pkg/store/auth.go, the
authorityChainTx and RowsAffected error returns omit that marker. A temporary
test overlay created a valid real account session, dropped memberships, and
called GET /api/v1/auth/session. It failed with HTTP 401 unauthenticated instead
of 503 unavailable. The overlay is removed with scratch. This finding is open,
and no claim of complete backend-failure classification is made. The existing
typed backend/key cases pass. W2 added no SQL or error-message workaround.

W1 still owns roleless/console session representation, cancellation-reason
snapshot delivery, the credential reader/writer/rotation/audit adapters and
the prior data, source, enrollment, billing and sync integration points. W9's
NewEngineClient and NewDataClient remain panic stubs. These facts were checked
against this checkout. The controller transport tests continue using declared
interfaces and fakes where those integrations are unavailable.

A1-20/21/22/23 gain the account session lifecycle implementation above. Their
remaining route, login, OAuth, console and verified-peer cutovers stay open.
W2 root machine/service/bootstrap provisioning, capability inventory/rotation
and the previously recorded payment event/configuration question also remain.
The other W2 amendment groups retain the dispositions in the prior handoff:
A1-01/03/05, 08/09/11/14/15/16/19, 24/25/26/27/28, 29/30/31/32/33/34/35,
38/39/40/49/53/55. This session neither rebuilds their completed pieces nor
claims their native integrations complete. The W1/W2 session/cancellation
handoff is partly completed by the session work here.

## Shared files

The sole production caller/registration migration outside pkg/controller is
internal/apiroutes/apiroutes.go: its discovery list includes sessionEndpoints.
No other package needed an API caller migration. Other shared edits are
tests/acceptance/session_flow_http_test.go, contracts/c2-credentials.md,
contracts/handoffs.md, contracts/t1-port-inventory.md, api/openapi.yaml,
docs/auth.md, docs/api-reference.md, docs/migrations/_unreleased.md,
CHANGELOG.md, their pkg/docs copies and perf/budgets.json. No W1 implementation
was edited. Exported Go API shape is unchanged.

## Verification

Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and
GOCACHE=/tmp/sw-astra-W2/cache, with GOTMPDIR and TMPDIR under that directory.
Go tests use -p 4. Final evidence:

- Whole-repository go build -p 4 ./... passes.
- Scoped go vet passes for capability, apierr, apiroutes, apiref, apispec,
  controller, internal/acceptance and tests/acceptance.
- Complete capability, apierr, route/reference/spec tests pass.
  internal/acceptance compiles and has no package-local tests.
- The W2 acceptance selection, including TestCredentials and all six new
  session tests, passes in 13.993 seconds. The selection covers credential,
  secret, source, authority, operator, deployment, data, engine, payment,
  credit, sizing, discovery, identity and queue HTTP tests and matching fuzz
  seed prefixes. Parked native cells remain skipped.
- Focused controller policy, session, issuance, row-cipher, registration and
  route-guard tests pass. The final focused run took 1.009 seconds.
- go run ./internal/commentcheck -base main . passes. Documentation mirrors,
  performance JSON and git diff --check pass.

The initial new session tests failed against the old handlers before the
implementation. The separate W1 backend overlay still fails as recorded above.
No full legacy-controller suite, socket, PostgreSQL, native placement or
performance pass is claimed. The lead retains product pipelines, independent
review, staging and landing. Scratch and its probes/caches are removed after
the checks. The default Git identity remains Korey's.

STATUS: CONTINUE

<!-- design:end -->
