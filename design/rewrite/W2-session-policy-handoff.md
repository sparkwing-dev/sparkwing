# W2 session policy continuation

<!-- design:begin -->

## Merge

The checkout began at 929ac52e1 with handoffs.md conflicted. Both additions
survive: W1's authority-bearing secret-read handoff and the integration branch's
approved W8 cancellation classification. No conflict markers remain. The Git
index still reports the file unmerged because the brief forbids Git writes.
The lead must stage it and finish the merge. Incoming changes to
internal/limits/deadline_test.go and sparkwing/sparkwingtest/host_test.go are
untouched.

## Implemented

Invariant: only one unambiguous, live session may authorize a request, and
failed session or CSRF proof must neither renew nor delete it.

PolicyRouter accepts the documented Session authorization header alongside
Bearer. A header selects its own presentation and cannot fall back to a live
cookie. Without a header, the default controller uses the existing strict
session selector. Duplicate and empty session cookies fail. Session-header
mutations verify CSRF against that selected session even when another session
cookie is present.

The default AuthenticationReader adapter consumes C1's single committed
snapshot and store clock. It applies the existing account-session license and
configured absolute lifetime to session authorities throughout the ancestry.
It returns canonical unavailable for noncanonical persistence faults without
exposing their diagnostics. Existing canonical refusals retain their codes.
It reads no extra SQL, receives no bearer and adds no new persistence contract,
cache, clock, lifetime limit or issuer.

The dashboard dispatches actual controller-router registrations to their own
proof boundary using the existing route inventory. Migrated routes avoid
legacy cookie pre-renewal and plaintext CSRF errors. Ordinary policy requests
read without renewal. The explicit session-renewal endpoint retains its
committed renewal and configured ceiling. Legacy registrations still use
their existing middleware while their cutovers remain unfinished.

POST /logout now registers through PolicyRouter with AuthBrowserFlow. The
verifier reads the live session without renewal, checks the bounded form,
same origin and the session-bound CSRF token, then supplies the verified
session to the handler. Browser and API logout share one deletion helper.
Success clears both cookies after deletion and redirects to /login. Failure
returns a canonical error. A deletion or key failure returns unavailable and
leaves the cookies intact. Audit attribution comes from the verified account.

The public auth docs, migration guide, changelog and embedded copies describe
the final behavior. API reference and OpenAPI generation produce no route
delta because /logout already existed. The discovery parser follows its new
endpoint declaration and tests the named proof independently.

## Deleted and ported

Deleted the 31-line legacy browser logout handler and replaced its direct
registration. Deleted 219 obsolete test/helper/import lines across
browser_login_test.go, session_hash_test.go and session_lifetime_test.go.
Seven old tests now have final-boundary coverage:

- TestBrowserLogoutNeedsTheSessionsTokenAndEndsIt becomes the three
  TestBrowserLogoutHTTP tests. They add generated invalid proofs, canonical
  refusals, unchanged expiry, audit attribution, cross-session preservation,
  cookie clearing, missing-key and real SQL deletion-failure checks.
- TestSessionRoute_ResolvesRawIDAndRepeatsTheCSRFToken is covered by
  TestSessionFlowHTTPRenewsWithNamedProof, including the stored account
  principal, session-bound CSRF token, role, grants and both transports.
- TestLogout_DeletesTheHashedRow is covered by
  TestSessionFlowHTTPLogoutUsesVerifiedSession. The obsolete body-selected
  logout request is removed.
- TestSessionRoute_RefusesRenewalPastTheAbsoluteCap,
  TestSessionRoute_ClampsRenewalToTheAbsoluteCap,
  TestSessionRoute_ExtendsARecentlyUsedSessionBeforeItsFinalHour and
  TestSessionRoute_RefusesASessionCreatedInTheFuture become
  TestSessionFlowHTTPLifetimeCases. They check default and configured ceilings,
  persisted/returned expiry equality, full renewal and row removal on refusal
  against nanosecond storage.

Eight new top-level HTTP tests pass. Three use testing/quick to generate
adversarial proofs and session ages. Existing session-flow properties and
TestCredentials store-sqlite cells still pass. Native route-matrix and transport
cells remain pending. No native acceptance cell was unskipped.

## Amendment and handoff disposition

Earlier completed work remains in W2-session-flow-handoff.md and the handoffs
it names. No completed implementation was rebuilt.

| Rows | Disposition |
| --- | --- |
| A1-01/03/05 | Shared decoder and node-service policies remain implemented. W3 owns production process transport and registration. |
| A1-08/09/19/26/55 | Mixed/named claims and here rules retain their HTTP boundaries. Concrete enrollment and execution identity remain integration work for W1/W3/W8/W9. |
| A1-11/16/17 | Sealed source and private execution projections remain. Consumer cutovers remain with their owners. |
| A1-14/15 | Failure reads and held-job cancellation retain their existing implementation and SQLite evidence. C1's cancellation-reason authentication projection remains open. |
| A1-20 | Member and Actions issuance remain implemented. Session-header member issuance now reaches the real issuer without browser pre-renewal. Bootstrap, machine and service provisioning, credential inventory and rotation remain W2 work. |
| A1-21/22/23 | Ordinary Session-header/cookie policy transport, lifetime enforcement and browser logout are implemented here. Login, OAuth, console, verified-peer and remaining legacy registrations stay open. |
| A1-24/25 | Existing source-release, plan-secret validation and credential read/write contracts remain. Missing W1 transaction and trusted intake adapters remain integration points. |
| A1-27/28 | Billing provenance helpers and verified committed-checkout transport remain. Production payment configuration and the other verified payment-event contracts remain open. |
| A1-29/30/31/32/33/34/35 | Existing signed-data and logical-record transports remain. Production authority/read/charge projections and real backend transfers remain integration points. |
| A1-38 | Launcher sync retains its declared interface and fake-backed evidence. Its production snapshot remains open. |
| A1-39/40/49/53 | Runner/local-controller, credential-file startup and native topology work remain with W3/W6/W8/W9/T1/lead. |

The W1/W2 session lifecycle handoff gains ordinary policy and browser-logout
coverage. Cancellation-reason delivery is not claimed fixed by this session.
The earlier credential reader/writer/rotation/audit and data/source handoffs
remain open with W1. W2 continues against their interfaces and fakes.

## Other contracts and remaining work

No foreign contract signature changed. The only contract clarification is
local C2 session transport and lifetime behavior. No new product-policy question
was introduced.

W1 still owns roleless and console session persistence and complete
ErrSessionBackend classification in LookupSession. Its authority-chain and
RowsAffected failures can still be misclassified by that method. The new
ordinary-policy snapshot adapter safely classifies raw backend failures, but
does not repair LookupSession or claim complete browser backend classification.

W1's final authority-bearing credential readers/writers, rotation and audit
methods are still absent from this checkout. Its legacy mutation and global
rotation methods are not fallbacks. Data authority/live-byte/download charging,
source permits, transactional enrollment/sizing/operator writers, billing and
launcher snapshots remain as recorded in earlier handoffs. W9's NewEngineClient
and NewDataClient still panic. No real integration through those stubs is
claimed.

W2 remains incomplete: browser login/OAuth and console/local identity, other
provisioning origins, capability listing/rotation, remaining legacy routes,
old auth code and payment-event/configuration work remain. Native HTTP/api.sock,
PostgreSQL, Windows/Darwin, physical storage and real-repository QA stay open.
The lead retains independent review, pipelines, staging and landing.

## Shared files

The only production caller/discovery migration outside internal/controller is
internal/apiroutes/apiroutes.go and its parser test. No removed exported Go API
required another package's production caller to change. Shared acceptance
edits are tests/acceptance/session_flow_http_test.go and the new
session_policy_http_test.go. Other shared edits are contracts/c2-credentials.md,
contracts/handoffs.md, contracts/t1-port-inventory.md, docs/auth.md,
docs/migrations/_unreleased.md, CHANGELOG.md and their generated embedded copies.
No W1 implementation is edited.

## Verification

Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W2/cache, and GOTMPDIR and TMPDIR under
/tmp/sw-astra-W2/tmp. Go tests use -p 4.

- Whole-repository go build -p 4 ./... passes.
- Scoped go vet passes for capability, apierr, apiroutes, apiref, apispec,
  controller, internal/acceptance and tests/acceptance.
- Complete capability, apierr, apiroutes, apiref and apispec suites pass.
- The credential/session acceptance selection passes. The final focused
  session/policy/credential/registration selection passes for acceptance
  (13.991 seconds) and controller (1.423 seconds).
- go run ./internal/commentcheck -base main . passes across 1,169 files.
- API generation and check-api-spec.sh pass. No generated API route or schema
  change remains.
- Documentation/changelog mirror tests, sync-docs.sh --check and git diff
  --check pass. The default Git identity remains Korey's and no core.worktree
  override was introduced.

With that environment, reproduce the focused boundary checks using:

```sh
go -C /home/bombuntu/.xwing/envs/1080/sparkwing test -p 4 ./tests/acceptance ./internal/controller -run '^(TestSession|TestBrowserLogoutHTTP|TestPolicyRouter|TestAuthorizedHandlersDoNotAuthorize|TestCredential|Test.*Registration|Test.*Route.*Guard)' -count=1
go -C /home/bombuntu/.xwing/envs/1080/sparkwing test -p 4 ./internal/capability ./internal/apierr ./internal/apiroutes ./internal/apiref ./internal/apispec -count=1
go -C /home/bombuntu/.xwing/envs/1080/sparkwing test -p 4 ./pkg/docs -run '^(TestPkgDocsContentMatchesDocsRoot|TestEmbeddedChangelogMatchesRoot)$' -count=1
go -C /home/bombuntu/.xwing/envs/1080/sparkwing build -p 4 ./...
go -C /home/bombuntu/.xwing/envs/1080/sparkwing run ./internal/commentcheck -base main .
```

The first new tests failed against the old implementation. A broader controller
selection then exposed the five obsolete token-table session fixtures, which
are ported and removed above. Their final selector passes. Bootstrap-login
tests remain tied to the retired token fixture and are not claimed passing.
Full legacy-controller, socket, PostgreSQL, native placement and performance
checks are not claimed. No Sparkwing binary or git write ran.

Friction: cold compilation and legacy token-table fixtures. The lead still
needs to mark the resolved merge file in the index. Scratch and all its caches
are deleted after final verification.

STATUS: CONTINUE

<!-- design:end -->
